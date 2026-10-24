package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sentinelproxy/sentinelproxy/internal/app"
	"github.com/sentinelproxy/sentinelproxy/internal/config"
	"github.com/sentinelproxy/sentinelproxy/internal/mock"
)

func TestRateLimit_IntegrationAndBreakerIsolation(t *testing.T) {
	backend := mock.NewServer("backend-rl")
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
		},
		Security: config.SecurityConfig{
			RequestIDHeader:   "X-Request-ID",
			TrustedProxyCIDRs: []string{"127.0.0.1/32"},
		},
		Routes: []config.RouteConfig{
			{
				ID:           "rate-limited-route",
				PathPrefix:   "/api/limited",
				Methods:      []string{"GET"},
				LoadBalancer: "round_robin",
				Timeout:      2 * time.Second,
				RateLimit: config.RateLimitConfig{
					Enabled:           true,
					RequestsPerSecond: 1, // 1 token per second
					Burst:             3, // Burst capacity 3
				},
				CircuitBreaker: config.CircuitBreakerConfig{
					Enabled:                  true,
					Window:                   10 * time.Second,
					BucketCount:              10,
					MinimumRequests:          3,
					FailureRateThreshold:     0.5,
					OpenDuration:             5 * time.Second,
					HalfOpenMaxProbes:        1,
					HalfOpenSuccessesToClose: 1,
					CountedStatusCodes:       []int{500, 502, 503, 504},
				},
				Backends: []config.BackendConfig{
					{ID: "backend-rl", URL: backendServer.URL, Weight: 1},
				},
			},
		},
	}

	application, err := app.New(cfg, "test_rl.yaml")
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

	// 1. Send burst of 3 requests -> must all succeed (200 OK)
	for i := 0; i < 3; i++ {
		resp, err := client.Get("http://" + gatewayAddr + "/api/limited")
		if err != nil {
			t.Fatalf("request %d failed: %v", i+1, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("request %d expected status 200, got %d", i+1, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}

	// 2. 4th, 5th, 6th requests must receive 429 Too Many Requests
	for i := 0; i < 3; i++ {
		resp, err := client.Get("http://" + gatewayAddr + "/api/limited")
		if err != nil {
			t.Fatalf("request %d failed: %v", i+4, err)
		}
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Errorf("request %d expected status 429, got %d", i+4, resp.StatusCode)
		}
		retryAfter := resp.Header.Get("Retry-After")
		if retryAfter == "" {
			t.Errorf("request %d expected non-empty Retry-After header", i+4)
		}
		_ = resp.Body.Close()
	}

	// 3. Rate-limit 429s must NOT trip the circuit breaker!
	// Wait 1.1s for a token to refill
	time.Sleep(1100 * time.Millisecond)

	resp, err := client.Get("http://" + gatewayAddr + "/api/limited")
	if err != nil {
		t.Fatalf("request after refill failed: %v", err)
	}
	// If breaker had tripped, this would return 503 circuit breaker open.
	// Since 429s did not trip breaker, it forwards to backend and returns 200!
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK after rate-limit refill (breaker must remain closed), got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}
