package circuitbreaker

import (
	"sync"
	"time"
)

// Clock provides monotonic time abstraction for testing.
type Clock interface {
	Now() time.Time
}

// RealClock uses standard system monotonic time.
type RealClock struct{}

func (RealClock) Now() time.Time {
	return time.Now()
}

// FakeClock provides deterministic manual time advancement for tests.
type FakeClock struct {
	mu  sync.RWMutex
	now time.Time
}

// NewFakeClock creates a fake clock initialized to an arbitrary non-zero time.
func NewFakeClock(initial time.Time) *FakeClock {
	if initial.IsZero() {
		initial = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return &FakeClock{now: initial}
}

// Now returns the simulated current time.
func (fc *FakeClock) Now() time.Time {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	return fc.now
}

// Advance moves simulated time forward by duration d.
func (fc *FakeClock) Advance(d time.Duration) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.now = fc.now.Add(d)
}

// Set sets the simulated time to t.
func (fc *FakeClock) Set(t time.Time) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.now = t
}
