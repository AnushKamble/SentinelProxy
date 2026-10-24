package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sentinelproxy/sentinelproxy/internal/proxy"
)

type testObserver struct {
	mu         sync.Mutex
	started    bool
	statusCode int
	completed  bool
	outcome    proxy.Outcome
	err        error
}

func (o *testObserver) OnStart() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.started = true
}

func (o *testObserver) OnHeaders(status int, _ http.Header) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.statusCode = status
}

func (o *testObserver) OnComplete(outcome proxy.Outcome, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.completed = true
	o.outcome = outcome
	o.err = err
}

func TestProxyHandler_ForwardingAndObserver(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Received-Query", r.URL.RawQuery)
		w.Header().Set("X-Received-Header", r.Header.Get("X-Test-Req"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("echo: " + string(body)))
	}))
	defer backend.Close()

	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("failed to parse backend url: %v", err)
	}

	transport := proxy.NewPooledTransport(proxy.DefaultTransportConfig())
	ph := proxy.NewProxyHandler(backendURL, transport)

	obs := &testObserver{}
	req := httptest.NewRequest(http.MethodPost, "/foo?q=test", strings.NewReader("hello world"))
	req.Header.Set("X-Test-Req", "sentinel")
	req = req.WithContext(proxy.WithObserver(req.Context(), obs))
	w := httptest.NewRecorder()

	ph.ServeHTTP(w, req)
	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
	if string(body) != "echo: hello world" {
		t.Errorf("expected 'echo: hello world', got %q", string(body))
	}
	if resp.Header.Get("X-Received-Query") != "q=test" {
		t.Errorf("expected query preserved, got %q", resp.Header.Get("X-Received-Query"))
	}
	if resp.Header.Get("X-Received-Header") != "sentinel" {
		t.Errorf("expected header forwarded, got %q", resp.Header.Get("X-Received-Header"))
	}

	obs.mu.Lock()
	defer obs.mu.Unlock()
	if !obs.started {
		t.Errorf("expected observer OnStart to be called")
	}
	if !obs.completed {
		t.Errorf("expected observer OnComplete to be called")
	}
	if obs.outcome != proxy.OutcomeSuccess {
		t.Errorf("expected OutcomeSuccess, got %v", obs.outcome)
	}
}

func TestProxyHandler_TimeoutHandling(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	backendURL, _ := url.Parse(backend.URL)
	transport := proxy.NewPooledTransport(proxy.DefaultTransportConfig())
	ph := proxy.NewProxyHandler(backendURL, transport)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	obs := &testObserver{}
	req := httptest.NewRequest(http.MethodGet, "/timeout", nil).WithContext(proxy.WithObserver(ctx, obs))
	w := httptest.NewRecorder()

	ph.ServeHTTP(w, req)
	resp := w.Result()
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Errorf("expected status 504 Gateway Timeout, got %d", resp.StatusCode)
	}

	obs.mu.Lock()
	defer obs.mu.Unlock()
	if !obs.completed {
		t.Errorf("expected observer OnComplete to be called on timeout")
	}
	if obs.outcome != proxy.OutcomeTimeout {
		t.Errorf("expected OutcomeTimeout, got %v", obs.outcome)
	}
}

func TestProxyHandler_BadGatewayOnConnectionRefused(t *testing.T) {
	// Point to closed port
	backendURL, _ := url.Parse("http://127.0.0.1:45678")
	transport := proxy.NewPooledTransport(proxy.DefaultTransportConfig())
	ph := proxy.NewProxyHandler(backendURL, transport)

	obs := &testObserver{}
	req := httptest.NewRequest(http.MethodGet, "/down", nil).WithContext(proxy.WithObserver(context.Background(), obs))
	w := httptest.NewRecorder()

	ph.ServeHTTP(w, req)
	resp := w.Result()
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected status 502 Bad Gateway, got %d", resp.StatusCode)
	}

	obs.mu.Lock()
	defer obs.mu.Unlock()
	if !obs.completed {
		t.Errorf("expected observer OnComplete to be called on connection refused")
	}
	if obs.outcome != proxy.OutcomeFailure {
		t.Errorf("expected OutcomeFailure, got %v", obs.outcome)
	}
}
