package circuitbreaker

import (
	"sync"
	"time"
)

type bucket struct {
	epoch     int64
	successes uint64
	failures  uint64
}

// SlidingWindow maintains a ring of time-sliced outcome buckets.
type SlidingWindow struct {
	mu             sync.RWMutex
	windowDuration time.Duration
	bucketDuration time.Duration
	bucketCount    int
	buckets        []bucket
}

// NewSlidingWindow creates a sliding window ring buffer.
func NewSlidingWindow(window time.Duration, bucketCount int) *SlidingWindow {
	if bucketCount < 1 {
		bucketCount = 10
	}
	bucketDur := window / time.Duration(bucketCount)
	return &SlidingWindow{
		windowDuration: window,
		bucketDuration: bucketDur,
		bucketCount:    bucketCount,
		buckets:        make([]bucket, bucketCount),
	}
}

// Add records an outcome at the specified timestamp.
func (sw *SlidingWindow) Add(now time.Time, isFailure bool) {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	epoch := now.UnixNano() / int64(sw.bucketDuration)
	idx := int(epoch % int64(sw.bucketCount))
	if idx < 0 {
		idx = (idx + sw.bucketCount) % sw.bucketCount
	}

	b := &sw.buckets[idx]
	if b.epoch != epoch {
		// New epoch: clear stale bucket counts before reuse
		b.epoch = epoch
		b.successes = 0
		b.failures = 0
	}

	if isFailure {
		b.failures++
	} else {
		b.successes++
	}
}

// Counts returns the total samples and failure count within the valid sliding window.
func (sw *SlidingWindow) Counts(now time.Time) (total uint64, failures uint64) {
	sw.mu.RLock()
	defer sw.mu.RUnlock()

	currentEpoch := now.UnixNano() / int64(sw.bucketDuration)
	minValidEpoch := currentEpoch - int64(sw.bucketCount) + 1

	for i := 0; i < sw.bucketCount; i++ {
		b := &sw.buckets[i]
		if b.epoch >= minValidEpoch && b.epoch <= currentEpoch {
			total += b.successes + b.failures
			failures += b.failures
		}
	}
	return total, failures
}

// Reset clears all buckets in the window.
func (sw *SlidingWindow) Reset() {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	for i := range sw.buckets {
		sw.buckets[i] = bucket{}
	}
}
