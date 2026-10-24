package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sentinelproxy/sentinelproxy/internal/app"
	"github.com/sentinelproxy/sentinelproxy/internal/config"
	"github.com/sentinelproxy/sentinelproxy/internal/mock"
)

func TestTracingAndMetrics_EndToEnd(t *testing.T) {
	backend := mock.NewServer("backend-trace")
	backendServer := httptest.NewServer(backend.Handler())
	defer backendServer.Close()

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
			Tracing: config.TracingConfig{
				Enabled:       true,
				ServiceName:   "sentinelproxy-test",
				SamplingRatio: 1.0,
			},
		},
		Security: config.SecurityConfig{
			RequestIDHeader: "X-Request-ID",
		},
		Routes: []config.RouteConfig{
			{
				ID:           "trace-route",
				PathPrefix:   "/api/trace",
				Methods:      []string{"GET"},
				LoadBalancer: "round_robin",
				Timeout:      2 * time.Second,
				Backends: []config.BackendConfig{
					{ID: "backend-trace", URL: backendServer.URL, Weight: 1},
				},
			},
		},
	}

	application, err := app.New(cfg, "test_trace.yaml")
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

	time.Sleep(50 * time.Millisecond)
	gatewayAddr := application.Addr()
	client := &http.Client{Timeout: 2 * time.Second}

	// 1. Send request with incoming traceparent
	const incomingTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	req, _ := http.NewRequest("GET", "http://"+gatewayAddr+"/api/trace", nil)
	req.Header.Set("traceparent", incomingTraceparent)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var echo map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&echo)
	_ = resp.Body.Close()

	// Verify the mock backend received the forwarded W3C traceparent with the same trace ID!
	headers := echo["headers"].(map[string]any)
	traceparentHeaders := headers["Traceparent"]
	if traceparentHeaders == nil {
		t.Fatalf("expected Traceparent header in backend echo, got nil")
	}
	tpSlice := traceparentHeaders.([]any)
	if len(tpSlice) == 0 {
		t.Fatalf("Traceparent header slice empty")
	}
	forwardedTraceparent := tpSlice[0].(string)
	if !strings.Contains(forwardedTraceparent, "4bf92f3577b34da6a3ce929d0e0e4736") {
		t.Errorf("expected trace ID 4bf92f3577b34da6a3ce929d0e0e4736 in forwarded traceparent, got %q", forwardedTraceparent)
	}

	// 2. Query /metrics to verify Prometheus metrics are populated
	metricsResp, err := client.Get("http://" + gatewayAddr + "/metrics")
	if err != nil {
		t.Fatalf("failed to query /metrics: %v", err)
	}
	if metricsResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 from /metrics, got %d", metricsResp.StatusCode)
	}
	metricsBody, _ := io.ReadAll(metricsResp.Body)
	_ = metricsResp.Body.Close()

	metricsText := string(metricsBody)
	if !strings.Contains(metricsText, "sentinelproxy_requests_total") {
		t.Errorf("expected sentinelproxy_requests_total metric in output")
	}
	if !strings.Contains(metricsText, `route_id="trace-route"`) {
		t.Errorf("expected route_id=\"trace-route\" label in metrics output")
	}
	if !strings.Contains(metricsText, "sentinelproxy_upstream_requests_total") {
		t.Errorf("expected sentinelproxy_upstream_requests_total in output")
	}
}
