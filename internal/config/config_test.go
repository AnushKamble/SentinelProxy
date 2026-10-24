package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/sentinelproxy/sentinelproxy/internal/config"
)

const validYAML = `
server:
  listen_addr: "127.0.0.1:8080"
  read_header_timeout: 5s
  idle_timeout: 60s
  shutdown_timeout: 10s

observability:
  log_level: info
  log_format: json
  metrics_path: /metrics
  tracing:
    enabled: true
    service_name: sentinelproxy
    otlp_endpoint: ""
    sampling_ratio: 1.0

security:
  request_id_header: X-Request-ID
  trusted_proxy_cidrs:
    - "127.0.0.1/32"
  jwt:
    enabled: true
    algorithm: HS256
    issuer: "test-issuer"
    audience: "test-aud"
    secret_env: "SENTINELPROXY_JWT_SECRET"

routes:
  - id: test-route
    path_prefix: /api/v1/test
    methods: [GET, POST]
    auth_required: true
    load_balancer: round_robin
    timeout: 3s
    rate_limit:
      enabled: true
      requests_per_second: 50
      burst: 100
    circuit_breaker:
      enabled: true
      window: 10s
      bucket_count: 10
      minimum_requests: 10
      failure_rate_threshold: 0.5
      open_duration: 5s
      half_open_max_probes: 2
      half_open_successes_to_close: 2
      counted_status_codes: [500, 502, 503, 504]
    health_check:
      path: /healthz
      interval: 2s
      timeout: 500ms
      healthy_status_codes: [200]
      unhealthy_threshold: 3
      healthy_threshold: 2
    backends:
      - id: backend-1
        url: http://127.0.0.1:9001
        weight: 1
      - id: backend-2
        url: http://127.0.0.1:9002
        weight: 2
`

func TestLoadFromBytes_ValidConfig(t *testing.T) {
	cfg, err := config.LoadFromBytes([]byte(validYAML))
	if err != nil {
		t.Fatalf("expected valid config to load, got error: %v", err)
	}

	if cfg.Server.ListenAddr != "127.0.0.1:8080" {
		t.Errorf("expected listen_addr 127.0.0.1:8080, got %s", cfg.Server.ListenAddr)
	}
	if len(cfg.Routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(cfg.Routes))
	}
	r := cfg.Routes[0]
	if r.ID != "test-route" {
		t.Errorf("expected route id test-route, got %s", r.ID)
	}
	if len(r.Backends) != 2 {
		t.Errorf("expected 2 backends, got %d", len(r.Backends))
	}
}

func TestLoadFromFile_ExampleFile(t *testing.T) {
	cfg, err := config.LoadFromFile("../../configs/routes.example.yaml")
	if err != nil {
		t.Fatalf("failed to load routes.example.yaml: %v", err)
	}
	if len(cfg.Routes) != 2 {
		t.Errorf("expected 2 routes in example, got %d", len(cfg.Routes))
	}
}

func TestValidation_Rejections(t *testing.T) {
	tests := []struct {
		name        string
		mutator     func(string) string
		errContains string
	}{
		{
			name: "empty YAML",
			mutator: func(_ string) string {
				return ""
			},
			errContains: "empty",
		},
		{
			name: "unknown field rejected (strict schema)",
			mutator: func(y string) string {
				return y + "\nunexpected_top_field: true\n"
			},
			errContains: "field unexpected_top_field not found",
		},
		{
			name: "empty listen addr",
			mutator: func(y string) string {
				return strings.Replace(y, `listen_addr: "127.0.0.1:8080"`, `listen_addr: ""`, 1)
			},
			errContains: "listen_addr must not be empty",
		},
		{
			name: "invalid log level",
			mutator: func(y string) string {
				return strings.Replace(y, `log_level: info`, `log_level: invalid_level`, 1)
			},
			errContains: "invalid log_level",
		},
		{
			name: "invalid metrics path without leading slash",
			mutator: func(y string) string {
				return strings.Replace(y, `metrics_path: /metrics`, `metrics_path: metrics`, 1)
			},
			errContains: "metrics_path must start with '/'",
		},
		{
			name: "invalid trusted CIDR",
			mutator: func(y string) string {
				return strings.Replace(y, `"127.0.0.1/32"`, `"not-a-cidr"`, 1)
			},
			errContains: "invalid trusted_proxy_cidr",
		},
		{
			name: "unsupported JWT algorithm",
			mutator: func(y string) string {
				return strings.Replace(y, `algorithm: HS256`, `algorithm: RS256`, 1)
			},
			errContains: "unsupported (allowlist: HS256)",
		},
		{
			name: "empty JWT secret env when auth required",
			mutator: func(y string) string {
				return strings.Replace(y, `secret_env: "SENTINELPROXY_JWT_SECRET"`, `secret_env: ""`, 1)
			},
			errContains: "jwt.secret_env must not be empty",
		},
		{
			name: "duplicate route IDs",
			mutator: func(y string) string {
				return y + `
  - id: test-route
    path_prefix: /api/v2/other
    methods: [GET]
    load_balancer: round_robin
    timeout: 1s
    backends:
      - id: b1
        url: http://127.0.0.1:9003
        weight: 1
`
			},
			errContains: "duplicate route id",
		},
		{
			name: "duplicate path prefix",
			mutator: func(y string) string {
				return y + `
  - id: other-route
    path_prefix: /api/v1/test
    methods: [GET]
    load_balancer: round_robin
    timeout: 1s
    backends:
      - id: b1
        url: http://127.0.0.1:9003
        weight: 1
`
			},
			errContains: "duplicate path_prefix",
		},
		{
			name: "reserved path conflict",
			mutator: func(y string) string {
				return strings.Replace(y, `path_prefix: /api/v1/test`, `path_prefix: /healthz`, 1)
			},
			errContains: "conflicts with reserved system path",
		},
		{
			name: "unsupported HTTP method",
			mutator: func(y string) string {
				return strings.Replace(y, `methods: [GET, POST]`, `methods: [GET, INVALID_VERB]`, 1)
			},
			errContains: "unsupported HTTP method",
		},
		{
			name: "empty methods list",
			mutator: func(y string) string {
				return strings.Replace(y, `methods: [GET, POST]`, `methods: []`, 1)
			},
			errContains: "methods list must not be empty",
		},
		{
			name: "invalid load balancer algorithm",
			mutator: func(y string) string {
				return strings.Replace(y, `load_balancer: round_robin`, `load_balancer: random_walk`, 1)
			},
			errContains: "invalid load_balancer",
		},
		{
			name: "negative route timeout",
			mutator: func(y string) string {
				return strings.Replace(y, `timeout: 3s`, `timeout: -1s`, 1)
			},
			errContains: "timeout must be positive",
		},
		{
			name: "invalid rate limit burst < 1",
			mutator: func(y string) string {
				return strings.Replace(y, `burst: 100`, `burst: 0`, 1)
			},
			errContains: "burst must be >= 1",
		},
		{
			name: "impossible circuit breaker failure rate threshold > 1.0",
			mutator: func(y string) string {
				return strings.Replace(y, `failure_rate_threshold: 0.5`, `failure_rate_threshold: 1.5`, 1)
			},
			errContains: "failure_rate_threshold must be in range",
		},
		{
			name: "circuit breaker bucket duration too small",
			mutator: func(y string) string {
				return strings.Replace(y, "window: 10s\n      bucket_count: 10", "window: 10ms\n      bucket_count: 10", 1)
			},
			errContains: "bucket duration",
		},
		{
			name: "health check timeout exceeds interval",
			mutator: func(y string) string {
				return strings.Replace(y, `timeout: 500ms`, `timeout: 5s`, 1)
			},
			errContains: "cannot exceed interval",
		},
		{
			name: "backend URL with user credentials rejected",
			mutator: func(y string) string {
				return strings.Replace(y, `url: http://127.0.0.1:9001`, `url: http://user:pass@127.0.0.1:9001`, 1)
			},
			errContains: "user credentials",
		},
		{
			name: "empty backends list rejected",
			mutator: func(y string) string {
				idx := strings.Index(y, "backends:")
				return y[:idx] + "backends: []\n"
			},
			errContains: "at least one backend is required",
		},
		{
			name: "duplicate backend IDs within route rejected",
			mutator: func(y string) string {
				return strings.Replace(y, `id: backend-2`, `id: backend-1`, 1)
			},
			errContains: "duplicate backend id",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := tc.mutator(validYAML)
			_, err := config.LoadFromBytes([]byte(input))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.errContains)
			}
			if !strings.Contains(err.Error(), tc.errContains) {
				t.Errorf("expected error containing %q, got %q", tc.errContains, err.Error())
			}
		})
	}
}

func TestMain(m *testing.M) {
	// Ensure required env vars are clean
	os.Exit(m.Run())
}
