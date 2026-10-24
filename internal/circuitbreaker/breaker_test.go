package circuitbreaker_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/sentinelproxy/sentinelproxy/internal/circuitbreaker"
	"github.com/sentinelproxy/sentinelproxy/internal/config"
)

func defaultTestBreakerConfig() config.CircuitBreakerConfig {
	return config.CircuitBreakerConfig{
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
}

// 1. Closed -> Open at exact threshold when minimum sample count reached.
func TestCircuitBreaker_OpensAtExactThreshold(t *testing.T) {
	clk := circuitbreaker.NewFakeClock(time.Now())
	cfg := defaultTestBreakerConfig()

	transitions := []string{}
	cb := circuitbreaker.NewCircuitBreaker("r1", "b1", cfg, clk, func(_, _ string, from, to circuitbreaker.State) {
		transitions = append(transitions, string(from)+"->"+string(to))
	})

	// Send 5 successes and 4 failures (total 9 samples, < 10 minimumRequests)
	for i := 0; i < 5; i++ {
		p, err := cb.TryAcquire()
		if err != nil {
			t.Fatalf("unexpected acquire error: %v", err)
		}
		p.RecordSuccess()
	}
	for i := 0; i < 4; i++ {
		p, err := cb.TryAcquire()
		if err != nil {
			t.Fatalf("unexpected acquire error: %v", err)
		}
		p.RecordFailure()
	}

	if cb.State() != circuitbreaker.StateClosed {
		t.Errorf("expected circuit to remain CLOSED with 9 samples, got %s", cb.State())
	}

	// 10th sample: failure. Total 10, failures 5 => 50% failure rate >= 0.5 threshold
	p, err := cb.TryAcquire()
	if err != nil {
		t.Fatalf("unexpected acquire error: %v", err)
	}
	p.RecordFailure()

	if cb.State() != circuitbreaker.StateOpen {
		t.Errorf("expected circuit to transition to OPEN at 10th sample with 50%% failure rate, got %s", cb.State())
	}

	if len(transitions) != 1 || transitions[0] != "CLOSED->OPEN" {
		t.Errorf("expected 1 transition CLOSED->OPEN, got %v", transitions)
	}
}

// 2. Low sample volume does not open circuit even at 100% failures.
func TestCircuitBreaker_LowSampleVolumeDoesNotOpen(t *testing.T) {
	clk := circuitbreaker.NewFakeClock(time.Now())
	cfg := defaultTestBreakerConfig()
	cfg.MinimumRequests = 20

	cb := circuitbreaker.NewCircuitBreaker("r1", "b1", cfg, clk, nil)

	// Send 19 consecutive failures (< 20 minimum requests)
	for i := 0; i < 19; i++ {
		p, err := cb.TryAcquire()
		if err != nil {
			t.Fatalf("acquire error: %v", err)
		}
		p.RecordFailure()
	}

	if cb.State() != circuitbreaker.StateClosed {
		t.Errorf("circuit should remain CLOSED below minimum request count, got %s", cb.State())
	}
}

// 3. Time bucket expires stale failures.
func TestCircuitBreaker_TimeBucketExpiresStaleFailures(t *testing.T) {
	clk := circuitbreaker.NewFakeClock(time.Now())
	cfg := defaultTestBreakerConfig()
	cfg.MinimumRequests = 10
	cfg.Window = 10 * time.Second

	cb := circuitbreaker.NewCircuitBreaker("r1", "b1", cfg, clk, nil)

	// Send 9 failures
	for i := 0; i < 9; i++ {
		p, _ := cb.TryAcquire()
		p.RecordFailure()
	}

	// Advance time past the entire 10s window (e.g. 15s)
	clk.Advance(15 * time.Second)

	// Send 1 failure in new window
	p, _ := cb.TryAcquire()
	p.RecordFailure()

	// Should still be closed because old failures rolled off and total samples in window is now only 1
	if cb.State() != circuitbreaker.StateClosed {
		t.Errorf("expected circuit to remain CLOSED after stale failures expired, got %s", cb.State())
	}
}

// 4. Open rejects normal traffic before open timeout.
func TestCircuitBreaker_OpenRejectsTrafficBeforeTimeout(t *testing.T) {
	clk := circuitbreaker.NewFakeClock(time.Now())
	cfg := defaultTestBreakerConfig()
	cfg.MinimumRequests = 5
	cfg.OpenDuration = 5 * time.Second

	cb := circuitbreaker.NewCircuitBreaker("r1", "b1", cfg, clk, nil)

	// Trip the breaker to OPEN
	for i := 0; i < 5; i++ {
		p, _ := cb.TryAcquire()
		p.RecordFailure()
	}

	if cb.State() != circuitbreaker.StateOpen {
		t.Fatalf("expected breaker to be OPEN, got %s", cb.State())
	}

	// Advance only 2s (< 5s OpenDuration)
	clk.Advance(2 * time.Second)

	_, err := cb.TryAcquire()
	if !errors.Is(err, circuitbreaker.ErrCircuitOpen) {
		t.Errorf("expected ErrCircuitOpen before timeout, got %v", err)
	}
}

// 5 & 8. Open timeout transitions to half-open and allows only configured half-open probes.
func TestCircuitBreaker_HalfOpenProbeLimitingAndConcurrency(t *testing.T) {
	clk := circuitbreaker.NewFakeClock(time.Now())
	cfg := defaultTestBreakerConfig()
	cfg.MinimumRequests = 5
	cfg.OpenDuration = 5 * time.Second
	cfg.HalfOpenMaxProbes = 2

	cb := circuitbreaker.NewCircuitBreaker("r1", "b1", cfg, clk, nil)

	// Trip breaker
	for i := 0; i < 5; i++ {
		p, _ := cb.TryAcquire()
		p.RecordFailure()
	}

	// Advance past open duration
	clk.Advance(6 * time.Second)

	// Probe 1 admitted
	p1, err := cb.TryAcquire()
	if err != nil {
		t.Fatalf("expected probe 1 to be admitted, got err: %v", err)
	}

	// Probe 2 admitted
	p2, err := cb.TryAcquire()
	if err != nil {
		t.Fatalf("expected probe 2 to be admitted, got err: %v", err)
	}

	// Probe 3 must be rejected (max probes = 2)
	_, err = cb.TryAcquire()
	if !errors.Is(err, circuitbreaker.ErrHalfOpenProbesExhausted) {
		t.Errorf("expected ErrHalfOpenProbesExhausted for probe 3, got: %v", err)
	}

	// Release probe 1 via cancel
	p1.ReleaseCancel()

	// Now another probe can be admitted
	p3, err := cb.TryAcquire()
	if err != nil {
		t.Errorf("expected probe 3 to succeed after probe 1 released, got: %v", err)
	}

	p2.ReleaseCancel()
	p3.ReleaseCancel()
}

// 6. Half-open success threshold closes circuit.
func TestCircuitBreaker_HalfOpenSuccessesCloseCircuit(t *testing.T) {
	clk := circuitbreaker.NewFakeClock(time.Now())
	cfg := defaultTestBreakerConfig()
	cfg.MinimumRequests = 5
	cfg.OpenDuration = 5 * time.Second
	cfg.HalfOpenMaxProbes = 2
	cfg.HalfOpenSuccessesToClose = 2

	cb := circuitbreaker.NewCircuitBreaker("r1", "b1", cfg, clk, nil)

	// Trip breaker
	for i := 0; i < 5; i++ {
		p, _ := cb.TryAcquire()
		p.RecordFailure()
	}

	clk.Advance(6 * time.Second)

	// Probe 1 success
	p1, _ := cb.TryAcquire()
	p1.RecordSuccess()
	if cb.State() != circuitbreaker.StateHalfOpen {
		t.Errorf("circuit should remain HALF_OPEN after 1 success, got %s", cb.State())
	}

	// Probe 2 success -> meets threshold 2
	p2, _ := cb.TryAcquire()
	p2.RecordSuccess()
	if cb.State() != circuitbreaker.StateClosed {
		t.Errorf("circuit should transition to CLOSED after 2 successes, got %s", cb.State())
	}
}

// 7. One half-open failure reopens circuit.
func TestCircuitBreaker_HalfOpenFailureReopens(t *testing.T) {
	clk := circuitbreaker.NewFakeClock(time.Now())
	cfg := defaultTestBreakerConfig()
	cfg.MinimumRequests = 5
	cfg.OpenDuration = 5 * time.Second
	cfg.HalfOpenMaxProbes = 2
	cfg.HalfOpenSuccessesToClose = 2

	cb := circuitbreaker.NewCircuitBreaker("r1", "b1", cfg, clk, nil)

	// Trip breaker
	for i := 0; i < 5; i++ {
		p, _ := cb.TryAcquire()
		p.RecordFailure()
	}

	clk.Advance(6 * time.Second)

	p1, _ := cb.TryAcquire()
	p1.RecordFailure()

	if cb.State() != circuitbreaker.StateOpen {
		t.Errorf("expected circuit to immediately reopen on half-open failure, got %s", cb.State())
	}
}

// 8. Concurrent callers never exceed half-open probe limit.
func TestCircuitBreaker_ConcurrentHalfOpenProbeLimit(t *testing.T) {
	clk := circuitbreaker.NewFakeClock(time.Now())
	cfg := defaultTestBreakerConfig()
	cfg.MinimumRequests = 5
	cfg.OpenDuration = 5 * time.Second
	cfg.HalfOpenMaxProbes = 3

	cb := circuitbreaker.NewCircuitBreaker("r1", "b1", cfg, clk, nil)

	// Trip breaker
	for i := 0; i < 5; i++ {
		p, _ := cb.TryAcquire()
		p.RecordFailure()
	}

	clk.Advance(6 * time.Second)

	const goroutines = 50
	var wg sync.WaitGroup
	var admittedCount int
	var mu sync.Mutex

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := cb.TryAcquire()
			if err == nil {
				mu.Lock()
				admittedCount++
				mu.Unlock()
				// Hold permit until all goroutines attempt acquire
				time.Sleep(10 * time.Millisecond)
				p.ReleaseCancel()
			}
		}()
	}

	wg.Wait()

	if admittedCount > 3 {
		t.Errorf("expected at most 3 admitted probes concurrently, got %d", admittedCount)
	}
}

// 9. Disabled breaker forwards all requests.
func TestCircuitBreaker_DisabledBreaker(t *testing.T) {
	cfg := defaultTestBreakerConfig()
	cfg.Enabled = false

	cb := circuitbreaker.NewCircuitBreaker("r1", "b1", cfg, nil, nil)

	for i := 0; i < 50; i++ {
		p, err := cb.TryAcquire()
		if err != nil {
			t.Fatalf("disabled breaker must never reject, got: %v", err)
		}
		p.RecordFailure()
	}

	if cb.State() != circuitbreaker.StateClosed {
		t.Errorf("disabled breaker state should remain CLOSED, got %s", cb.State())
	}
}

// 10. Client 4xx and cancellations do not trip breaker.
func TestClassifier_ClientErrorsAndCancellationNotCounted(t *testing.T) {
	classifier := circuitbreaker.NewClassifier([]int{500, 502, 503, 504})

	// 200 OK -> not failure
	if classifier.IsFailure(http.StatusOK, nil) {
		t.Errorf("200 OK should not be counted as failure")
	}

	// 400 Bad Request, 404 Not Found -> not failure
	if classifier.IsFailure(http.StatusBadRequest, nil) {
		t.Errorf("400 Bad Request should not be counted as failure")
	}
	if classifier.IsFailure(http.StatusNotFound, nil) {
		t.Errorf("404 Not Found should not be counted as failure")
	}

	// Client context cancellation -> not upstream failure
	if classifier.IsFailure(0, context.Canceled) {
		t.Errorf("context.Canceled should not be counted as upstream failure")
	}

	// Upstream 500, 502, 503, 504 -> counted as failure
	if !classifier.IsFailure(http.StatusInternalServerError, nil) {
		t.Errorf("500 Internal Server Error should be counted as failure")
	}
	if !classifier.IsFailure(http.StatusBadGateway, nil) {
		t.Errorf("502 Bad Gateway should be counted as failure")
	}

	// Deadline exceeded -> counted as failure
	if !classifier.IsFailure(0, context.DeadlineExceeded) {
		t.Errorf("context.DeadlineExceeded should be counted as failure")
	}
}
