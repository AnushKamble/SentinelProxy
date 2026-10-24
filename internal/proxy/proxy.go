package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// Outcome represents the final classification of an upstream request.
type Outcome int

const (
	OutcomeSuccess Outcome = iota
	OutcomeFailure
	OutcomeTimeout
	OutcomeCanceled
)

// Observer tracks the full lifecycle of an upstream exchange through body completion.
type Observer interface {
	OnStart()
	OnHeaders(statusCode int, header http.Header)
	OnComplete(outcome Outcome, err error)
}

type observerKey struct{}

// WithObserver attaches an Observer to the request context.
func WithObserver(ctx context.Context, obs Observer) context.Context {
	return context.WithValue(ctx, observerKey{}, obs)
}

// ObserverFromContext extracts the Observer from context if present.
func ObserverFromContext(ctx context.Context) Observer {
	if obs, ok := ctx.Value(observerKey{}).(Observer); ok {
		return obs
	}
	return nil
}

// ObservedRoundTripper wraps an http.RoundTripper to observe start, headers, and complete body read.
type ObservedRoundTripper struct {
	Base http.RoundTripper
}

func (ort *ObservedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	obs := ObserverFromContext(req.Context())
	if obs != nil {
		obs.OnStart()
	}

	resp, err := ort.Base.RoundTrip(req)
	if err != nil {
		if obs != nil {
			outcome := classifyError(req.Context(), err)
			obs.OnComplete(outcome, err)
		}
		return nil, err
	}

	if obs != nil {
		obs.OnHeaders(resp.StatusCode, resp.Header)
		// Wrap response body to observe body close or EOF
		resp.Body = newObservedBody(resp.Body, req.Context(), obs, resp.StatusCode)
	}

	return resp, nil
}

type observedBody struct {
	inner      io.ReadCloser
	ctx        context.Context
	obs        Observer
	statusCode int
	once       sync.Once
	readErr    error
}

func newObservedBody(inner io.ReadCloser, ctx context.Context, obs Observer, statusCode int) *observedBody {
	return &observedBody{
		inner:      inner,
		ctx:        ctx,
		obs:        obs,
		statusCode: statusCode,
	}
}

func (b *observedBody) Read(p []byte) (n int, err error) {
	n, err = b.inner.Read(p)
	if err != nil && err != io.EOF {
		b.readErr = err
	}
	if err == io.EOF {
		b.finish(nil)
	}
	return n, err
}

func (b *observedBody) Close() error {
	err := b.inner.Close()
	b.finish(err)
	return err
}

func (b *observedBody) finish(closeErr error) {
	b.once.Do(func() {
		if b.obs == nil {
			return
		}
		finalErr := b.readErr
		if finalErr == nil {
			finalErr = closeErr
		}
		outcome := classifyOutcome(b.ctx, b.statusCode, finalErr)
		b.obs.OnComplete(outcome, finalErr)
	})
}

func classifyError(ctx context.Context, err error) Outcome {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return OutcomeTimeout
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return OutcomeCanceled
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return OutcomeTimeout
	}
	return OutcomeFailure
}

func classifyOutcome(ctx context.Context, statusCode int, err error) Outcome {
	if err != nil {
		return classifyError(ctx, err)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return OutcomeTimeout
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return OutcomeCanceled
	}
	return OutcomeSuccess
}

// ProxyHandler encapsulates a reverse proxy targeting a specific upstream backend.
type ProxyHandler struct {
	Target       *url.URL
	ReverseProxy *httputil.ReverseProxy
}

// NewProxyHandler builds an httputil.ReverseProxy with strict error handling and streaming.
func NewProxyHandler(target *url.URL, transport http.RoundTripper) *ProxyHandler {
	observedTransport := &ObservedRoundTripper{Base: transport}

	rp := &httputil.ReverseProxy{
		Transport:     observedTransport,
		FlushInterval: 50 * time.Millisecond, // responsive streaming
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
			// Inject W3C Trace Context
			otel.GetTextMapPropagator().Inject(pr.In.Context(), propagation.HeaderCarrier(pr.Out.Header))
			// Ensure host header matches upstream target host
			pr.Out.Host = target.Host
			// Remove dangerous or unwanted hop headers
			pr.Out.Header.Del("Keep-Alive")
			pr.Out.Header.Del("Proxy-Authenticate")
			pr.Out.Header.Del("Proxy-Authorization")
			pr.Out.Header.Del("TE")
			pr.Out.Header.Del("Trailer")
			pr.Out.Header.Del("Transfer-Encoding")
			pr.Out.Header.Del("Upgrade")
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(r.Context().Err(), context.Canceled) {
				// Client canceled request
				return
			}

			w.Header().Set("Content-Type", "application/json")
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(r.Context().Err(), context.DeadlineExceeded) {
				w.WriteHeader(http.StatusGatewayTimeout)
				_, _ = w.Write([]byte(`{"error":"gateway timeout"}`))
				return
			}

			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				w.WriteHeader(http.StatusGatewayTimeout)
				_, _ = w.Write([]byte(`{"error":"upstream timeout"}`))
				return
			}

			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"bad gateway"}`))
		},
	}

	return &ProxyHandler{
		Target:       target,
		ReverseProxy: rp,
	}
}

// ServeHTTP forwards the request through the reverse proxy.
func (p *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.ReverseProxy.ServeHTTP(w, r)
}
