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
	balancers     map[string]balancer.Balancer   // key: route.ID
	routeBackends map[string][]*balancer.Backend // key: route.ID
	proxies       map[string]*proxy.ProxyHandler // key: backend.ID
	logger        *slog.Logger
	listener      net.Listener
	mu            sync.RWMutex
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
		proxies:       proxies,
		logger:        logger,
	}

	mux := http.NewServeMux()
	adminHandler.RegisterRoutes(mux)

	// Fallback/routing handler for gateway traffic
	mux.HandleFunc("/", app.serveHTTP)

	app.server = &http.Server{
		Addr:              cfg.Server.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
	}

	return app, nil
}

// ServeHTTP routes incoming requests to admin or matched backend proxy.
func (a *App) serveHTTP(w http.ResponseWriter, r *http.Request) {
	match := a.router.Match(r.URL.Path, r.Method)
	if match.IsReserved {
		// Handled directly by ServeMux
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

	// Select candidate backend through load balancer
	chosenBackend, releaseFunc, err := bal.Select(candidates, nil)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"no eligible backend candidate available"}`))
		return
	}
	defer releaseFunc()

	ph, ok := a.proxies[chosenBackend.ID]
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"backend proxy not found"}`))
		return
	}

	// Apply route timeout to request context
	ctx, cancel := context.WithTimeout(r.Context(), route.Timeout)
	defer cancel()

	ph.ServeHTTP(w, r.WithContext(ctx))
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
