package mock_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sentinelproxy/sentinelproxy/internal/mock"
)

func TestMockBackend_EchoAndDefaults(t *testing.T) {
	s := mock.NewServer("backend-test-1")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	// Test GET echo
	resp, err := http.Get(ts.URL + "/test/path?foo=bar")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Backend-ID") != "backend-test-1" {
		t.Errorf("expected X-Backend-ID header backend-test-1, got %s", resp.Header.Get("X-Backend-ID"))
	}

	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if data["path"] != "/test/path" {
		t.Errorf("expected path /test/path, got %v", data["path"])
	}
	if data["query"] != "foo=bar" {
		t.Errorf("expected query foo=bar, got %v", data["query"])
	}
}

func TestMockBackend_HealthCheckAndFaults(t *testing.T) {
	s := mock.NewServer("backend-health")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	// Initial health: 200 OK
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Update faults via POST /-/faults
	faultReq := mock.FaultConfig{
		HealthHealthy: false,
		HealthStatus:  http.StatusServiceUnavailable,
		FailureRate:   1.0,
		FailureStatus: http.StatusInternalServerError,
		DelayMs:       10,
		Seed:          42,
	}
	body, _ := json.Marshal(faultReq)
	fResp, err := http.Post(ts.URL+"/-/faults", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error setting faults: %v", err)
	}
	fResp.Body.Close()
	if fResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 from faults update, got %d", fResp.StatusCode)
	}

	// Now /healthz should return 503
	resp2, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 for healthz after fault, got %d", resp2.StatusCode)
	}

	// Regular traffic should now fail with 500
	reqStart := time.Now()
	resp3, err := http.Get(ts.URL + "/api/data")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500 for traffic after fault, got %d", resp3.StatusCode)
	}
	if time.Since(reqStart) < 9*time.Millisecond {
		t.Errorf("expected delay to take at least ~10ms, took %v", time.Since(reqStart))
	}

	// Reset faults via POST /-/reset
	rResp, err := http.Post(ts.URL+"/-/reset", "application/json", nil)
	if err != nil {
		t.Fatalf("unexpected error calling reset: %v", err)
	}
	rResp.Body.Close()
	if rResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 from reset, got %d", rResp.StatusCode)
	}

	// Now /healthz should be 200 again
	resp4, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp4.Body.Close()
	if resp4.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for healthz after reset, got %d", resp4.StatusCode)
	}
}

func TestMockBackend_QueryOverridesAndStreaming(t *testing.T) {
	s := mock.NewServer("backend-query")
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	// Query override status=503
	resp, err := http.Get(ts.URL + "/api/override?status=503")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 from query override, got %d", resp.StatusCode)
	}

	// Query override delay
	start := time.Now()
	resp2, err := http.Get(ts.URL + "/api/delay?delay_ms=50")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp2.Body.Close()
	if time.Since(start) < 45*time.Millisecond {
		t.Errorf("expected delay_ms query override to delay response, took %v", time.Since(start))
	}

	// Stream endpoint
	sResp, err := http.Get(ts.URL + "/stream?chunks=3")
	if err != nil {
		t.Fatalf("unexpected error streaming: %v", err)
	}
	defer sResp.Body.Close()
	streamBytes, err := io.ReadAll(sResp.Body)
	if err != nil {
		t.Fatalf("error reading stream body: %v", err)
	}
	expectedOutput := "chunk-0\nchunk-1\nchunk-2\n"
	if string(streamBytes) != expectedOutput {
		t.Errorf("expected %q, got %q", expectedOutput, string(streamBytes))
	}
}
