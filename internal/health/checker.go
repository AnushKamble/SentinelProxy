package health

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/sentinelproxy/sentinelproxy/internal/config"
)

// ProbeTarget identifies a backend health probe target and policy.
type ProbeTarget struct {
	RouteID   string
	BackendID string
	ProbeURL  string
	Config    config.HealthCheckConfig
}

// TransitionHook is called when a backend changes health state.
type TransitionHook func(routeID, backendID string, healthy bool)

// Checker executes active periodic HTTP health checks against backend targets.
type Checker struct {
	registry     *Registry
	client       *http.Client
	targets      []ProbeTarget
	probingLocks map[string]*sync.Mutex
	mu           sync.Mutex
	wg           sync.WaitGroup
	cancel       context.CancelFunc
	onTransition TransitionHook
}

// NewChecker creates a health check worker pool with dedicated isolated HTTP client.
func NewChecker(registry *Registry, targets []ProbeTarget, hook TransitionHook) *Checker {
	// Isolated transport for health probes; never shares connections with reverse proxy
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   2 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        50,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     30 * time.Second,
		DisableKeepAlives:   false,
	}

	client := &http.Client{
		Transport: transport,
	}

	locks := make(map[string]*sync.Mutex, len(targets))
	for _, t := range targets {
		key := stateKey(t.RouteID, t.BackendID)
		locks[key] = &sync.Mutex{}
		registry.Register(t.RouteID, t.BackendID)
	}

	return &Checker{
		registry:     registry,
		client:       client,
		targets:      targets,
		probingLocks: locks,
		onTransition: hook,
	}
}

// ProbeOnce executes a single health check probe against a target without overlapping.
func (c *Checker) ProbeOnce(ctx context.Context, target ProbeTarget) (healthy bool, transitioned bool) {
	key := stateKey(target.RouteID, target.BackendID)
	c.mu.Lock()
	probeMu := c.probingLocks[key]
	if probeMu == nil {
		probeMu = &sync.Mutex{}
		c.probingLocks[key] = probeMu
	}
	c.mu.Unlock()

	// Ensure no concurrent overlapping probes to the same backend
	if !probeMu.TryLock() {
		return c.registry.IsHealthy(target.RouteID, target.BackendID), false
	}
	defer probeMu.Unlock()

	probeCtx, cancel := context.WithTimeout(ctx, target.Config.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, target.ProbeURL, nil)
	if err != nil {
		transitioned = c.registry.RecordProbeResult(target.RouteID, target.BackendID, false, 0, err, target.Config.HealthyThreshold, target.Config.UnhealthyThreshold)
		return c.registry.IsHealthy(target.RouteID, target.BackendID), transitioned
	}

	resp, err := c.client.Do(req)
	var statusCode int
	var probeSuccess bool

	if err == nil {
		statusCode = resp.StatusCode
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()

		for _, code := range target.Config.HealthyStatusCodes {
			if statusCode == code {
				probeSuccess = true
				break
			}
		}
	}

	transitioned = c.registry.RecordProbeResult(
		target.RouteID,
		target.BackendID,
		probeSuccess,
		statusCode,
		err,
		target.Config.HealthyThreshold,
		target.Config.UnhealthyThreshold,
	)

	isHealthy := c.registry.IsHealthy(target.RouteID, target.BackendID)
	if transitioned && c.onTransition != nil {
		c.onTransition(target.RouteID, target.BackendID, isHealthy)
	}

	return isHealthy, transitioned
}

// Start launches background health check loops for all targets.
func (c *Checker) Start(ctx context.Context) {
	c.mu.Lock()
	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.mu.Unlock()

	for _, target := range c.targets {
		if target.Config.Path == "" || target.Config.Interval <= 0 {
			continue
		}

		c.wg.Add(1)
		go func(tgt ProbeTarget) {
			defer c.wg.Done()
			ticker := time.NewTicker(tgt.Config.Interval)
			defer ticker.Stop()

			// Initial immediate probe
			_, _ = c.ProbeOnce(runCtx, tgt)

			for {
				select {
				case <-runCtx.Done():
					return
				case <-ticker.C:
					_, _ = c.ProbeOnce(runCtx, tgt)
				}
			}
		}(target)
	}
}

// Stop terminates all health check goroutines and waits for completion.
func (c *Checker) Stop() {
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Unlock()

	c.wg.Wait()
}
