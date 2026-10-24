package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sentinelproxy/sentinelproxy/internal/app"
	"github.com/sentinelproxy/sentinelproxy/internal/config"
	"github.com/sentinelproxy/sentinelproxy/internal/mock"
)

func TestGatewayLifecycle_HealthAndProxying(t *testing.T) {
	// 1. Start mock backend
	mockServer := mock.NewServer("backend-life")
	backendHTTP := httptest.NewServer(mockServer.Handler())
	defer backendHTTP.Close()

	// 2. Build configuration pointing to the mock backend
	cfg := &config.Config{
		Server: config.ServerConfig{
			ListenAddr:        "127.0.0.1:0", // Dynamic port
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
				ID:           "test-life",
				PathPrefix:   "/api/v1/echo",
				Methods:      []string{"GET", "POST"},
				LoadBalancer: "round_robin",
				Timeout:      3 * time.Second,
				Backends: []config.BackendConfig{
					{
						ID:     "backend-life",
						URL:    backendHTTP.URL,
						Weight: 1,
					},
				},
			},
		},
	}

	application, err := app.New(cfg, "test_config.yaml")
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

	// Wait briefly for server listener to be ready
	time.Sleep(50 * time.Millisecond)
	gatewayAddr := application.Addr()

	client := &http.Client{Timeout: 3 * time.Second}

	// 3. Test /healthz
	resp, err := client.Get("http://" + gatewayAddr + "/healthz")
	if err != nil {
		t.Fatalf("failed to query /healthz: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 from /healthz, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != `{"status":"ok"}` {
		t.Errorf("unexpected /healthz body: %s", string(body))
	}

	// 4. Test /readyz
	resp, err = client.Get("http://" + gatewayAddr + "/readyz")
	if err != nil {
		t.Fatalf("failed to query /readyz: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 from /readyz, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 5. Test /-/status
	resp, err = client.Get("http://" + gatewayAddr + "/-/status")
	if err != nil {
		t.Fatalf("failed to query /-/status: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 from /-/status, got %d", resp.StatusCode)
	}
	var diag map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&diag)
	_ = resp.Body.Close()
	if diag["status"] != "operational" {
		t.Errorf("expected status 'operational', got %v", diag["status"])
	}

	// 6. Test proxied request to mock backend
	resp, err = client.Get("http://" + gatewayAddr + "/api/v1/echo?test=true")
	if err != nil {
		t.Fatalf("failed to proxy request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 from proxy, got %d", resp.StatusCode)
	}
	var echoResp map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&echoResp)
	_ = resp.Body.Close()

	if echoResp["backend_id"] != "backend-life" {
		t.Errorf("expected backend_id backend-life, got %v", echoResp["backend_id"])
	}
	if echoResp["query"] != "test=true" {
		t.Errorf("expected query test=true, got %v", echoResp["query"])
	}

	// 7. Test 404 for unknown route
	resp, err = client.Get("http://" + gatewayAddr + "/unknown/path")
	if err != nil {
		t.Fatalf("failed to query unknown path: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for unknown route, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 8. Test 405 for unsupported method
	resp, err = client.Post("http://"+gatewayAddr+"/api/v1/echo", "application/json", nil)
	if err != nil {
		t.Fatalf("failed to test POST: %v", err)
	}
	// Note: POST is allowed in route, so this returns 200
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for allowed POST, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Try DELETE (not allowed)
	delReq, _ := http.NewRequest(http.MethodDelete, "http://"+gatewayAddr+"/api/v1/echo", nil)
	resp, err = client.Do(delReq)
	if err != nil {
		t.Fatalf("failed to test DELETE: %v", err)
	}
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for DELETE, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Allow") == "" {
		t.Errorf("expected Allow header on 405 response")
	}
	_ = resp.Body.Close()
}
