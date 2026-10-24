package ratelimit_test

import (
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sentinelproxy/sentinelproxy/internal/config"
	"github.com/sentinelproxy/sentinelproxy/internal/ratelimit"
)

type testClock struct {
	mu  sync.RWMutex
	now time.Time
}

func newTestClock(initial time.Time) *testClock {
	return &testClock{now: initial}
}

func (c *testClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestLimiter_BurstRefillAndIsolation(t *testing.T) {
	clk := newTestClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	cfg := config.RateLimitConfig{
		Enabled:           true,
		RequestsPerSecond: 2.0, // 2 tokens per second
		Burst:             3,   // 3 initial tokens
	}

	l := ratelimit.NewLimiter("test-route", cfg, clk)

	// Caller A: consume burst of 3
	for i := 0; i < 3; i++ {
		allowed, _ := l.Allow("user-A")
		if !allowed {
			t.Fatalf("request %d for user-A should be allowed", i+1)
		}
	}

	// 4th request must be denied
	allowed, retryAfter := l.Allow("user-A")
	if allowed {
		t.Errorf("4th request for user-A should be denied")
	}
	if retryAfter < time.Second {
		t.Errorf("expected Retry-After >= 1s, got %v", retryAfter)
	}

	// Isolation: Caller B has its own independent bucket and can consume 3
	for i := 0; i < 3; i++ {
		allowedB, _ := l.Allow("user-B")
		if !allowedB {
			t.Fatalf("request %d for user-B should be allowed despite user-A exhausted", i+1)
		}
	}

	// Refill: Advance time by 1 second (generates 2 tokens for user-A)
	clk.Advance(1 * time.Second)

	// User-A can now make 2 requests
	a1, _ := l.Allow("user-A")
	a2, _ := l.Allow("user-A")
	a3, _ := l.Allow("user-A")

	if !a1 || !a2 {
		t.Errorf("expected 2 refilled tokens to allow requests, got a1=%v, a2=%v", a1, a2)
	}
	if a3 {
		t.Errorf("3rd request should be denied after consuming refilled 2 tokens")
	}
}

func TestLimiter_ConcurrentNoOverspend(t *testing.T) {
	clk := newTestClock(time.Now())
	cfg := config.RateLimitConfig{
		Enabled:           true,
		RequestsPerSecond: 0, // No refill during test
		Burst:             15,
	}

	l := ratelimit.NewLimiter("test-route", cfg, clk)

	const goroutines = 60
	var wg sync.WaitGroup
	var allowedCount atomic.Int64
	var deniedCount atomic.Int64

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed, _ := l.Allow("shared-key")
			if allowed {
				allowedCount.Add(1)
			} else {
				deniedCount.Add(1)
			}
		}()
	}

	wg.Wait()

	if allowedCount.Load() != 15 {
		t.Errorf("expected exactly 15 allowed requests, got %d", allowedCount.Load())
	}
	if deniedCount.Load() != 45 {
		t.Errorf("expected exactly 45 denied requests, got %d", deniedCount.Load())
	}
}

func TestKeyExtractor_PrecedenceAndTrustedProxy(t *testing.T) {
	trustedCIDRs := []string{"10.0.0.0/8", "127.0.0.1/32"}
	ke, err := ratelimit.NewKeyExtractor(trustedCIDRs)
	if err != nil {
		t.Fatalf("failed to create key extractor: %v", err)
	}

	// 1. Authenticated sub takes precedence over IP and headers
	req1 := httptest.NewRequest("GET", "/test", nil)
	req1.RemoteAddr = "192.168.1.50:45678"
	req1.Header.Set("X-Forwarded-For", "203.0.113.195")
	key1 := ke.ExtractKey(req1, "auth-user-99")
	if key1 != "sub:auth-user-99" {
		t.Errorf("expected 'sub:auth-user-99', got %q", key1)
	}

	// 2. Untrusted proxy peer (192.168.1.50 not in trusted CIDRs) -> ignores XFF and uses RemoteAddr
	key2 := ke.ExtractKey(req1, "")
	if key2 != "ip:192.168.1.50" {
		t.Errorf("expected untrusted peer IP 'ip:192.168.1.50', got %q", key2)
	}

	// 3. Trusted proxy peer (10.1.2.3 in 10.0.0.0/8) -> parses XFF
	req3 := httptest.NewRequest("GET", "/test", nil)
	req3.RemoteAddr = "10.1.2.3:12345"
	req3.Header.Set("X-Forwarded-For", "203.0.113.195, 10.1.2.3")
	key3 := ke.ExtractKey(req3, "")
	if key3 != "ip:203.0.113.195" {
		t.Errorf("expected forwarded client IP 'ip:203.0.113.195', got %q", key3)
	}
}

func TestLimiter_BoundedMemoryAndIdleCleanup(t *testing.T) {
	clk := newTestClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	cfg := config.RateLimitConfig{
		Enabled:           true,
		RequestsPerSecond: 10,
		Burst:             10,
	}

	l := ratelimit.NewLimiter("bounded-test", cfg, clk)
	// Set small limits for test: max 3 entries, idle timeout 5 minutes
	l.SetLimits(3, 5*time.Minute)

	l.Allow("key-1")
	l.Allow("key-2")
	l.Allow("key-3")

	if l.EntryCount() != 3 {
		t.Errorf("expected 3 entries, got %d", l.EntryCount())
	}

	// Advance time past idle timeout (e.g. 6 minutes)
	clk.Advance(6 * time.Minute)

	// Touch key-3 so it is active
	l.Allow("key-3")

	// Adding key-4 triggers idle cleanup which evicts idle key-1 and key-2
	l.Allow("key-4")

	if l.EntryCount() > 3 {
		t.Errorf("entry count should not exceed capacity 3, got %d", l.EntryCount())
	}

	// Cleanup test
	clk.Advance(10 * time.Minute)
	evicted := l.Cleanup(clk.Now())
	if evicted == 0 {
		t.Errorf("expected idle cleanup to evict entries")
	}
	if l.EntryCount() != 0 {
		t.Errorf("expected 0 entries after full cleanup, got %d", l.EntryCount())
	}
}
