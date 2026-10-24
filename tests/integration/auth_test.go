package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/sentinelproxy/sentinelproxy/internal/app"
	"github.com/sentinelproxy/sentinelproxy/internal/auth"
	"github.com/sentinelproxy/sentinelproxy/internal/config"
	"github.com/sentinelproxy/sentinelproxy/internal/mock"
)

func TestAuth_IntegrationAndSubjectRateLimiting(t *testing.T) {
	testSecret := []byte("my-test-jwt-secret-key-32-bytes-minimum")
	_ = os.Setenv("SENTINELPROXY_TEST_JWT_SECRET", string(testSecret))
	defer func() { _ = os.Unsetenv("SENTINELPROXY_TEST_JWT_SECRET") }()

	backend := mock.NewServer("backend-auth")
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
			JWT: config.JWTConfig{
				Enabled:   true,
				Algorithm: "HS256",
				Issuer:    "sentinel-test-issuer",
				Audience:  "sentinel-test-audience",
				SecretEnv: "SENTINELPROXY_TEST_JWT_SECRET",
			},
		},
		Routes: []config.RouteConfig{
			{
				ID:           "auth-protected-route",
				PathPrefix:   "/api/protected",
				Methods:      []string{"GET"},
				AuthRequired: true,
				LoadBalancer: "round_robin",
				Timeout:      2 * time.Second,
				RateLimit: config.RateLimitConfig{
					Enabled:           true,
					RequestsPerSecond: 1,
					Burst:             2, // 2 requests per subject
				},
				Backends: []config.BackendConfig{
					{ID: "backend-auth", URL: backendServer.URL, Weight: 1},
				},
			},
		},
	}

	application, err := app.New(cfg, "test_auth.yaml")
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

	// 1. Request with no token -> 401 Unauthorized
	resp, err := client.Get("http://" + gatewayAddr + "/api/protected")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 without token, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 2. Request with invalid token -> 401 Unauthorized
	reqInvalid, _ := http.NewRequest("GET", "http://"+gatewayAddr+"/api/protected", nil)
	reqInvalid.Header.Set("Authorization", "Bearer invalid-token-string")
	resp, err = client.Do(reqInvalid)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid token, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 3. Request with valid token for Alice
	tokenAlice, err := auth.SignTestToken(testSecret, "alice", "sentinel-test-issuer", "sentinel-test-audience", 5*time.Minute, time.Time{})
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}

	for i := 0; i < 2; i++ {
		reqAlice, _ := http.NewRequest("GET", "http://"+gatewayAddr+"/api/protected", nil)
		reqAlice.Header.Set("Authorization", "Bearer "+tokenAlice)
		resp, err = client.Do(reqAlice)
		if err != nil {
			t.Fatalf("request for Alice failed: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 for Alice burst %d, got %d", i+1, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}

	// 4. Alice exceeds burst of 2 -> receives 429
	reqAlice3, _ := http.NewRequest("GET", "http://"+gatewayAddr+"/api/protected", nil)
	reqAlice3.Header.Set("Authorization", "Bearer "+tokenAlice)
	resp, err = client.Do(reqAlice3)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected 429 for Alice 3rd request, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 5. Subject isolation: Bob with valid token can still make requests even though Alice is rate limited!
	tokenBob, _ := auth.SignTestToken(testSecret, "bob", "sentinel-test-issuer", "sentinel-test-audience", 5*time.Minute, time.Time{})
	reqBob, _ := http.NewRequest("GET", "http://"+gatewayAddr+"/api/protected", nil)
	reqBob.Header.Set("Authorization", "Bearer "+tokenBob)
	resp, err = client.Do(reqBob)
	if err != nil {
		t.Fatalf("request for Bob failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for Bob despite Alice rate-limited, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}
