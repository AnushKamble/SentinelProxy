package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/sentinelproxy/sentinelproxy/internal/admin"
	"github.com/sentinelproxy/sentinelproxy/internal/auth"
	"github.com/sentinelproxy/sentinelproxy/internal/balancer"
	"github.com/sentinelproxy/sentinelproxy/internal/circuitbreaker"
	"github.com/sentinelproxy/sentinelproxy/internal/config"
	"github.com/sentinelproxy/sentinelproxy/internal/health"
	"github.com/sentinelproxy/sentinelproxy/internal/middleware"
	"github.com/sentinelproxy/sentinelproxy/internal/proxy"
	"github.com/sentinelproxy/sentinelproxy/internal/ratelimit"
	"github.com/sentinelproxy/sentinelproxy/internal/router"
)

// App manages the lifecycle of the SentinelProxy gateway.
type App struct {
	configPath     string
	sm             *config.SnapshotManager
	admin          *admin.Handler
	server         *http.Server
	transport      *http.Transport
	router         *router.Router
	balancers      map[string]balancer.Balancer                  // key: route.ID
	routeBackends  map[string][]*balancer.Backend                // key: route.ID
	breakers       map[string]*circuitbreaker.CircuitBreaker     // key: routeID + "/" + backendID
	healthRegistry *health.Registry
	healthChecker  *health.Checker
	limiters       map[string]*ratelimit.Limiter                 // key: route.ID
	jwtVerifier    auth.Verifier
	keyExtractor   *ratelimit.KeyExtractor
	proxies        map[string]*proxy.ProxyHandler                // key: backend.ID
	logger         *slog.Logger
	listener       net.Listener
	mu             sync.RWMutex
}

type backendEligibilityChecker struct {
	routeID  string
	breakers map[string]*circuitbreaker.CircuitBreaker
	health   *health.Registry
}

func (c *backendEligibilityChecker) IsEligible(backendID string) bool {
	if c.health != nil && !c.health.IsHealthy(c.routeID, backendID) {
		return false
	}

	cbKey := c.routeID + "/" + backendID
	if cb, ok := c.breakers[cbKey]; ok {
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
	limiters := make(map[string]*ratelimit.Limiter)
	proxies := make(map[string]*proxy.ProxyHandler)
	healthReg := health.NewRegistry()
	var probeTargets []health.ProbeTarget

	keyExtractor, err := ratelimit.NewKeyExtractor(cfg.Security.TrustedProxyCIDRs)
	if err != nil {
		return nil, fmt.Errorf("invalid trusted_proxy_cidrs: %w", err)
	}

	// Initialize JWT verifier if any route requires authentication
	var jwtVerifier auth.Verifier
	needsAuth := cfg.Security.JWT.Enabled
	for _, r := range cfg.Routes {
		if r.AuthRequired {
			needsAuth = true
			break
		}
	}

	if needsAuth {
		verifier, err := auth.NewHMACVerifier(cfg.Security.JWT)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize jwt verifier: %w", err)
		}
		jwtVerifier = verifier
	}

	for _, r := range cfg.Routes {
		if r.LoadBalancer == "least_connections" {
			balancers[r.ID] = balancer.NewLeastConnectionsBalancer()
		} else {
			balancers[r.ID] = balancer.NewRoundRobinBalancer()
		}

		if r.RateLimit.Enabled {
			limiters[r.ID] = ratelimit.NewLimiter(r.ID, r.RateLimit, nil)
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

			cbKey := r.ID + "/" + b.ID
			breakers[cbKey] = circuitbreaker.NewCircuitBreaker(r.ID, b.ID, r.CircuitBreaker, nil, func(rt, bk string, from, to circuitbreaker.State) {
				logger.Warn("circuit breaker state transition", "route_id", rt, "backend_id", bk, "from", from, "to", to)
			})

			if r.HealthCheck.Path != "" {
				probeURL := strings.TrimSuffix(b.URL, "/") + r.HealthCheck.Path
				probeTargets = append(probeTargets, health.ProbeTarget{
					RouteID:   r.ID,
					BackendID: b.ID,
					ProbeURL:  probeURL,
					Config:    r.HealthCheck,
				})
			}

			if _, exists := proxies[b.ID]; !exists {
				proxies[b.ID] = proxy.NewProxyHandler(targetURL, transport)
			}
		}
		routeBackends[r.ID] = bList
	}

	checker := health.NewChecker(healthReg, probeTargets, func(rt, bk string, healthy bool) {
		logger.Warn("backend health state change", "route_id", rt, "backend_id", bk, "healthy", healthy)
	})

	app := &App{
		configPath:     configPath,
		sm:             sm,
		admin:          adminHandler,
		transport:      transport,
		router:         rtr,
		balancers:      balancers,
		routeBackends:  routeBackends,
		breakers:       breakers,
		healthRegistry: healthReg,
		healthChecker:  checker,
		limiters:       limiters,
		jwtVerifier:    jwtVerifier,
		keyExtractor:   keyExtractor,
		proxies:        proxies,
		logger:         logger,
	}

	mux := http.NewServeMux()
	adminHandler.RegisterRoutes(mux)
	mux.HandleFunc("/", app.serveHTTP)

	// Wrap root with defensive security headers
	securedHandler := middleware.SecurityHeadersMiddleware(mux)

	app.server = &http.Server{
		Addr:              cfg.Server.ListenAddr,
		Handler:           securedHandler,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
	}

	return app, nil
}

// ServeHTTP routes incoming requests through security, auth, rate limiter, balancer, breaker, and proxy.
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
	snap := a.sm.Get()
	headerName := "X-Request-ID"
	if snap != nil && snap.Config != nil && snap.Config.Security.RequestIDHeader != "" {
		headerName = snap.Config.Security.RequestIDHeader
	}
	reqID := middleware.ValidateAndExtractRequestID(r, headerName)
	w.Header().Set(headerName, reqID)
	reqCtx := middleware.WithRequestID(r.Context(), reqID)

	var authenticatedSub string

	// Enforce JWT authentication if route requires it
	if route.AuthRequired {
		if a.jwtVerifier == nil {
			// Fail closed if verifier is unconfigured
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}

		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}

		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
		claims, err := a.jwtVerifier.Verify(tokenStr)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}

		if sub, ok := claims["sub"].(string); ok {
			authenticatedSub = sub
		}
	}

	// Enforce local token-bucket rate limiting if enabled for this route
	if route.RateLimit.Enabled {
		a.mu.RLock()
		limiter := a.limiters[route.ID]
		a.mu.RUnlock()

		if limiter != nil {
			clientKey := a.keyExtractor.ExtractKey(r, authenticatedSub)
			allowed, retryAfter := limiter.Allow(clientKey)
			if !allowed {
				retrySeconds := int(retryAfter.Seconds())
				if retrySeconds < 1 {
					retrySeconds = 1
				}
				w.Header().Set("Retry-After", strconv.Itoa(retrySeconds))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":"rate limit exceeded"}`))
				return
			}
		}
	}

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

	checker := &backendEligibilityChecker{
		routeID:  route.ID,
		breakers: a.breakers,
		health:   a.healthRegistry,
	}

	// Select candidate backend through load balancer filtered by health and circuit breaker state
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

	ctx, cancel := context.WithTimeout(reqCtx, route.Timeout)
	defer cancel()

	observedCtx := proxy.WithObserver(ctx, obs)
	ph.ServeHTTP(w, r.WithContext(observedCtx))
}

// Start begins listening on the configured address and starts active health checks.
func (a *App) Start() error {
	ln, err := net.Listen("tcp", a.server.Addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", a.server.Addr, err)
	}
	a.listener = ln
	a.logger.Info("SentinelProxy started", "addr", ln.Addr().String())

	if a.healthChecker != nil {
		a.healthChecker.Start(context.Background())
	}

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

// Shutdown gracefully stops the server, health checks, and closes idle transport connections.
func (a *App) Shutdown(ctx context.Context) error {
	a.admin.SetReady(false)
	a.logger.Info("SentinelProxy shutting down...")

	if a.healthChecker != nil {
		a.healthChecker.Stop()
	}

	err := a.server.Shutdown(ctx)
	a.transport.CloseIdleConnections()
	return err
}
