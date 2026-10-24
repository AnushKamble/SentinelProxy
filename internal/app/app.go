package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

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
	"github.com/sentinelproxy/sentinelproxy/internal/telemetry"
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
	metrics        *telemetry.Metrics
	tracer         *telemetry.TracerManager
	proxies        map[string]*proxy.ProxyHandler                // key: backend.ID
	logger         *slog.Logger
	listener       net.Listener
	watcher        *config.Watcher
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

// BreakerPermitObserver wraps a circuit breaker permit in a proxy Observer with metrics.
type BreakerPermitObserver struct {
	permit     circuitbreaker.Permit
	classifier *circuitbreaker.Classifier
	metrics    *telemetry.Metrics
	routeID    string
	backendID  string
	startTime  time.Time
	statusCode int
}

func (o *BreakerPermitObserver) OnStart() {
	o.startTime = time.Now()
}

func (o *BreakerPermitObserver) OnHeaders(status int, _ http.Header) {
	o.statusCode = status
}

func (o *BreakerPermitObserver) OnComplete(outcome proxy.Outcome, err error) {
	upstreamDuration := time.Since(o.startTime)
	outcomeStr := "success"

	if o.permit != nil {
		if outcome == proxy.OutcomeCanceled {
			outcomeStr = "canceled"
			o.permit.ReleaseCancel()
		} else {
			isFailure := o.classifier.IsFailure(o.statusCode, err)
			if outcome == proxy.OutcomeTimeout {
				outcomeStr = "timeout"
				o.permit.RecordFailure()
			} else if outcome == proxy.OutcomeFailure || isFailure {
				outcomeStr = "failure"
				o.permit.RecordFailure()
			} else {
				o.permit.RecordSuccess()
			}
		}
	}

	if o.metrics != nil {
		o.metrics.RecordUpstream(o.routeID, o.backendID, outcomeStr, o.statusCode, upstreamDuration)
	}
}

type statusResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *statusResponseWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

// New creates and initializes an App from a validated configuration.
func New(cfg *config.Config, configPath string) (*App, error) {
	snap, err := config.NewRuntimeSnapshot(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create initial runtime snapshot: %w", err)
	}

	sm := config.NewSnapshotManager(snap)
	metrics := telemetry.NewMetrics(nil)
	adminHandler := admin.NewHandler(sm, "v1.0.0", metrics.Registry)

	logger := telemetry.NewLogger(cfg.Observability.LogLevel, cfg.Observability.LogFormat)
	tracer, err := telemetry.NewTracerManager(cfg.Observability.Tracing.ServiceName, cfg.Observability.Tracing.SamplingRatio)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize tracer: %w", err)
	}

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
				metrics.RecordCircuitBreakerTransition(rt, bk, string(from), string(to))
				metrics.SetCircuitBreakerState(rt, bk, string(to))
				logger.Warn("circuit breaker state transition", "route_id", rt, "backend_id", bk, "from", from, "to", to)
			})
			metrics.SetCircuitBreakerState(r.ID, b.ID, "CLOSED")

			if r.HealthCheck.Path != "" {
				probeURL := strings.TrimSuffix(b.URL, "/") + r.HealthCheck.Path
				probeTargets = append(probeTargets, health.ProbeTarget{
					RouteID:   r.ID,
					BackendID: b.ID,
					ProbeURL:  probeURL,
					Config:    r.HealthCheck,
				})
			}
			metrics.SetBackendHealth(r.ID, b.ID, true)

			if _, exists := proxies[b.ID]; !exists {
				proxies[b.ID] = proxy.NewProxyHandler(targetURL, transport)
			}
		}
		routeBackends[r.ID] = bList
	}

	checker := health.NewChecker(healthReg, probeTargets, func(rt, bk string, healthy bool) {
		metrics.SetBackendHealth(rt, bk, healthy)
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
		metrics:        metrics,
		tracer:         tracer,
		proxies:        proxies,
		logger:         logger,
	}

	mux := http.NewServeMux()
	adminHandler.RegisterRoutes(mux)
	mux.HandleFunc("/", app.serveHTTP)

	securedHandler := middleware.SecurityHeadersMiddleware(mux)

	app.server = &http.Server{
		Addr:              cfg.Server.ListenAddr,
		Handler:           securedHandler,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
	}

	return app, nil
}

// Reload atomically parses, validates, and swaps configuration with state continuity preservation.
func (a *App) Reload() error {
	newCfg, err := config.LoadFromFile(a.configPath)
	if err != nil {
		a.metrics.RecordConfigReload(false)
		a.logger.Error("config reload rejected: invalid configuration", "error", err)
		return fmt.Errorf("reload validation failed: %w", err)
	}

	newSnap, err := config.NewRuntimeSnapshot(newCfg)
	if err != nil {
		a.metrics.RecordConfigReload(false)
		a.logger.Error("failed to construct candidate runtime snapshot", "error", err)
		return fmt.Errorf("runtime snapshot failed: %w", err)
	}

	newRouter := router.NewRouter(newCfg.Routes)
	newBalancers := make(map[string]balancer.Balancer)
	newRouteBackends := make(map[string][]*balancer.Backend)
	newBreakers := make(map[string]*circuitbreaker.CircuitBreaker)
	newLimiters := make(map[string]*ratelimit.Limiter)
	newProxies := make(map[string]*proxy.ProxyHandler)
	var newProbeTargets []health.ProbeTarget

	a.mu.RLock()
	oldBreakers := a.breakers
	oldLimiters := a.limiters
	oldProxies := a.proxies
	a.mu.RUnlock()

	for _, r := range newCfg.Routes {
		if r.LoadBalancer == "least_connections" {
			newBalancers[r.ID] = balancer.NewLeastConnectionsBalancer()
		} else {
			newBalancers[r.ID] = balancer.NewRoundRobinBalancer()
		}

		// Transfer rate limiter state if route config matches
		if r.RateLimit.Enabled {
			if oldLim, exists := oldLimiters[r.ID]; exists {
				newLimiters[r.ID] = oldLim // Preserve key token bucket state
			} else {
				newLimiters[r.ID] = ratelimit.NewLimiter(r.ID, r.RateLimit, nil)
			}
		}

		var bList []*balancer.Backend
		for _, b := range r.Backends {
			targetURL, err := url.Parse(b.URL)
			if err != nil {
				return err
			}
			backendObj := &balancer.Backend{
				ID:     b.ID,
				URL:    targetURL,
				Weight: b.Weight,
			}
			bList = append(bList, backendObj)

			// State continuity: transfer circuit breaker if unchanged identity exists
			cbKey := r.ID + "/" + b.ID
			if oldCb, exists := oldBreakers[cbKey]; exists {
				newBreakers[cbKey] = oldCb // Preserve circuit breaker state (e.g. still OPEN)
			} else {
				newBreakers[cbKey] = circuitbreaker.NewCircuitBreaker(r.ID, b.ID, r.CircuitBreaker, nil, func(rt, bk string, from, to circuitbreaker.State) {
					a.metrics.RecordCircuitBreakerTransition(rt, bk, string(from), string(to))
					a.metrics.SetCircuitBreakerState(rt, bk, string(to))
					a.logger.Warn("circuit breaker state transition", "route_id", rt, "backend_id", bk, "from", from, "to", to)
				})
				a.metrics.SetCircuitBreakerState(r.ID, b.ID, "CLOSED")
			}

			if r.HealthCheck.Path != "" {
				probeURL := strings.TrimSuffix(b.URL, "/") + r.HealthCheck.Path
				newProbeTargets = append(newProbeTargets, health.ProbeTarget{
					RouteID:   r.ID,
					BackendID: b.ID,
					ProbeURL:  probeURL,
					Config:    r.HealthCheck,
				})
			}

			// Reuse existing proxy handler if unchanged
			if oldPh, exists := oldProxies[b.ID]; exists && oldPh.Target.String() == targetURL.String() {
				newProxies[b.ID] = oldPh
			} else {
				newProxies[b.ID] = proxy.NewProxyHandler(targetURL, a.transport)
			}
		}
		newRouteBackends[r.ID] = bList
	}

	// Atomic swap
	a.mu.Lock()
	a.sm.Swap(newSnap)
	a.router = newRouter
	a.balancers = newBalancers
	a.routeBackends = newRouteBackends
	a.breakers = newBreakers
	a.limiters = newLimiters
	a.proxies = newProxies
	a.mu.Unlock()

	a.metrics.RecordConfigReload(true)
	a.logger.Info("atomic configuration hot reload completed successfully", "fingerprint", newSnap.Fingerprint)
	return nil
}

// ServeHTTP routes incoming requests through security, auth, rate limiter, balancer, breaker, proxy, and telemetry.
func (a *App) serveHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	a.mu.RLock()
	currentRouter := a.router
	a.mu.RUnlock()

	match := currentRouter.Match(r.URL.Path, r.Method)
	if match.IsReserved {
		return
	}

	snap := a.sm.Get()
	headerName := "X-Request-ID"
	if snap != nil && snap.Config != nil && snap.Config.Security.RequestIDHeader != "" {
		headerName = snap.Config.Security.RequestIDHeader
	}
	reqID := middleware.ValidateAndExtractRequestID(r, headerName)
	w.Header().Set(headerName, reqID)
	reqCtx := middleware.WithRequestID(r.Context(), reqID)

	routeID := "unknown"
	if match.Route != nil {
		routeID = match.Route.ID
	}

	spanCtx, span := a.tracer.StartInboundSpan(r.WithContext(reqCtx), routeID)
	defer span.End()

	if !match.PathMatched {
		duration := time.Since(start)
		a.metrics.RecordRequest("unknown", r.Method, http.StatusNotFound, duration)
		telemetry.LogRequest(spanCtx, a.logger, "unknown", r.Method, r.URL.Path, http.StatusNotFound, duration.Milliseconds(), "", "not_found")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Not Found"}`))
		return
	}

	if !match.MethodAllowed {
		duration := time.Since(start)
		a.metrics.RecordRequest(routeID, r.Method, http.StatusMethodNotAllowed, duration)
		telemetry.LogRequest(spanCtx, a.logger, routeID, r.Method, r.URL.Path, http.StatusMethodNotAllowed, duration.Milliseconds(), "", "method_not_allowed")
		w.Header().Set("Allow", strings.Join(match.AllowedMethods, ", "))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte(`{"error":"Method Not Allowed"}`))
		return
	}

	route := match.Route
	var authenticatedSub string

	// Enforce JWT authentication if route requires it
	if route.AuthRequired {
		if a.jwtVerifier == nil {
			duration := time.Since(start)
			a.metrics.RecordRequest(route.ID, r.Method, http.StatusUnauthorized, duration)
			telemetry.LogRequest(spanCtx, a.logger, route.ID, r.Method, r.URL.Path, http.StatusUnauthorized, duration.Milliseconds(), "", "unauthorized_no_verifier")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}

		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			duration := time.Since(start)
			a.metrics.RecordRequest(route.ID, r.Method, http.StatusUnauthorized, duration)
			telemetry.LogRequest(spanCtx, a.logger, route.ID, r.Method, r.URL.Path, http.StatusUnauthorized, duration.Milliseconds(), "", "unauthorized_missing_token")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}

		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
		claims, err := a.jwtVerifier.Verify(tokenStr)
		if err != nil {
			duration := time.Since(start)
			a.metrics.RecordRequest(route.ID, r.Method, http.StatusUnauthorized, duration)
			telemetry.LogRequest(spanCtx, a.logger, route.ID, r.Method, r.URL.Path, http.StatusUnauthorized, duration.Milliseconds(), "", "unauthorized_invalid_token")
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
			a.metrics.RecordRateLimitDecision(route.ID, allowed)

			if !allowed {
				duration := time.Since(start)
				a.metrics.RecordRequest(route.ID, r.Method, http.StatusTooManyRequests, duration)
				telemetry.LogRequest(spanCtx, a.logger, route.ID, r.Method, r.URL.Path, http.StatusTooManyRequests, duration.Milliseconds(), "", "rate_limited")

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
		duration := time.Since(start)
		a.metrics.RecordRequest(route.ID, r.Method, http.StatusServiceUnavailable, duration)
		telemetry.LogRequest(spanCtx, a.logger, route.ID, r.Method, r.URL.Path, http.StatusServiceUnavailable, duration.Milliseconds(), "", "no_backends")
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

	chosenBackend, releaseFunc, err := bal.Select(candidates, checker)
	if err != nil {
		duration := time.Since(start)
		a.metrics.RecordRequest(route.ID, r.Method, http.StatusServiceUnavailable, duration)
		telemetry.LogRequest(spanCtx, a.logger, route.ID, r.Method, r.URL.Path, http.StatusServiceUnavailable, duration.Milliseconds(), "", "no_eligible_backends")
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
		duration := time.Since(start)
		a.metrics.RecordRequest(route.ID, r.Method, http.StatusBadGateway, duration)
		telemetry.LogRequest(spanCtx, a.logger, route.ID, r.Method, r.URL.Path, http.StatusBadGateway, duration.Milliseconds(), chosenBackend.ID, "proxy_missing")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"backend proxy or breaker not configured"}`))
		return
	}

	permit, err := cb.TryAcquire()
	if err != nil {
		duration := time.Since(start)
		a.metrics.RecordRequest(route.ID, r.Method, http.StatusServiceUnavailable, duration)
		telemetry.LogRequest(spanCtx, a.logger, route.ID, r.Method, r.URL.Path, http.StatusServiceUnavailable, duration.Milliseconds(), chosenBackend.ID, "breaker_open")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"circuit breaker open"}`))
		return
	}

	obs := &BreakerPermitObserver{
		permit:     permit,
		classifier: cb.Classifier(),
		metrics:    a.metrics,
		routeID:    route.ID,
		backendID:  chosenBackend.ID,
	}

	ctx, cancel := context.WithTimeout(spanCtx, route.Timeout)
	defer cancel()

	observedCtx := proxy.WithObserver(ctx, obs)
	rw := &statusResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}

	ph.ServeHTTP(rw, r.WithContext(observedCtx))

	totalDuration := time.Since(start)
	a.metrics.RecordRequest(route.ID, r.Method, rw.statusCode, totalDuration)
	telemetry.LogRequest(spanCtx, a.logger, route.ID, r.Method, r.URL.Path, rw.statusCode, totalDuration.Milliseconds(), chosenBackend.ID, "")
}

// Start begins listening on the configured address, starts health checks and config watcher.
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

	if a.configPath != "" {
		w, err := config.NewWatcher(a.configPath, a.Reload, a.logger)
		if err == nil {
			a.watcher = w
			_ = a.watcher.Start(context.Background())
		}
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

// Metrics returns the Prometheus metrics instance.
func (a *App) Metrics() *telemetry.Metrics {
	return a.metrics
}

// Shutdown gracefully stops the server, watcher, health checks, tracer, and closes idle transport connections.
func (a *App) Shutdown(ctx context.Context) error {
	a.admin.SetReady(false)
	a.logger.Info("SentinelProxy shutting down...")

	if a.watcher != nil {
		a.watcher.Stop()
	}

	if a.healthChecker != nil {
		a.healthChecker.Stop()
	}

	if a.tracer != nil {
		_ = a.tracer.Shutdown(ctx)
	}

	err := a.server.Shutdown(ctx)
	a.transport.CloseIdleConnections()
	return err
}
