package health_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sentinelproxy/sentinelproxy/internal/config"
	"github.com/sentinelproxy/sentinelproxy/internal/health"
)

func TestRegistry_HysteresisTransitions(t *testing.T) {
	reg := health.NewRegistry()
	reg.Register("r1", "b1")

	if !reg.IsHealthy("r1", "b1") {
		t.Fatalf("target should initially be healthy")
	}

	const healthyThreshold = 2
	const unhealthyThreshold = 3

	// 1st failure: consecutive=1, should stay healthy
	trans := reg.RecordProbeResult("r1", "b1", false, 503, nil, healthyThreshold, unhealthyThreshold)
	if trans || !reg.IsHealthy("r1", "b1") {
		t.Errorf("1st failure should not transition, expected healthy=true")
	}

	// 2nd failure: consecutive=2, should stay healthy
	trans = reg.RecordProbeResult("r1", "b1", false, 503, nil, healthyThreshold, unhealthyThreshold)
	if trans || !reg.IsHealthy("r1", "b1") {
		t.Errorf("2nd failure should not transition, expected healthy=true")
	}

	// 3rd failure: consecutive=3 == unhealthyThreshold -> transitions to UNHEALTHY
	trans = reg.RecordProbeResult("r1", "b1", false, 503, nil, healthyThreshold, unhealthyThreshold)
	if !trans || reg.IsHealthy("r1", "b1") {
		t.Errorf("3rd failure must transition to unhealthy=false")
	}

	// 1st recovery success: consecutive=1 (< healthyThreshold 2) -> stays unhealthy
	trans = reg.RecordProbeResult("r1", "b1", true, 200, nil, healthyThreshold, unhealthyThreshold)
	if trans || reg.IsHealthy("r1", "b1") {
		t.Errorf("1st recovery success should not transition, expected healthy=false")
	}

	// 2nd recovery success: consecutive=2 == healthyThreshold -> transitions to HEALTHY
	trans = reg.RecordProbeResult("r1", "b1", true, 200, nil, healthyThreshold, unhealthyThreshold)
	if !trans || !reg.IsHealthy("r1", "b1") {
		t.Errorf("2nd recovery success must transition to healthy=true")
	}
}

func TestChecker_ProbeOnce(t *testing.T) {
	var statusCode atomic.Int32
	statusCode.Store(http.StatusOK)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(statusCode.Load()))
	}))
	defer ts.Close()

	reg := health.NewRegistry()
	target := health.ProbeTarget{
		RouteID:   "r1",
		BackendID: "b1",
		ProbeURL:  ts.URL + "/healthz",
		Config: config.HealthCheckConfig{
			Path:               "/healthz",
			Interval:           1 * time.Second,
			Timeout:            500 * time.Millisecond,
			HealthyStatusCodes: []int{200},
			UnhealthyThreshold: 1,
			HealthyThreshold:   1,
		},
	}

	checker := health.NewChecker(reg, []health.ProbeTarget{target}, nil)

	// Probe with 200 OK -> healthy
	healthy, _ := checker.ProbeOnce(context.Background(), target)
	if !healthy {
		t.Errorf("expected target to be healthy after 200 OK")
	}

	// Switch mock server to 503
	statusCode.Store(http.StatusServiceUnavailable)
	healthy, transitioned := checker.ProbeOnce(context.Background(), target)
	if healthy {
		t.Errorf("expected target to be unhealthy after 503")
	}
	if !transitioned {
		t.Errorf("expected transition to occurred on status change")
	}
}

func TestChecker_NonOverlappingProbes(t *testing.T) {
	hangChan := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-hangChan
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	reg := health.NewRegistry()
	target := health.ProbeTarget{
		RouteID:   "r1",
		BackendID: "b1",
		ProbeURL:  ts.URL + "/healthz",
		Config: config.HealthCheckConfig{
			Path:               "/healthz",
			Interval:           1 * time.Second,
			Timeout:            2 * time.Second,
			HealthyStatusCodes: []int{200},
			UnhealthyThreshold: 1,
			HealthyThreshold:   1,
		},
	}

	checker := health.NewChecker(reg, []health.ProbeTarget{target}, nil)

	// Launch probe 1 in background (will hang on hangChan)
	done1 := make(chan struct{})
	go func() {
		_, _ = checker.ProbeOnce(context.Background(), target)
		close(done1)
	}()

	time.Sleep(20 * time.Millisecond)

	// Launch probe 2 while probe 1 is in-flight: should return without duplicate probe
	_, transitioned := checker.ProbeOnce(context.Background(), target)
	if transitioned {
		t.Errorf("overlapping probe should not transition")
	}

	// Unblock probe 1
	close(hangChan)
	<-done1
}

func TestChecker_StartAndStopLifecycle(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	reg := health.NewRegistry()
	target := health.ProbeTarget{
		RouteID:   "r1",
		BackendID: "b1",
		ProbeURL:  ts.URL + "/healthz",
		Config: config.HealthCheckConfig{
			Path:               "/healthz",
			Interval:           50 * time.Millisecond,
			Timeout:            20 * time.Millisecond,
			HealthyStatusCodes: []int{200},
			UnhealthyThreshold: 1,
			HealthyThreshold:   1,
		},
	}

	checker := health.NewChecker(reg, []health.ProbeTarget{target}, nil)
	checker.Start(context.Background())

	// Let it run for a couple ticks
	time.Sleep(120 * time.Millisecond)

	// Stop must shut down cleanly without leaking goroutines
	checker.Stop()

	if !reg.IsHealthy("r1", "b1") {
		t.Errorf("expected backend to be healthy after probe run")
	}
}

func TestRegistry_StatusQuery(t *testing.T) {
	reg := health.NewRegistry()
	reg.RecordProbeResult("r2", "b2", false, 502, errors.New("conn refused"), 2, 2)

	st, ok := reg.GetStatus("r2", "b2")
	if !ok {
		t.Fatalf("expected status to exist")
	}
	if st.LastStatusCode != 502 {
		t.Errorf("expected last status 502, got %d", st.LastStatusCode)
	}
	if st.LastError != "conn refused" {
		t.Errorf("expected last error 'conn refused', got %q", st.LastError)
	}
}
