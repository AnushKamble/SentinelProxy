package balancer

import (
	"errors"
	"net/url"
	"sync"
	"sync/atomic"
)

// ErrNoEligibleBackend is returned when all backend candidates are unhealthy or circuit-broken.
var ErrNoEligibleBackend = errors.New("no eligible backend candidate available")

// Backend represents an upstream candidate with concurrent in-flight tracking.
type Backend struct {
	ID       string
	URL      *url.URL
	Weight   int
	InFlight atomic.Int64
}

// EligibilityChecker determines if a backend is allowed to receive traffic.
type EligibilityChecker interface {
	IsEligible(backendID string) bool
}

// ReleaseFunc releases in-flight resources acquired during backend selection.
// It is guaranteed to execute at most once.
type ReleaseFunc func()

// Balancer defines the candidate selection contract.
type Balancer interface {
	Select(candidates []*Backend, checker EligibilityChecker) (*Backend, ReleaseFunc, error)
}

func makeReleaseFunc(b *Backend) ReleaseFunc {
	var once sync.Once
	return func() {
		once.Do(func() {
			b.InFlight.Add(-1)
		})
	}
}

// RoundRobinBalancer distributes requests sequentially across eligible backends.
type RoundRobinBalancer struct {
	index atomic.Uint64
}

// NewRoundRobinBalancer creates a thread-safe round-robin balancer.
func NewRoundRobinBalancer() *RoundRobinBalancer {
	return &RoundRobinBalancer{}
}

func (rr *RoundRobinBalancer) Select(candidates []*Backend, checker EligibilityChecker) (*Backend, ReleaseFunc, error) {
	if len(candidates) == 0 {
		return nil, nil, ErrNoEligibleBackend
	}

	// Filter eligible backends
	eligible := make([]*Backend, 0, len(candidates))
	for _, b := range candidates {
		if checker == nil || checker.IsEligible(b.ID) {
			eligible = append(eligible, b)
		}
	}

	if len(eligible) == 0 {
		return nil, nil, ErrNoEligibleBackend
	}

	idx := rr.index.Add(1) - 1
	selected := eligible[idx%uint64(len(eligible))]
	selected.InFlight.Add(1)

	return selected, makeReleaseFunc(selected), nil
}

// LeastConnectionsBalancer routes requests to the eligible backend with the fewest active connections.
type LeastConnectionsBalancer struct {
	tieBreaker atomic.Uint64
}

// NewLeastConnectionsBalancer creates a thread-safe least-connections balancer with round-robin tie-breaking.
func NewLeastConnectionsBalancer() *LeastConnectionsBalancer {
	return &LeastConnectionsBalancer{}
}

func (lc *LeastConnectionsBalancer) Select(candidates []*Backend, checker EligibilityChecker) (*Backend, ReleaseFunc, error) {
	if len(candidates) == 0 {
		return nil, nil, ErrNoEligibleBackend
	}

	// Filter eligible backends
	eligible := make([]*Backend, 0, len(candidates))
	for _, b := range candidates {
		if checker == nil || checker.IsEligible(b.ID) {
			eligible = append(eligible, b)
		}
	}

	if len(eligible) == 0 {
		return nil, nil, ErrNoEligibleBackend
	}

	// Snapshot in-flight counts in a single pass to prevent concurrent-modification race
	snapshotCounts := make([]int64, len(eligible))
	minCount := eligible[0].InFlight.Load()
	snapshotCounts[0] = minCount

	for i := 1; i < len(eligible); i++ {
		c := eligible[i].InFlight.Load()
		snapshotCounts[i] = c
		if c < minCount {
			minCount = c
		}
	}

	// Collect candidates that share the minimum in-flight count in this snapshot
	var tied []*Backend
	for i, b := range eligible {
		if snapshotCounts[i] == minCount {
			tied = append(tied, b)
		}
	}

	if len(tied) == 0 {
		tied = eligible
	}

	// Tie-break deterministically using round-robin among equals
	idx := lc.tieBreaker.Add(1) - 1
	selected := tied[idx%uint64(len(tied))]
	selected.InFlight.Add(1)

	return selected, makeReleaseFunc(selected), nil
}
