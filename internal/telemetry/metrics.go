package telemetry

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics manages all bounded Prometheus telemetry for SentinelProxy.
type Metrics struct {
	Registry *prometheus.Registry

	requestsTotal           *prometheus.CounterVec
	requestDuration         *prometheus.HistogramVec
	upstreamRequestsTotal   *prometheus.CounterVec
	upstreamDuration        *prometheus.HistogramVec
	circuitBreakerState     *prometheus.GaugeVec
	circuitBreakerTrans     *prometheus.CounterVec
	backendHealth           *prometheus.GaugeVec
	rateLimitDecisionsTotal *prometheus.CounterVec
	configReloadTotal       *prometheus.CounterVec
}

var defaultLatencyBuckets = []float64{
	0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0,
}

// NewMetrics initializes Prometheus metrics with bounded label sets.
func NewMetrics(reg *prometheus.Registry) *Metrics {
	if reg == nil {
		reg = prometheus.NewRegistry()
	}

	factory := promauto.With(reg)

	m := &Metrics{
		Registry: reg,
		requestsTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "sentinelproxy_requests_total",
				Help: "Total number of HTTP requests received by SentinelProxy.",
			},
			[]string{"route_id", "method", "status_class"},
		),
		requestDuration: factory.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "sentinelproxy_request_duration_seconds",
				Help:    "Latency distribution of incoming requests in seconds.",
				Buckets: defaultLatencyBuckets,
			},
			[]string{"route_id", "method"},
		),
		upstreamRequestsTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "sentinelproxy_upstream_requests_total",
				Help: "Total number of upstream requests forwarded to backends.",
			},
			[]string{"route_id", "backend_id", "status_class", "outcome"},
		),
		upstreamDuration: factory.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "sentinelproxy_upstream_duration_seconds",
				Help:    "Latency distribution of upstream requests in seconds.",
				Buckets: defaultLatencyBuckets,
			},
			[]string{"route_id", "backend_id"},
		),
		circuitBreakerState: factory.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "sentinelproxy_circuit_breaker_state",
				Help: "Current state of the circuit breaker (0=CLOSED, 1=HALF_OPEN, 2=OPEN).",
			},
			[]string{"route_id", "backend_id"},
		),
		circuitBreakerTrans: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "sentinelproxy_circuit_breaker_transitions_total",
				Help: "Total number of circuit breaker state transitions.",
			},
			[]string{"route_id", "backend_id", "from_state", "to_state"},
		),
		backendHealth: factory.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "sentinelproxy_backend_health",
				Help: "Active health check status of the backend (1=healthy, 0=unhealthy).",
			},
			[]string{"route_id", "backend_id"},
		),
		rateLimitDecisionsTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "sentinelproxy_rate_limit_decisions_total",
				Help: "Total number of rate limiting decisions made.",
			},
			[]string{"route_id", "decision"},
		),
		configReloadTotal: factory.NewCounterVec(
			prometheus.CounterOpts{
				Name: "sentinelproxy_config_reload_total",
				Help: "Total number of configuration reload attempts.",
			},
			[]string{"result"},
		),
	}

	return m
}

// FormatStatusClass buckets numeric status codes into bounded strings.
func FormatStatusClass(statusCode int) string {
	switch {
	case statusCode >= 200 && statusCode < 300:
		return "2xx"
	case statusCode >= 300 && statusCode < 400:
		return "3xx"
	case statusCode >= 400 && statusCode < 500:
		return "4xx"
	case statusCode >= 500 && statusCode < 600:
		return "5xx"
	default:
		return "error"
	}
}

// RecordRequest records inbound gateway request metrics.
func (m *Metrics) RecordRequest(routeID, method string, statusCode int, duration time.Duration) {
	if m == nil {
		return
	}
	sc := FormatStatusClass(statusCode)
	m.requestsTotal.WithLabelValues(routeID, method, sc).Inc()
	m.requestDuration.WithLabelValues(routeID, method).Observe(duration.Seconds())
}

// RecordUpstream records outbound backend request metrics.
func (m *Metrics) RecordUpstream(routeID, backendID, outcome string, statusCode int, duration time.Duration) {
	if m == nil {
		return
	}
	sc := FormatStatusClass(statusCode)
	m.upstreamRequestsTotal.WithLabelValues(routeID, backendID, sc, outcome).Inc()
	m.upstreamDuration.WithLabelValues(routeID, backendID).Observe(duration.Seconds())
}

// SetCircuitBreakerState updates the circuit breaker state gauge.
func (m *Metrics) SetCircuitBreakerState(routeID, backendID, state string) {
	if m == nil {
		return
	}
	var val float64
	switch state {
	case "CLOSED":
		val = 0
	case "HALF_OPEN":
		val = 1
	case "OPEN":
		val = 2
	}
	m.circuitBreakerState.WithLabelValues(routeID, backendID).Set(val)
}

// RecordCircuitBreakerTransition increments the transition counter.
func (m *Metrics) RecordCircuitBreakerTransition(routeID, backendID, from, to string) {
	if m == nil {
		return
	}
	m.circuitBreakerTrans.WithLabelValues(routeID, backendID, from, to).Inc()
}

// SetBackendHealth updates the backend active health gauge.
func (m *Metrics) SetBackendHealth(routeID, backendID string, healthy bool) {
	if m == nil {
		return
	}
	var val float64
	if healthy {
		val = 1
	}
	m.backendHealth.WithLabelValues(routeID, backendID).Set(val)
}

// RecordRateLimitDecision records an allowed or denied rate limit decision.
func (m *Metrics) RecordRateLimitDecision(routeID string, allowed bool) {
	if m == nil {
		return
	}
	decision := "allowed"
	if !allowed {
		decision = "denied"
	}
	m.rateLimitDecisionsTotal.WithLabelValues(routeID, decision).Inc()
}

// RecordConfigReload records a success or failure reload attempt.
func (m *Metrics) RecordConfigReload(success bool) {
	if m == nil {
		return
	}
	res := "success"
	if !success {
		res = "failure"
	}
	m.configReloadTotal.WithLabelValues(res).Inc()
}
