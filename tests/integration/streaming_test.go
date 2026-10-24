package integration_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sentinelproxy/sentinelproxy/internal/app"
	"github.com/sentinelproxy/sentinelproxy/internal/config"
	"github.com/sentinelproxy/sentinelproxy/internal/mock"
)

func TestStreaming_LargePayloadAndCancellation(t *testing.T) {
	mockServer := mock.NewServer("backend-stream")
	backendHTTP := httptest.NewServer(mockServer.Handler())
	defer backendHTTP.Close()

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
				ID:           "test-stream",
				PathPrefix:   "/stream",
				Methods:      []string{"GET", "POST"},
				LoadBalancer: "round_robin",
				Timeout:      10 * time.Second,
				Backends: []config.BackendConfig{
					{
						ID:     "backend-stream",
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

	time.Sleep(50 * time.Millisecond)
	gatewayAddr := application.Addr()

	client := &http.Client{Timeout: 10 * time.Second}

	// 1. Test streaming response chunks
	resp, err := client.Get("http://" + gatewayAddr + "/stream?chunks=5")
	if err != nil {
		t.Fatalf("failed to query streaming endpoint: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("error reading streamed body: %v", err)
	}
	expectedContent := "chunk-0\nchunk-1\nchunk-2\nchunk-3\nchunk-4\n"
	if string(body) != expectedContent {
		t.Errorf("expected body %q, got %q", expectedContent, string(body))
	}

	// 2. Test large request body forwarding (e.g. 2MB)
	largeData := bytes.Repeat([]byte("ABCDEF1234567890"), 128*1024) // 2MB
	postResp, err := client.Post("http://"+gatewayAddr+"/stream/large", "application/octet-stream", bytes.NewReader(largeData))
	if err != nil {
		t.Fatalf("failed to post large body: %v", err)
	}
	if postResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for large body POST, got %d", postResp.StatusCode)
	}
	_ = postResp.Body.Close()

	// 3. Test client context cancellation mid-request
	cancelCtx, cancelFunc := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(cancelCtx, http.MethodGet, "http://"+gatewayAddr+"/stream?chunks=50", nil)

	// Cancel context after 30ms while streaming
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancelFunc()
	}()

	resp, err = client.Do(req)
	var streamErr error
	if err == nil {
		defer resp.Body.Close()
		_, streamErr = io.ReadAll(resp.Body)
	} else {
		streamErr = err
	}

	if streamErr == nil {
		t.Errorf("expected error during streamed body read due to canceled context, got nil")
	}
}
