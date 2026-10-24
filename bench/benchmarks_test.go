package bench_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sentinelproxy/sentinelproxy/bench"
	"github.com/sentinelproxy/sentinelproxy/internal/app"
	"github.com/sentinelproxy/sentinelproxy/internal/circuitbreaker"
	"github.com/sentinelproxy/sentinelproxy/internal/config"
	"github.com/sentinelproxy/sentinelproxy/internal/mock"
	"github.com/sentinelproxy/sentinelproxy/internal/ratelimit"
	"github.com/sentinelproxy/sentinelproxy/internal/router"
)

// -----------------------------------------------------------------------------
// Go Microbenchmarks (Allocations, ns/op)
// -----------------------------------------------------------------------------

func BenchmarkRouterMatch(b *testing.B) {
	routes := []config.RouteConfig{
		{ID: "r1", PathPrefix: "/api/v1/users", Methods: []string{"GET", "POST"}},
		{ID: "r2", PathPrefix: "/api/v1/orders", Methods: []string{"GET", "POST", "PUT"}},
		{ID: "r3", PathPrefix: "/api/v1/payments", Methods: []string{"POST"}},
		{ID: "r4", PathPrefix: "/api/v2/analytics", Methods: []string{"GET"}},
		{ID: "r5", PathPrefix: "/api/v1/users/profile", Methods: []string{"GET"}},
	}
	rtr := router.NewRouter(routes)

	paths := []string{
		"/api/v1/users/profile",
		"/api/v1/orders/123",
		"/api/v1/users/10",
		"/api/v2/analytics/daily",
		"/api/unknown/path",
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		p := paths[i%len(paths)]
		_ = rtr.Match(p, "GET")
	}
}

func BenchmarkCircuitBreakerAcquireRelease(b *testing.B) {
	cbCfg := config.CircuitBreakerConfig{
		Enabled:                  true,
		Window:                   10 * time.Second,
		BucketCount:              10,
		MinimumRequests:          10,
		FailureRateThreshold:     0.5,
		OpenDuration:             5 * time.Second,
		HalfOpenMaxProbes:        2,
		HalfOpenSuccessesToClose: 2,
		CountedStatusCodes:       []int{500, 502, 503, 504},
	}
	cb := circuitbreaker.NewCircuitBreaker("bench-route", "bench-backend", cbCfg, circuitbreaker.RealClock{}, nil)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		permit, err := cb.TryAcquire()
		if err == nil {
			permit.RecordSuccess()
		}
	}
}

func BenchmarkRateLimiterAllow(b *testing.B) {
	rlCfg := config.RateLimitConfig{
		Enabled:           true,
		RequestsPerSecond: 100000,
		Burst:             10000,
	}
	limiter := ratelimit.NewLimiter("bench-route", rlCfg, ratelimit.RealClock{})

	keys := []string{"192.168.1.1", "192.168.1.2", "192.168.1.3", "192.168.1.4"}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		k := keys[i%len(keys)]
		_, _ = limiter.Allow(k)
	}
}

// -----------------------------------------------------------------------------
// Controlled Experiments Evaluation (Baselines A, B, C, D)
// Each executed with multiple repeated runs, computing medians and distributions
// -----------------------------------------------------------------------------

func TestExperiments_FullEvaluationMatrix(t *testing.T) {
	// 1. Setup mock backends
	backendPrimary := mock.NewServer("backend-primary")
	tsPrimary := httptest.NewServer(backendPrimary.Handler())
	defer tsPrimary.Close()

	backendHealthy := mock.NewServer("backend-healthy")
	tsHealthy := httptest.NewServer(backendHealthy.Handler())
	defer tsHealthy.Close()

	// 2. Build configuration with 2 routes: primary (for degradation) and isolated (healthy comparison)
	buildConfig := func(breakerEnabled bool) *config.Config {
		return &config.Config{
			Server: config.ServerConfig{
				ListenAddr:        "127.0.0.1:0",
				ReadHeaderTimeout: 5 * time.Second,
				IdleTimeout:       30 * time.Second,
				ShutdownTimeout:   5 * time.Second,
			},
			Observability: config.ObservabilityConfig{
				LogLevel:    "error", // minimal log overhead during benchmark
				LogFormat:   "json",
				MetricsPath: "/metrics",
			},
			Security: config.SecurityConfig{
				RequestIDHeader: "X-Request-ID",
			},
			Routes: []config.RouteConfig{
				{
					ID:           "route-primary",
					PathPrefix:   "/api/primary",
					Methods:      []string{"GET"},
					LoadBalancer: "round_robin",
					Timeout:      300 * time.Millisecond,
					Backends: []config.BackendConfig{
						{ID: "backend-primary", URL: tsPrimary.URL, Weight: 1},
					},
					CircuitBreaker: config.CircuitBreakerConfig{
						Enabled:                  breakerEnabled,
						Window:                   10 * time.Second,
						BucketCount:              10,
						MinimumRequests:          5,
						FailureRateThreshold:     0.5,
						OpenDuration:             2 * time.Second,
						HalfOpenMaxProbes:        1,
						HalfOpenSuccessesToClose: 1,
						CountedStatusCodes:       []int{500, 502, 503, 504},
					},
				},
				{
					ID:           "route-isolated",
					PathPrefix:   "/api/isolated",
					Methods:      []string{"GET"},
					LoadBalancer: "round_robin",
					Timeout:      1 * time.Second,
					Backends: []config.BackendConfig{
						{ID: "backend-healthy", URL: tsHealthy.URL, Weight: 1},
					},
					CircuitBreaker: config.CircuitBreakerConfig{
						Enabled:                  true,
						Window:                   10 * time.Second,
						BucketCount:              10,
						MinimumRequests:          5,
						FailureRateThreshold:     0.5,
						OpenDuration:             2 * time.Second,
						HalfOpenMaxProbes:        1,
						HalfOpenSuccessesToClose: 1,
						CountedStatusCodes:       []int{500, 502, 503, 504},
					},
				},
			},
		}
	}

	const repetitions = 5
	const runDuration = 1500 * time.Millisecond
	const concurrency = 8
	resultsDir := filepath.Join("..", "results", "benchmarks")

	// -------------------------------------------------------------------------
	// Condition A: Healthy Baseline (All backends healthy, breaker enabled)
	// -------------------------------------------------------------------------
	t.Run("Condition_A_HealthyBaseline", func(t *testing.T) {
		backendPrimary.Reset()
		cfg := buildConfig(true)
		appInst, err := app.New(cfg, "")
		if err != nil {
			t.Fatalf("failed to create app: %v", err)
		}
		gw := httptest.NewServer(appInst.Handler())
		defer gw.Close()

		wCfg := bench.WorkloadConfig{
			Name:           "condition_A_healthy_baseline",
			TargetURL:      gw.URL + "/api/primary",
			Concurrency:    concurrency,
			Duration:       runDuration,
			WarmupDuration: 200 * time.Millisecond,
			Timeout:        2 * time.Second,
		}
		runner := bench.NewRunner(wCfg)
		var runs []bench.RunStats

		for i := 1; i <= repetitions; i++ {
			stats, err := runner.Execute(i)
			if err != nil {
				t.Fatalf("run %d failed: %v", i, err)
			}
			runs = append(runs, *stats)
		}

		summary := bench.ComputeSummary("condition_A_healthy_baseline", runs)
		_ = bench.ExportSummaryJSON(filepath.Join(resultsDir, "condition_A_healthy_baseline_summary.json"), summary)
		_ = bench.ExportRunsCSV(filepath.Join(resultsDir, "condition_A_healthy_baseline_runs.csv"), runs)

		t.Logf("[Condition A] Median Throughput: %.2f req/s | p50: %.2f ms | p95: %.2f ms | Success: %.2f%%",
			summary.ThroughputMedian, summary.LatencyP50Median, summary.LatencyP95Median, summary.SuccessRatePct)

		if summary.SuccessRatePct < 99.0 {
			t.Errorf("expected healthy baseline to have near 100%% success, got %.2f%%", summary.SuccessRatePct)
		}
	})

	// -------------------------------------------------------------------------
	// Condition B: Breaker-Disabled Baseline under Outage/Degradation
	// -------------------------------------------------------------------------
	t.Run("Condition_B_BreakerDisabledDegraded", func(t *testing.T) {
		// Inject 100% 500 failure + 100ms delay into primary backend
		backendPrimary.SetFaults(mock.FaultConfig{
			FailureRate:   1.0,
			FailureStatus: http.StatusInternalServerError,
			DelayMs:       50,
			Seed:          202,
		})

		cfg := buildConfig(false) // Breaker DISABLED
		appInst, err := app.New(cfg, "")
		if err != nil {
			t.Fatalf("failed to create app: %v", err)
		}
		gw := httptest.NewServer(appInst.Handler())
		defer gw.Close()

		wCfg := bench.WorkloadConfig{
			Name:           "condition_B_breaker_disabled_degraded",
			TargetURL:      gw.URL + "/api/primary",
			Concurrency:    concurrency,
			Duration:       runDuration,
			WarmupDuration: 200 * time.Millisecond,
			Timeout:        2 * time.Second,
		}
		runner := bench.NewRunner(wCfg)
		var runs []bench.RunStats

		for i := 1; i <= repetitions; i++ {
			stats, err := runner.Execute(i)
			if err != nil {
				t.Fatalf("run %d failed: %v", i, err)
			}
			runs = append(runs, *stats)
		}

		summary := bench.ComputeSummary("condition_B_breaker_disabled_degraded", runs)
		_ = bench.ExportSummaryJSON(filepath.Join(resultsDir, "condition_B_breaker_disabled_degraded_summary.json"), summary)
		_ = bench.ExportRunsCSV(filepath.Join(resultsDir, "condition_B_breaker_disabled_degraded_runs.csv"), runs)

		t.Logf("[Condition B] Median Throughput: %.2f req/s | p50: %.2f ms | p95: %.2f ms | Success: %.2f%%",
			summary.ThroughputMedian, summary.LatencyP50Median, summary.LatencyP95Median, summary.SuccessRatePct)
	})

	// -------------------------------------------------------------------------
	// Condition C: Breaker-Enabled Condition under Outage/Degradation
	// -------------------------------------------------------------------------
	t.Run("Condition_C_BreakerEnabledDegraded", func(t *testing.T) {
		// Identical failure and delay schedule as Condition B
		backendPrimary.SetFaults(mock.FaultConfig{
			FailureRate:   1.0,
			FailureStatus: http.StatusInternalServerError,
			DelayMs:       50,
			Seed:          202,
		})

		cfg := buildConfig(true) // Breaker ENABLED
		appInst, err := app.New(cfg, "")
		if err != nil {
			t.Fatalf("failed to create app: %v", err)
		}
		gw := httptest.NewServer(appInst.Handler())
		defer gw.Close()

		wCfg := bench.WorkloadConfig{
			Name:           "condition_C_breaker_enabled_degraded",
			TargetURL:      gw.URL + "/api/primary",
			Concurrency:    concurrency,
			Duration:       runDuration,
			WarmupDuration: 200 * time.Millisecond,
			Timeout:        2 * time.Second,
		}
		runner := bench.NewRunner(wCfg)
		var runs []bench.RunStats

		for i := 1; i <= repetitions; i++ {
			stats, err := runner.Execute(i)
			if err != nil {
				t.Fatalf("run %d failed: %v", i, err)
			}
			runs = append(runs, *stats)
		}

		summary := bench.ComputeSummary("condition_C_breaker_enabled_degraded", runs)
		_ = bench.ExportSummaryJSON(filepath.Join(resultsDir, "condition_C_breaker_enabled_degraded_summary.json"), summary)
		_ = bench.ExportRunsCSV(filepath.Join(resultsDir, "condition_C_breaker_enabled_degraded_runs.csv"), runs)

		t.Logf("[Condition C] Median Throughput: %.2f req/s | p50: %.2f ms | p95: %.2f ms | Success: %.2f%%",
			summary.ThroughputMedian, summary.LatencyP50Median, summary.LatencyP95Median, summary.SuccessRatePct)

		// Verification: Fast 503 circuit-breaker rejection should allow higher throughput / lower latency
		// than waiting for backend delay in Condition B!
	})

	// -------------------------------------------------------------------------
	// Condition D: Healthy-Route Isolation Comparison
	// (Check latency and availability of /api/isolated while /api/primary is degrading)
	// -------------------------------------------------------------------------
	t.Run("Condition_D_HealthyRouteIsolation", func(t *testing.T) {
		// Primary is completely failing with delay
		backendPrimary.SetFaults(mock.FaultConfig{
			FailureRate:   1.0,
			FailureStatus: http.StatusInternalServerError,
			DelayMs:       100,
			Seed:          404,
		})
		// Isolated route is 100% healthy
		backendHealthy.Reset()

		cfg := buildConfig(true)
		appInst, err := app.New(cfg, "")
		if err != nil {
			t.Fatalf("failed to create app: %v", err)
		}
		gw := httptest.NewServer(appInst.Handler())
		defer gw.Close()

		// Run background disturbance load on /api/primary
		ctx, cancel := context.WithTimeout(context.Background(), runDuration*repetitions+2*time.Second)
		defer cancel()
		go func() {
			client := gw.Client()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					resp, err := client.Get(gw.URL + "/api/primary")
					if err == nil {
						_ = resp.Body.Close()
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
		}()

		// Benchmark the healthy isolated route
		wCfg := bench.WorkloadConfig{
			Name:           "condition_D_healthy_route_isolation",
			TargetURL:      gw.URL + "/api/isolated",
			Concurrency:    concurrency,
			Duration:       runDuration,
			WarmupDuration: 200 * time.Millisecond,
			Timeout:        2 * time.Second,
		}
		runner := bench.NewRunner(wCfg)
		var runs []bench.RunStats

		for i := 1; i <= repetitions; i++ {
			stats, err := runner.Execute(i)
			if err != nil {
				t.Fatalf("run %d failed: %v", i, err)
			}
			runs = append(runs, *stats)
		}

		summary := bench.ComputeSummary("condition_D_healthy_route_isolation", runs)
		_ = bench.ExportSummaryJSON(filepath.Join(resultsDir, "condition_D_healthy_route_isolation_summary.json"), summary)
		_ = bench.ExportRunsCSV(filepath.Join(resultsDir, "condition_D_healthy_route_isolation_runs.csv"), runs)

		t.Logf("[Condition D] Median Throughput: %.2f req/s | p50: %.2f ms | p95: %.2f ms | Success: %.2f%%",
			summary.ThroughputMedian, summary.LatencyP50Median, summary.LatencyP95Median, summary.SuccessRatePct)

		if summary.SuccessRatePct < 99.0 {
			t.Errorf("expected healthy isolated route to maintain near 100%% success despite primary failure, got %.2f%%", summary.SuccessRatePct)
		}
	})
}
