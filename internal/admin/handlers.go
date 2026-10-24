package admin

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sentinelproxy/sentinelproxy/internal/config"
)

// DiagnosticStatus provides current gateway status for /-/status.
type DiagnosticStatus struct {
	Status      string    `json:"status"`
	Version     string    `json:"version"`
	Fingerprint string    `json:"config_fingerprint"`
	LoadedAt    time.Time `json:"config_loaded_at"`
	RoutesCount int       `json:"routes_count"`
}

// Handler manages built-in administrative and diagnostic HTTP endpoints.
type Handler struct {
	sm      *config.SnapshotManager
	ready   atomic.Bool
	version string
}

// NewHandler creates a new admin Handler.
func NewHandler(sm *config.SnapshotManager, version string) *Handler {
	h := &Handler{
		sm:      sm,
		version: version,
	}
	if sm != nil && sm.Get() != nil {
		h.ready.Store(true)
	}
	return h
}

// SetReady marks the gateway as ready or unready.
func (h *Handler) SetReady(ready bool) {
	h.ready.Store(ready)
}

// RegisterRoutes attaches built-in reserved endpoints to an http.ServeMux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", h.handleHealthz)
	mux.HandleFunc("/readyz", h.handleReadyz)
	mux.HandleFunc("/-/status", h.handleStatus)
	mux.Handle("/metrics", promhttp.Handler())
}

func (h *Handler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (h *Handler) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.ready.Load() || h.sm == nil || h.sm.Get() == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"not ready"}`))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ready"}`))
}

func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	snap := h.sm.Get()
	status := DiagnosticStatus{
		Status:  "operational",
		Version: h.version,
	}
	if snap != nil {
		status.Fingerprint = snap.Fingerprint
		status.LoadedAt = snap.LoadedAt
		if snap.Config != nil {
			status.RoutesCount = len(snap.Config.Routes)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(status)
}
