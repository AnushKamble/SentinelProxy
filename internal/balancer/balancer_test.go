package balancer_test

import (
	"fmt"
	"net/url"
	"sync"
	"testing"

	"github.com/sentinelproxy/sentinelproxy/internal/balancer"
)

type staticChecker struct {
	ineligibleIDs map[string]bool
}

func (s *staticChecker) IsEligible(backendID string) bool {
	return !s.ineligibleIDs[backendID]
}

func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

func TestRoundRobin_DistributionAndEligibility(t *testing.T) {
	candidates := []*balancer.Backend{
		{ID: "b1", URL: mustParseURL("http://127.0.0.1:9001"), Weight: 1},
		{ID: "b2", URL: mustParseURL("http://127.0.0.1:9002"), Weight: 1},
		{ID: "b3", URL: mustParseURL("http://127.0.0.1:9003"), Weight: 1},
	}

	rr := balancer.NewRoundRobinBalancer()
	counts := make(map[string]int)

	const totalRequests = 3000
	for i := 0; i < totalRequests; i++ {
		b, release, err := rr.Select(candidates, nil)
		if err != nil {
			t.Fatalf("unexpected select error: %v", err)
		}
		counts[b.ID]++
		release() // clean up immediately
	}

	for _, b := range candidates {
		if counts[b.ID] != 1000 {
			t.Errorf("expected exactly 1000 requests for backend %s, got %d", b.ID, counts[b.ID])
		}
		if b.InFlight.Load() != 0 {
			t.Errorf("expected in-flight 0 for backend %s, got %d", b.ID, b.InFlight.Load())
		}
	}

	// Test eligibility filter: mark b2 ineligible
	checker := &staticChecker{
		ineligibleIDs: map[string]bool{"b2": true},
	}
	countsFiltered := make(map[string]int)
	for i := 0; i < 1000; i++ {
		b, release, err := rr.Select(candidates, checker)
		if err != nil {
			t.Fatalf("unexpected select error: %v", err)
		}
		countsFiltered[b.ID]++
		release()
	}

	if countsFiltered["b2"] != 0 {
		t.Errorf("expected 0 requests to ineligible backend b2, got %d", countsFiltered["b2"])
	}
	if countsFiltered["b1"] != 500 || countsFiltered["b3"] != 500 {
		t.Errorf("expected 500 each for b1 and b3, got b1=%d, b3=%d", countsFiltered["b1"], countsFiltered["b3"])
	}
}

func TestLeastConnections_PrefersLessBusyAndTieBreaks(t *testing.T) {
	b1 := &balancer.Backend{ID: "b1", URL: mustParseURL("http://127.0.0.1:9001"), Weight: 1}
	b2 := &balancer.Backend{ID: "b2", URL: mustParseURL("http://127.0.0.1:9002"), Weight: 1}
	candidates := []*balancer.Backend{b1, b2}

	lc := balancer.NewLeastConnectionsBalancer()

	// Simulate b1 holding 5 active requests
	var heldReleases []balancer.ReleaseFunc
	for i := 0; i < 5; i++ {
		_, rel, err := lc.Select([]*balancer.Backend{b1}, nil)
		if err != nil {
			t.Fatalf("failed to acquire on b1: %v", err)
		}
		heldReleases = append(heldReleases, rel)
	}

	if b1.InFlight.Load() != 5 {
		t.Fatalf("expected b1 in-flight 5, got %d", b1.InFlight.Load())
	}
	if b2.InFlight.Load() != 0 {
		t.Fatalf("expected b2 in-flight 0, got %d", b2.InFlight.Load())
	}

	// Next 5 requests must all route to b2 since b2 has fewer in-flight connections
	for i := 0; i < 5; i++ {
		chosen, rel, err := lc.Select(candidates, nil)
		if err != nil {
			t.Fatalf("failed to select: %v", err)
		}
		if chosen.ID != "b2" {
			t.Errorf("expected least-connections to select b2, got %s", chosen.ID)
		}
		heldReleases = append(heldReleases, rel)
	}

	// Now both b1 and b2 have 5 in-flight. Next requests should tie-break evenly
	counts := make(map[string]int)
	for i := 0; i < 100; i++ {
		chosen, rel, err := lc.Select(candidates, nil)
		if err != nil {
			t.Fatalf("failed to select: %v", err)
		}
		counts[chosen.ID]++
		rel()
	}

	if counts["b1"] != 50 || counts["b2"] != 50 {
		t.Errorf("expected even 50/50 tie-break, got b1=%d, b2=%d", counts["b1"], counts["b2"])
	}

	// Release all held connections
	for _, rel := range heldReleases {
		rel()
	}

	if b1.InFlight.Load() != 0 {
		t.Errorf("expected b1 in-flight 0 after releases, got %d", b1.InFlight.Load())
	}
	if b2.InFlight.Load() != 0 {
		t.Errorf("expected b2 in-flight 0 after releases, got %d", b2.InFlight.Load())
	}
}

func TestBalancer_InFlightReleaseGuaranteeUnderConcurrency(t *testing.T) {
	b1 := &balancer.Backend{ID: "b1", URL: mustParseURL("http://127.0.0.1:9001"), Weight: 1}
	b2 := &balancer.Backend{ID: "b2", URL: mustParseURL("http://127.0.0.1:9002"), Weight: 1}
	candidates := []*balancer.Backend{b1, b2}

	balancers := []struct {
		name string
		bal  balancer.Balancer
	}{
		{"round_robin", balancer.NewRoundRobinBalancer()},
		{"least_connections", balancer.NewLeastConnectionsBalancer()},
	}

	for _, tc := range balancers {
		t.Run(tc.name, func(t *testing.T) {
			var wg sync.WaitGroup
			const workers = 50
			const reqsPerWorker = 200

			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func(workerID int) {
					defer wg.Done()
					for r := 0; r < reqsPerWorker; r++ {
						_, release, err := tc.bal.Select(candidates, nil)
						if err != nil {
							t.Errorf("select error: %v", err)
							return
						}

						// Simulate arbitrary outcome: normal release, duplicate release call, or unwind
						if (workerID+r)%3 == 0 {
							// Call release multiple times to ensure idempotence
							release()
							release()
						} else {
							func() {
								defer release()
								// Simulating work
								_ = fmt.Sprintf("work-%d-%d", workerID, r)
							}()
						}
					}
				}(w)
			}

			wg.Wait()

			// In-flight counts must return strictly to 0
			if b1.InFlight.Load() != 0 {
				t.Errorf("[%s] expected b1 in-flight 0, got %d", tc.name, b1.InFlight.Load())
			}
			if b2.InFlight.Load() != 0 {
				t.Errorf("[%s] expected b2 in-flight 0, got %d", tc.name, b2.InFlight.Load())
			}
		})
	}
}

func TestBalancer_AllIneligibleReturnsError(t *testing.T) {
	candidates := []*balancer.Backend{
		{ID: "b1", URL: mustParseURL("http://127.0.0.1:9001"), Weight: 1},
	}
	rr := balancer.NewRoundRobinBalancer()
	checker := &staticChecker{ineligibleIDs: map[string]bool{"b1": true}}

	_, _, err := rr.Select(candidates, checker)
	if err != balancer.ErrNoEligibleBackend {
		t.Errorf("expected ErrNoEligibleBackend, got %v", err)
	}
}
