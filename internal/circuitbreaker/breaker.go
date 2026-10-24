package circuitbreaker

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/sentinelproxy/sentinelproxy/internal/config"
)

// State represents the circuit breaker operational mode.
type State string

const (
	StateClosed   State = "CLOSED"
	StateOpen     State = "OPEN"
	StateHalfOpen State = "HALF_OPEN"
)

var (
	// ErrCircuitOpen is returned when traffic is rejected because the circuit is OPEN.
	ErrCircuitOpen = errors.New("circuit breaker is open")
	// ErrHalfOpenProbesExhausted is returned when the maximum concurrent half-open probes are in flight.
	ErrHalfOpenProbesExhausted = errors.New("circuit breaker half-open probe limit reached")
)

// StateChangeHook is invoked when a breaker transitions states.
type StateChangeHook func(routeID string, backendID string, from State, to State)

// CircuitBreaker manages fault isolation for a specific route/backend pair.
type CircuitBreaker struct {
	mu                     sync.Mutex
	RouteID                string
	BackendID              string
	cfg                    config.CircuitBreakerConfig
	clock                  Clock
	classifier             *Classifier
	window                 *SlidingWindow
	state                  State
	openedAt               time.Time
	activeHalfOpenProbes   int
	halfOpenSuccesses      int
	onStateChange          StateChangeHook
}

// NewCircuitBreaker creates a circuit breaker instance.
func NewCircuitBreaker(routeID, backendID string, cfg config.CircuitBreakerConfig, clk Clock, hook StateChangeHook) *CircuitBreaker {
	if clk == nil {
		clk = RealClock{}
	}
	classifier := NewClassifier(cfg.CountedStatusCodes)
	window := NewSlidingWindow(cfg.Window, cfg.BucketCount)

	return &CircuitBreaker{
		RouteID:       routeID,
		BackendID:     backendID,
		cfg:           cfg,
		clock:         clk,
		classifier:    classifier,
		window:        window,
		state:         StateClosed,
		onStateChange: hook,
	}
}

// State returns the current operational state.
func (cb *CircuitBreaker) State() State {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.currentStateLocked(cb.clock.Now())
}

// Classifier returns the failure classifier for this breaker.
func (cb *CircuitBreaker) Classifier() *Classifier {
	return cb.classifier
}

// TryAcquire requests an admission permit to send traffic to the backend.
func (cb *CircuitBreaker) TryAcquire() (Permit, error) {
	if !cb.cfg.Enabled {
		return noopPermit{}, nil
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := cb.clock.Now()
	state := cb.currentStateLocked(now)

	switch state {
	case StateClosed:
		return &circuitPermit{cb: cb, state: StateClosed}, nil

	case StateOpen:
		return nil, ErrCircuitOpen

	case StateHalfOpen:
		if cb.activeHalfOpenProbes < cb.cfg.HalfOpenMaxProbes {
			cb.activeHalfOpenProbes++
			return &circuitPermit{cb: cb, state: StateHalfOpen}, nil
		}
		return nil, ErrHalfOpenProbesExhausted

	default:
		return nil, fmt.Errorf("unknown circuit breaker state: %s", state)
	}
}

func (cb *CircuitBreaker) currentStateLocked(now time.Time) State {
	if cb.state == StateOpen {
		if now.Sub(cb.openedAt) >= cb.cfg.OpenDuration {
			cb.transitionLocked(StateHalfOpen)
			cb.activeHalfOpenProbes = 0
			cb.halfOpenSuccesses = 0
		}
	}
	return cb.state
}

func (cb *CircuitBreaker) transitionLocked(to State) {
	from := cb.state
	if from == to {
		return
	}
	cb.state = to
	if cb.onStateChange != nil {
		cb.onStateChange(cb.RouteID, cb.BackendID, from, to)
	}
}

func (cb *CircuitBreaker) onPermitSuccess(permitState State) {
	if !cb.cfg.Enabled {
		return
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := cb.clock.Now()

	switch permitState {
	case StateClosed:
		cb.window.Add(now, false)

	case StateHalfOpen:
		cb.activeHalfOpenProbes--
		if cb.activeHalfOpenProbes < 0 {
			cb.activeHalfOpenProbes = 0
		}
		if cb.state == StateHalfOpen {
			cb.halfOpenSuccesses++
			if cb.halfOpenSuccesses >= cb.cfg.HalfOpenSuccessesToClose {
				cb.transitionLocked(StateClosed)
				cb.window.Reset()
				cb.halfOpenSuccesses = 0
				cb.activeHalfOpenProbes = 0
			}
		}
	}
}

func (cb *CircuitBreaker) onPermitFailure(permitState State) {
	if !cb.cfg.Enabled {
		return
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := cb.clock.Now()

	switch permitState {
	case StateClosed:
		cb.window.Add(now, true)
		total, failures := cb.window.Counts(now)
		if total >= uint64(cb.cfg.MinimumRequests) {
			failureRate := float64(failures) / float64(total)
			if failureRate >= cb.cfg.FailureRateThreshold {
				cb.transitionLocked(StateOpen)
				cb.openedAt = now
				cb.activeHalfOpenProbes = 0
				cb.halfOpenSuccesses = 0
			}
		}

	case StateHalfOpen:
		cb.activeHalfOpenProbes--
		if cb.activeHalfOpenProbes < 0 {
			cb.activeHalfOpenProbes = 0
		}
		// A single failure in HALF_OPEN immediately returns to OPEN
		cb.transitionLocked(StateOpen)
		cb.openedAt = now
		cb.halfOpenSuccesses = 0
	}
}

func (cb *CircuitBreaker) onPermitCancel(permitState State) {
	if !cb.cfg.Enabled {
		return
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()

	if permitState == StateHalfOpen {
		cb.activeHalfOpenProbes--
		if cb.activeHalfOpenProbes < 0 {
			cb.activeHalfOpenProbes = 0
		}
	}
	// Caller cancellation does not count as success or failure
}
