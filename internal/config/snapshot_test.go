package config_test

import (
	"testing"
	"time"

	"github.com/sentinelproxy/sentinelproxy/internal/config"
)

func TestRuntimeSnapshot_CreationAndLongestPrefixOrder(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{
			ListenAddr:        "127.0.0.1:8080",
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       30 * time.Second,
			ShutdownTimeout:   5 * time.Second,
		},
		Observability: config.ObservabilityConfig{
			LogLevel:    "info",
			LogFormat:   "json",
			MetricsPath: "/metrics",
		},
		Security: config.SecurityConfig{
			RequestIDHeader: "X-Request-ID",
		},
		Routes: []config.RouteConfig{
			{
				ID:           "short",
				PathPrefix:   "/api",
				Methods:      []string{"GET"},
				LoadBalancer: "round_robin",
				Timeout:      1 * time.Second,
				Backends: []config.BackendConfig{
					{ID: "b1", URL: "http://127.0.0.1:9001", Weight: 1},
				},
			},
			{
				ID:           "longest",
				PathPrefix:   "/api/v1/users/profile",
				Methods:      []string{"GET"},
				LoadBalancer: "round_robin",
				Timeout:      1 * time.Second,
				Backends: []config.BackendConfig{
					{ID: "b2", URL: "http://127.0.0.1:9002", Weight: 1},
				},
			},
			{
				ID:           "medium",
				PathPrefix:   "/api/v1",
				Methods:      []string{"GET"},
				LoadBalancer: "round_robin",
				Timeout:      1 * time.Second,
				Backends: []config.BackendConfig{
					{ID: "b3", URL: "http://127.0.0.1:9003", Weight: 1},
				},
			},
		},
	}

	snap, err := config.NewRuntimeSnapshot(cfg)
	if err != nil {
		t.Fatalf("failed to create runtime snapshot: %v", err)
	}

	if snap.Fingerprint == "" {
		t.Errorf("expected non-empty fingerprint")
	}

	// Verify longest-prefix ordering
	expectedOrder := []string{"longest", "medium", "short"}
	for i, r := range snap.SortedRoutes {
		if r.ID != expectedOrder[i] {
			t.Errorf("expected sorted route[%d] to be %s, got %s", i, expectedOrder[i], r.ID)
		}
	}

	// Verify RouteByID lookup
	r, found := snap.RouteByID("medium")
	if !found || r.ID != "medium" {
		t.Errorf("failed to lookup route 'medium' by ID")
	}

	_, notFound := snap.RouteByID("non-existent")
	if notFound {
		t.Errorf("expected notFound for non-existent route")
	}
}

func TestSnapshotManager_AtomicSwap(t *testing.T) {
	snap1 := &config.RuntimeSnapshot{Fingerprint: "v1"}
	sm := config.NewSnapshotManager(snap1)

	if sm.Get().Fingerprint != "v1" {
		t.Errorf("expected v1, got %s", sm.Get().Fingerprint)
	}

	snap2 := &config.RuntimeSnapshot{Fingerprint: "v2"}
	old := sm.Swap(snap2)
	if old.Fingerprint != "v1" {
		t.Errorf("expected old to be v1, got %s", old.Fingerprint)
	}
	if sm.Get().Fingerprint != "v2" {
		t.Errorf("expected current to be v2, got %s", sm.Get().Fingerprint)
	}
}
