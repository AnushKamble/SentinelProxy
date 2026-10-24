package health

import (
	"sync"
	"time"
)

// TargetStatus records the current health state and recent probe history of a backend.
type TargetStatus struct {
	RouteID               string
	BackendID             string
	Healthy               bool
	ConsecutiveSuccesses  int
	ConsecutiveFailures   int
	LastChecked           time.Time
	LastStatusCode        int
	LastError             string
}

// Registry manages thread-safe health states for all route/backend pairs.
type Registry struct {
	mu     sync.RWMutex
	states map[string]*TargetStatus // key: routeID + "/" + backendID
}

// NewRegistry creates a new health state registry.
func NewRegistry() *Registry {
	return &Registry{
		states: make(map[string]*TargetStatus),
	}
}

func stateKey(routeID, backendID string) string {
	return routeID + "/" + backendID
}

// Register initializes a target as healthy by default.
func (r *Registry) Register(routeID, backendID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := stateKey(routeID, backendID)
	if _, exists := r.states[key]; !exists {
		r.states[key] = &TargetStatus{
			RouteID:   routeID,
			BackendID: backendID,
			Healthy:   true, // Optimistically start healthy until probed
		}
	}
}

// IsHealthy returns true if the specified route/backend pair is healthy.
func (r *Registry) IsHealthy(routeID, backendID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	st, exists := r.states[stateKey(routeID, backendID)]
	if !exists {
		return true // If not monitored, assume healthy
	}
	return st.Healthy
}

// RecordProbeResult updates health state with hysteresis based on healthy and unhealthy thresholds.
func (r *Registry) RecordProbeResult(routeID, backendID string, probeSuccess bool, statusCode int, err error, healthyThreshold, unhealthyThreshold int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := stateKey(routeID, backendID)
	st, exists := r.states[key]
	if !exists {
		st = &TargetStatus{
			RouteID:   routeID,
			BackendID: backendID,
			Healthy:   true,
		}
		r.states[key] = st
	}

	st.LastChecked = time.Now()
	st.LastStatusCode = statusCode
	if err != nil {
		st.LastError = err.Error()
	} else {
		st.LastError = ""
	}

	transitionOccurred := false
	if probeSuccess {
		st.ConsecutiveSuccesses++
		st.ConsecutiveFailures = 0

		if !st.Healthy && st.ConsecutiveSuccesses >= healthyThreshold {
			st.Healthy = true
			transitionOccurred = true
		}
	} else {
		st.ConsecutiveFailures++
		st.ConsecutiveSuccesses = 0

		if st.Healthy && st.ConsecutiveFailures >= unhealthyThreshold {
			st.Healthy = false
			transitionOccurred = true
		}
	}

	return transitionOccurred
}

// GetStatus returns the current status snapshot for a target.
func (r *Registry) GetStatus(routeID, backendID string) (TargetStatus, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	st, exists := r.states[stateKey(routeID, backendID)]
	if !exists {
		return TargetStatus{}, false
	}
	return *st, true
}
