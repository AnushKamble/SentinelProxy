package ratelimit

import (
	"sync"
	"time"

	"github.com/sentinelproxy/sentinelproxy/internal/config"
)

// Clock provides monotonic time abstraction for testing.
type Clock interface {
	Now() time.Time
}

// RealClock uses standard system time.
type RealClock struct{}

func (RealClock) Now() time.Time {
	return time.Now()
}

// TokenBucket implements a local in-memory token bucket for a single caller key.
// Rate limiting is local to this gateway process, not distributed across replicas.
type TokenBucket struct {
	mu         sync.Mutex
	rate       float64 // tokens per second
	burst      float64
	tokens     float64
	lastRefill time.Time
	lastAccess time.Time
}

func newTokenBucket(rate float64, burst float64, now time.Time) *TokenBucket {
	return &TokenBucket{
		rate:       rate,
		burst:      burst,
		tokens:     burst, // Start full
		lastRefill: now,
		lastAccess: now,
	}
}

// Allow attempts to consume cost tokens from the bucket.
// Returns whether the request is allowed, and if rejected, a non-zero Retry-After duration.
func (tb *TokenBucket) Allow(now time.Time, cost float64) (bool, time.Duration) {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	tb.lastAccess = now

	// Calculate monotonic refill
	elapsed := now.Sub(tb.lastRefill).Seconds()
	if elapsed > 0 {
		tb.tokens += elapsed * tb.rate
		if tb.tokens > tb.burst {
			tb.tokens = tb.burst
		}
		tb.lastRefill = now
	}

	if tb.tokens >= cost {
		tb.tokens -= cost
		return true, 0
	}

	// Calculate retry after duration
	missingTokens := cost - tb.tokens
	waitSeconds := missingTokens / tb.rate
	retryAfter := time.Duration(waitSeconds * float64(time.Second))
	if retryAfter < time.Second {
		retryAfter = time.Second
	}

	return false, retryAfter
}

// Limiter manages local per-key token buckets for a route with bounded memory and LRU/idle cleanup.
type Limiter struct {
	mu          sync.Mutex
	routeID     string
	cfg         config.RateLimitConfig
	clock       Clock
	maxEntries  int
	idleTimeout time.Duration
	buckets     map[string]*TokenBucket
}

// NewLimiter creates a local token-bucket rate limiter.
func NewLimiter(routeID string, cfg config.RateLimitConfig, clk Clock) *Limiter {
	if clk == nil {
		clk = RealClock{}
	}
	return &Limiter{
		routeID:     routeID,
		cfg:         cfg,
		clock:       clk,
		maxEntries:  10000,            // Hard upper bound on key memory
		idleTimeout: 10 * time.Minute, // Idle eviction timeout
		buckets:     make(map[string]*TokenBucket),
	}
}

// SetLimits allows customizing capacity limits for testing.
func (l *Limiter) SetLimits(maxEntries int, idleTimeout time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.maxEntries = maxEntries
	l.idleTimeout = idleTimeout
}

// Allow checks if the given caller key has available quota.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	if !l.cfg.Enabled {
		return true, 0
	}

	now := l.clock.Now()

	l.mu.Lock()
	tb, exists := l.buckets[key]
	if !exists {
		// Bounded memory check
		if len(l.buckets) >= l.maxEntries {
			l.evictIdleLocked(now)
			// If still full after idle cleanup, evict least recently accessed key
			if len(l.buckets) >= l.maxEntries {
				l.evictOldestLocked()
			}
		}

		tb = newTokenBucket(l.cfg.RequestsPerSecond, float64(l.cfg.Burst), now)
		l.buckets[key] = tb
	}
	l.mu.Unlock()

	return tb.Allow(now, 1.0)
}

// Cleanup removes idle buckets older than idleTimeout.
func (l *Limiter) Cleanup(now time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.evictIdleLocked(now)
}

func (l *Limiter) evictIdleLocked(now time.Time) int {
	evicted := 0
	for k, tb := range l.buckets {
		tb.mu.Lock()
		idle := now.Sub(tb.lastAccess) > l.idleTimeout
		tb.mu.Unlock()

		if idle {
			delete(l.buckets, k)
			evicted++
		}
	}
	return evicted
}

func (l *Limiter) evictOldestLocked() {
	var oldestKey string
	var oldestTime time.Time

	for k, tb := range l.buckets {
		tb.mu.Lock()
		acc := tb.lastAccess
		tb.mu.Unlock()

		if oldestKey == "" || acc.Before(oldestTime) {
			oldestKey = k
			oldestTime = acc
		}
	}

	if oldestKey != "" {
		delete(l.buckets, oldestKey)
	}
}

// EntryCount returns the number of active key buckets in the cache.
func (l *Limiter) EntryCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
