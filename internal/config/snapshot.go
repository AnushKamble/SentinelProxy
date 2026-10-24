package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync/atomic"
	"time"
)

// RuntimeSnapshot is an immutable view of SentinelProxy's active configuration.
type RuntimeSnapshot struct {
	Config      *Config
	Fingerprint string
	LoadedAt    time.Time
	// SortedRoutes is sorted by descending path_prefix length for deterministic longest-prefix matching
	SortedRoutes []RouteConfig
	routesByID   map[string]RouteConfig
}

// NewRuntimeSnapshot constructs a new immutable snapshot from a validated Config.
func NewRuntimeSnapshot(cfg *Config) (*RuntimeSnapshot, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}

	// Calculate deterministic policy fingerprint
	h := sha256.New()
	for _, r := range cfg.Routes {
		fmt.Fprintf(h, "route:%s:%s:%v:%s:%v\n", r.ID, r.PathPrefix, r.Methods, r.LoadBalancer, r.Timeout)
		fmt.Fprintf(h, "cb:%v:%v:%v:%v\n", r.CircuitBreaker.Enabled, r.CircuitBreaker.Window, r.CircuitBreaker.FailureRateThreshold, r.CircuitBreaker.OpenDuration)
		fmt.Fprintf(h, "rl:%v:%v:%v\n", r.RateLimit.Enabled, r.RateLimit.RequestsPerSecond, r.RateLimit.Burst)
		for _, b := range r.Backends {
			fmt.Fprintf(h, "backend:%s:%s:%d\n", b.ID, b.URL, b.Weight)
		}
	}
	fp := hex.EncodeToString(h.Sum(nil))

	// Copy and sort routes by path prefix length descending
	sorted := make([]RouteConfig, len(cfg.Routes))
	copy(sorted, cfg.Routes)
	sort.Slice(sorted, func(i, j int) bool {
		if len(sorted[i].PathPrefix) != len(sorted[j].PathPrefix) {
			return len(sorted[i].PathPrefix) > len(sorted[j].PathPrefix)
		}
		return sorted[i].PathPrefix < sorted[j].PathPrefix
	})

	byID := make(map[string]RouteConfig, len(cfg.Routes))
	for _, r := range cfg.Routes {
		byID[r.ID] = r
	}

	return &RuntimeSnapshot{
		Config:       cfg,
		Fingerprint:  fp,
		LoadedAt:     time.Now(),
		SortedRoutes: sorted,
		routesByID:   byID,
	}, nil
}

// RouteByID returns route config for a given ID if found.
func (s *RuntimeSnapshot) RouteByID(id string) (RouteConfig, bool) {
	r, ok := s.routesByID[id]
	return r, ok
}

// SnapshotManager manages atomic pointer swapping of RuntimeSnapshot.
type SnapshotManager struct {
	ptr atomic.Pointer[RuntimeSnapshot]
}

// NewSnapshotManager creates a manager initialized with an initial snapshot.
func NewSnapshotManager(initial *RuntimeSnapshot) *SnapshotManager {
	sm := &SnapshotManager{}
	sm.ptr.Store(initial)
	return sm
}

// Get returns the current active immutable snapshot.
func (sm *SnapshotManager) Get() *RuntimeSnapshot {
	return sm.ptr.Load()
}

// Swap atomically replaces the current snapshot with a new snapshot and returns the old snapshot.
func (sm *SnapshotManager) Swap(newSnap *RuntimeSnapshot) *RuntimeSnapshot {
	return sm.ptr.Swap(newSnap)
}
