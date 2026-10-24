package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sentinelproxy/sentinelproxy/internal/app"
	"github.com/sentinelproxy/sentinelproxy/internal/config"
	"github.com/sentinelproxy/sentinelproxy/internal/mock"
)

func TestHealthCheck_IntegrationSelection(t *testing.T) {
	// 1. Start two mock backend servers
	backendA := mock.NewServer("backend-A")
	serverA := httptest.NewServer(backendA.Handler())
	defer serverA.Close()

	backendB := mock.NewServer("backend-B")
	serverB := httptest.NewServer(backendB.Handler())
	defer serverB.Close()

	cfg := &config.Config{
		Server: config.ServerConfig{
			ListenAddr:        "127.0.0.1:0",
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
				ID:           "health-test-route",
				PathPrefix:   "/api/health-test",
				Methods:      []string{"GET"},
				LoadBalancer: "round_robin",
				Timeout:      2 * time.Second,
				HealthCheck: config.HealthCheckConfig{
					Path:               "/healthz",
					Interval:           50 * time.Millisecond,
					Timeout:            30 * time.Millisecond,
					HealthyStatusCodes: []int{200},
					UnhealthyThreshold: 1, // rapid response for test
					HealthyThreshold:   1,
				},
				Backends: []config.BackendConfig{
					{ID: "backend-A", URL: serverA.URL, Weight: 1},
					{ID: "backend-B", URL: serverB.URL, Weight: 1},
				},
			},
		},
	}

	application, err := app.New(cfg, "test_health.yaml")
	if err != nil {
		t.Fatalf("failed to create app: %v", err)
	}

	go func() {
		_ = application.Start()
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = application.Shutdown(shutdownCtx)
	}()

	time.Sleep(100 * time.Millisecond) // Allow listener and initial probe
	gatewayAddr := application.Addr()
	client := &http.Client{Timeout: 2 * time.Second}

	// 2. Initially both backends are healthy -> traffic alternates between A and B
	counts := make(map[string]int)
	for i := 0; i < 10; i++ {
		resp, err := client.Get("http://" + gatewayAddr + "/api/health-test")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		var echo map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&echo)
		_ = resp.Body.Close()
		counts[echo["backend_id"].(string)]++
	}

	if counts["backend-A"] == 0 || counts["backend-B"] == 0 {
		t.Errorf("expected both backends to receive traffic, got A=%d, B=%d", counts["backend-A"], counts["backend-B"])
	}

	// 3. Mark backend-A as unhealthy
	backendA.SetFaults(mock.FaultConfig{
		HealthHealthy: false,
		HealthStatus:  http.StatusServiceUnavailable,
	})

	// Wait for active health probe interval to detect failure
	time.Sleep(120 * time.Millisecond)

	// Now all requests must route exclusively to backend-B!
	for i := 0; i < 10; i++ {
		resp, err := client.Get("http://" + gatewayAddr + "/api/health-test")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		var echo map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&echo)
		_ = resp.Body.Close()
		if echo["backend_id"] != "backend-B" {
			t.Errorf("expected backend-B only, got %v", echo["backend_id"])
		}
	}

	// 4. Restore backend-A to healthy
	backendA.Reset()
	time.Sleep(120 * time.Millisecond)

	// Verify backend-A receives traffic again
	restoredA := false
	for i := 0; i < 10; i++ {
		resp, err := client.Get("http://" + gatewayAddr + "/api/health-test")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		var echo map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&echo)
		_ = resp.Body.Close()
		if echo["backend_id"] == "backend-A" {
			restoredA = true
			break
		}
	}

	if !restoredA {
		t.Errorf("expected backend-A to resume receiving traffic after health restored")
	}
}
