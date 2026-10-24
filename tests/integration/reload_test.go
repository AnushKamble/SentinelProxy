package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sentinelproxy/sentinelproxy/internal/app"
	"github.com/sentinelproxy/sentinelproxy/internal/config"
	"github.com/sentinelproxy/sentinelproxy/internal/mock"
)

const initialConfigTemplate = `server:
  listen_addr: "127.0.0.1:0"
  read_header_timeout: 5s
  idle_timeout: 30s
  shutdown_timeout: 5s

observability:
  log_level: info
  log_format: json
  metrics_path: /metrics

security:
  request_id_header: X-Request-ID

routes:
  - id: route-alpha
    path_prefix: /api/alpha
    methods: [GET]
    load_balancer: round_robin
    timeout: 2s
    backends:
      - id: backend-alpha
        url: %s
        weight: 1
`

const updatedValidConfigTemplate = `server:
  listen_addr: "127.0.0.1:0"
  read_header_timeout: 5s
  idle_timeout: 30s
  shutdown_timeout: 5s

observability:
  log_level: info
  log_format: json
  metrics_path: /metrics

security:
  request_id_header: X-Request-ID

routes:
  - id: route-alpha
    path_prefix: /api/alpha
    methods: [GET]
    load_balancer: round_robin
    timeout: 2s
    backends:
      - id: backend-alpha
        url: %s
        weight: 1

  - id: route-beta
    path_prefix: /api/beta
    methods: [GET]
    load_balancer: round_robin
    timeout: 2s
    backends:
      - id: backend-beta
        url: %s
        weight: 1
`

const invalidConfigTemplate = `server:
  listen_addr: "127.0.0.1:0"
  read_header_timeout: 5s
  idle_timeout: 30s
  shutdown_timeout: 5s

observability:
  log_level: info
  log_format: json
  metrics_path: /metrics

security:
  request_id_header: X-Request-ID

routes:
  - id: route-broken
    path_prefix: /api/alpha
    methods: [INVALID_METHOD]
    load_balancer: unknown_lb
    timeout: -1s
    backends: []
`

func TestHotReload_ValidChangeAndInvalidFallback(t *testing.T) {
	// Start mock backends
	backendAlpha := mock.NewServer("backend-alpha")
	serverAlpha := httptest.NewServer(backendAlpha.Handler())
	defer serverAlpha.Close()

	backendBeta := mock.NewServer("backend-beta")
	serverBeta := httptest.NewServer(backendBeta.Handler())
	defer serverBeta.Close()

	// Write initial config to temp file
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "routes.yaml")

	initialYAML := []byte(strings.ReplaceAll(
		strings.ReplaceAll(initialConfigTemplate, "%s", serverAlpha.URL),
		"\r\n", "\n",
	))
	if err := os.WriteFile(configPath, initialYAML, 0644); err != nil {
		t.Fatalf("failed to write initial config: %v", err)
	}

	cfg, err := config.LoadFromFile(configPath)
	if err != nil {
		t.Fatalf("failed to load initial config: %v", err)
	}

	application, err := app.New(cfg, configPath)
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

	// 1. Initial route-alpha works
	resp, err := client.Get("http://" + gatewayAddr + "/api/alpha")
	if err != nil {
		t.Fatalf("request to alpha failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for alpha, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Route-beta does not exist yet -> 404
	resp, err = client.Get("http://" + gatewayAddr + "/api/beta")
	if err != nil {
		t.Fatalf("request to beta failed: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for unconfigured beta route, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 2. Perform valid reload adding route-beta
	validNewYAML := []byte(strings.Replace(
		strings.Replace(updatedValidConfigTemplate, "%s", serverAlpha.URL, 1),
		"%s", serverBeta.URL, 1,
	))
	if err := os.WriteFile(configPath, validNewYAML, 0644); err != nil {
		t.Fatalf("failed to update config file: %v", err)
	}

	// Trigger reload
	if err := application.Reload(); err != nil {
		t.Fatalf("expected valid reload to succeed, got: %v", err)
	}

	// Now route-beta works!
	resp, err = client.Get("http://" + gatewayAddr + "/api/beta")
	if err != nil {
		t.Fatalf("request to beta after reload failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for newly added beta route, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Route-alpha still works!
	resp, err = client.Get("http://" + gatewayAddr + "/api/alpha")
	if err != nil {
		t.Fatalf("request to alpha after reload failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for existing alpha route, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 3. Perform invalid reload (bad syntax / invalid fields)
	if err := os.WriteFile(configPath, []byte(invalidConfigTemplate), 0644); err != nil {
		t.Fatalf("failed to write invalid config file: %v", err)
	}

	err = application.Reload()
	if err == nil {
		t.Errorf("expected invalid reload to be rejected, got nil")
	}

	// Last-known-good retention: both alpha and beta must continue to work without disruption!
	resp, err = client.Get("http://" + gatewayAddr + "/api/alpha")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("expected alpha to remain operational after rejected reload")
	}
	_ = resp.Body.Close()

	resp, err = client.Get("http://" + gatewayAddr + "/api/beta")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("expected beta to remain operational after rejected reload")
	}
	_ = resp.Body.Close()
}

func TestHotReload_ConcurrentTrafficDuringReload(t *testing.T) {
	backendA := mock.NewServer("backend-A")
	serverA := httptest.NewServer(backendA.Handler())
	defer serverA.Close()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "routes.yaml")

	cfgYAML := []byte(strings.ReplaceAll(initialConfigTemplate, "%s", serverA.URL))
	_ = os.WriteFile(configPath, cfgYAML, 0644)

	cfg, _ := config.LoadFromFile(configPath)
	application, _ := app.New(cfg, configPath)

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

	// Run concurrent requests while repeatedly triggering reloads
	var wg sync.WaitGroup
	const clients = 20
	const reqsPerClient = 50

	stopTraffic := make(chan struct{})

	// Traffic goroutines
	for c := 0; c < clients; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			httpClient := &http.Client{Timeout: 2 * time.Second}
			for i := 0; i < reqsPerClient; i++ {
				select {
				case <-stopTraffic:
					return
				default:
					resp, err := httpClient.Get("http://" + gatewayAddr + "/api/alpha")
					if err == nil {
						if resp.StatusCode != http.StatusOK {
							t.Errorf("unexpected status code during reload: %d", resp.StatusCode)
						}
						_ = resp.Body.Close()
					}
				}
			}
		}()
	}

	// Reload goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			time.Sleep(15 * time.Millisecond)
			_ = application.Reload()
		}
	}()

	wg.Wait()
	close(stopTraffic)
}
