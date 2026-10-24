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

	"github.com/sentinelproxy/sentinelproxy/internal/admin"
	"github.com/sentinelproxy/sentinelproxy/internal/config"
	"github.com/sentinelproxy/sentinelproxy/internal/proxy"
)

// App manages the lifecycle of the SentinelProxy gateway.
type App struct {
	configPath string
	sm         *config.SnapshotManager
	admin      *admin.Handler
	server     *http.Server
	transport  *http.Transport
	proxies    map[string]*proxy.ProxyHandler // key: backend.ID
	logger     *slog.Logger
	listener   net.Listener
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

	// Pre-build proxy handlers for all configured backends
	proxies := make(map[string]*proxy.ProxyHandler)
	for _, r := range cfg.Routes {
		for _, b := range r.Backends {
			targetURL, err := url.Parse(b.URL)
			if err != nil {
				return nil, fmt.Errorf("invalid backend url %q: %w", b.URL, err)
			}
			proxies[b.ID] = proxy.NewProxyHandler(targetURL, transport)
		}
	}

	app := &App{
		configPath: configPath,
		sm:         sm,
		admin:      adminHandler,
		transport:  transport,
		proxies:    proxies,
		logger:     logger,
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
	snap := a.sm.Get()
	if snap == nil {
		http.Error(w, `{"error":"gateway not ready"}`, http.StatusServiceUnavailable)
		return
	}

	// Longest prefix match on sorted routes
	for _, route := range snap.SortedRoutes {
		if strings.HasPrefix(r.URL.Path, route.PathPrefix) {
			// Check method
			methodAllowed := false
			for _, m := range route.Methods {
				if r.Method == m {
					methodAllowed = true
					break
				}
			}
			if !methodAllowed {
				w.Header().Set("Allow", strings.Join(route.Methods, ", "))
				http.Error(w, `{"error":"Method Not Allowed"}`, http.StatusMethodNotAllowed)
				return
			}

			// For baseline proxying (Phase 2), pick first available backend
			if len(route.Backends) == 0 {
				http.Error(w, `{"error":"no backends available"}`, http.StatusServiceUnavailable)
				return
			}

			backendID := route.Backends[0].ID
			ph, ok := a.proxies[backendID]
			if !ok {
				http.Error(w, `{"error":"backend proxy not found"}`, http.StatusBadGateway)
				return
			}

			// Apply route timeout to request context
			ctx, cancel := context.WithTimeout(r.Context(), route.Timeout)
			defer cancel()

			ph.ServeHTTP(w, r.WithContext(ctx))
			return
		}
	}

	// No route matched
	http.Error(w, `{"error":"Not Found"}`, http.StatusNotFound)
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

// Addr returns the active listener address (useful when dynamic port 0 is used in tests).
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
