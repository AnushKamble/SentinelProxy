package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/sentinelproxy/sentinelproxy/internal/admin"
	"github.com/sentinelproxy/sentinelproxy/internal/balancer"
	"github.com/sentinelproxy/sentinelproxy/internal/circuitbreaker"
	"github.com/sentinelproxy/sentinelproxy/internal/config"
	"github.com/sentinelproxy/sentinelproxy/internal/proxy"
	"github.com/sentinelproxy/sentinelproxy/internal/router"
)

// App manages the lifecycle of the SentinelProxy gateway.
type App struct {
	configPath    string
	sm            *config.SnapshotManager
	admin         *admin.Handler
	server        *http.Server
	transport     *http.Transport
	router        *router.Router
	balancers     map[string]balancer.Balancer                  // key: route.ID
	routeBackends map[string][]*balancer.Backend                // key: route.ID
	breakers      map[string]*circuitbreaker.CircuitBreaker     // key: routeID + "/" + backendID
	proxies       map[string]*proxy.ProxyHandler                // key: backend.ID
	logger        *slog.Logger
	listener      net.Listener
	mu            sync.RWMutex
}

type breakerEligibilityChecker struct {
	routeID  string
	breakers map[string]*circuitbreaker.CircuitBreaker
}

func (c *breakerEligibilityChecker) IsEligible(backendID string) bool {
	cbKey := c.routeID + "/" + backendID
	if cb, ok := c.breakers[cbKey]; ok {
		// If breaker is OPEN and open duration hasn't expired, backend is not eligible
		if cb.State() == circuitbreaker.StateOpen {
			return false
		}
	}
	return true
}

// BreakerPermitObserver wraps a circuit breaker permit in a proxy Observer.
type BreakerPermitObserver struct {
	permit     circuitbreaker.Permit
	classifier *circuitbreaker.Classifier
	statusCode int
}

func (o *BreakerPermitObserver) OnStart() {}

func (o *BreakerPermitObserver) OnHeaders(status int, _ http.Header) {
	o.statusCode = status
}

func (o *BreakerPermitObserver) OnComplete(outcome proxy.Outcome, err error) {
	if o.permit == nil {
		return
	}

	if outcome == proxy.OutcomeCanceled {
		o.permit.ReleaseCancel()
		return
	}

	isFailure := o.classifier.IsFailure(o.statusCode, err)
	if outcome == proxy.OutcomeTimeout || outcome == proxy.OutcomeFailure || isFailure {
		o.permit.RecordFailure()
	} else {
		o.permit.RecordSuccess()
	}
}

// New creates and initializes an App from a validated configuration.
func New(cfg *config.Config, configPath string) (*App, error) {
	snap, err := config.NewRuntimeSnapshot(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create initial runtime snapshot: %w", err)
	}

	sm := config.NewSnapshotManager(snap)
	adminHandler := admin.NewHandler(sm, "v1.0.0")

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	tpCfg := proxy.DefaultTransportConfig()
	transport := proxy.NewPooledTransport(tpCfg)

	rtr := router.NewRouter(cfg.Routes)
	balancers := make(map[string]balancer.Balancer)
	routeBackends := make(map[string][]*balancer.Backend)
	breakers := make(map[string]*circuitbreaker.CircuitBreaker)
	proxies := make(map[string]*proxy.ProxyHandler)

	for _, r := range cfg.Routes {
		if r.LoadBalancer == "least_connections" {
			balancers[r.ID] = balancer.NewLeastConnectionsBalancer()
		} else {
			balancers[r.ID] = balancer.NewRoundRobinBalancer()
		}

		var bList []*balancer.Backend
		for _, b := range r.Backends {
			targetURL, err := url.Parse(b.URL)
			if err != nil {
				return nil, fmt.Errorf("invalid backend url %q: %w", b.URL, err)
			}
			backendObj := &balancer.Backend{
				ID:     b.ID,
				URL:    targetURL,
				Weight: b.Weight,
			}
			bList = append(bList, backendObj)

			// Setup circuit breaker per route and backend pair
			cbKey := r.ID + "/" + b.ID
			breakers[cbKey] = circuitbreaker.NewCircuitBreaker(r.ID, b.ID, r.CircuitBreaker, nil, func(rt, bk string, from, to circuitbreaker.State) {
				logger.Warn("circuit breaker state transition", "route_id", rt, "backend_id", bk, "from", from, "to", to)
			})

			if _, exists := proxies[b.ID]; !exists {
				proxies[b.ID] = proxy.NewProxyHandler(targetURL, transport)
			}
		}
		routeBackends[r.ID] = bList
	}

	app := &App{
		configPath:    configPath,
		sm:            sm,
		admin:         adminHandler,
		transport:     transport,
		router:        rtr,
		balancers:     balancers,
		routeBackends: routeBackends,
		breakers:      breakers,
		proxies:       proxies,
		logger:        logger,
	}

	mux := http.NewServeMux()
	adminHandler.RegisterRoutes(mux)
	mux.HandleFunc("/", app.serveHTTP)

	app.server = &http.Server{
		Addr:              cfg.Server.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
	}

	return app, nil
}

// ServeHTTP routes incoming requests through router, balancer, circuit breaker, and reverse proxy.
func (a *App) serveHTTP(w http.ResponseWriter, r *http.Request) {
	match := a.router.Match(r.URL.Path, r.Method)
	if match.IsReserved {
		return
	}

	if !match.PathMatched {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Not Found"}`))
		return
	}

	if !match.MethodAllowed {
		w.Header().Set("Allow", strings.Join(match.AllowedMethods, ", "))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte(`{"error":"Method Not Allowed"}`))
		return
	}

	route := match.Route
	a.mu.RLock()
	bal := a.balancers[route.ID]
	candidates := a.routeBackends[route.ID]
	a.mu.RUnlock()

	if bal == nil || len(candidates) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"no backends available"}`))
		return
	}

	checker := &breakerEligibilityChecker{
		routeID:  route.ID,
		breakers: a.breakers,
	}

	// Select candidate backend through load balancer filtered by circuit breaker state
	chosenBackend, releaseFunc, err := bal.Select(candidates, checker)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"no eligible backend candidate available"}`))
		return
	}
	defer releaseFunc()

	cbKey := route.ID + "/" + chosenBackend.ID
	a.mu.RLock()
	cb := a.breakers[cbKey]
	ph := a.proxies[chosenBackend.ID]
	a.mu.RUnlock()

	if cb == nil || ph == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"backend proxy or breaker not configured"}`))
		return
	}

	permit, err := cb.TryAcquire()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"circuit breaker open"}`))
		return
	}

	// Attach permit observer to track complete stream lifecycle
	obs := &BreakerPermitObserver{
		permit:     permit,
		classifier: cb.Classifier(),
	}

	ctx, cancel := context.WithTimeout(r.Context(), route.Timeout)
	defer cancel()

	observedCtx := proxy.WithObserver(ctx, obs)
	ph.ServeHTTP(w, r.WithContext(observedCtx))
}

// Start begins listening on the configured address.
func (a *App) Start() error {
	ln, err := net.Listen("tcp", a.server.Addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", a.server.Addr, err)
	}
	a.listener = ln
	a.logger.Info("SentinelProxy started", "addr", ln.Addr().String())

	if err := a.server.Serve(ln); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}

// Addr returns the active listener address.
func (a *App) Addr() string {
	if a.listener != nil {
		return a.listener.Addr().String()
	}
	return a.server.Addr
}

// Shutdown gracefully stops the server and closes idle transport connections.
func (a *App) Shutdown(ctx context.Context) error {
	a.admin.SetReady(false)
	a.logger.Info("SentinelProxy shutting down...")

	err := a.server.Shutdown(ctx)
	a.transport.CloseIdleConnections()
	return err
}
