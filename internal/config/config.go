package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// ServerConfig defines gateway HTTP server options.
type ServerConfig struct {
	ListenAddr        string        `yaml:"listen_addr"`
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	IdleTimeout       time.Duration `yaml:"idle_timeout"`
	ShutdownTimeout   time.Duration `yaml:"shutdown_timeout"`
}

// TracingConfig defines OpenTelemetry tracing options.
type TracingConfig struct {
	Enabled       bool    `yaml:"enabled"`
	ServiceName   string  `yaml:"service_name"`
	OTLPEndpoint  string  `yaml:"otlp_endpoint"`
	SamplingRatio float64 `yaml:"sampling_ratio"`
}

// ObservabilityConfig defines logging, metrics, and tracing settings.
type ObservabilityConfig struct {
	LogLevel    string        `yaml:"log_level"`
	LogFormat   string        `yaml:"log_format"`
	MetricsPath string        `yaml:"metrics_path"`
	Tracing     TracingConfig `yaml:"tracing"`
}

// JWTConfig defines route-level JWT validation parameters.
type JWTConfig struct {
	Enabled   bool   `yaml:"enabled"`
	Algorithm string `yaml:"algorithm"`
	Issuer    string `yaml:"issuer"`
	Audience  string `yaml:"audience"`
	SecretEnv string `yaml:"secret_env"`
}

// SecurityConfig defines edge security and authentication settings.
type SecurityConfig struct {
	RequestIDHeader   string    `yaml:"request_id_header"`
	TrustedProxyCIDRs []string  `yaml:"trusted_proxy_cidrs"`
	JWT               JWTConfig `yaml:"jwt"`
}

// RateLimitConfig defines local token-bucket rate limiter settings.
type RateLimitConfig struct {
	Enabled           bool    `yaml:"enabled"`
	RequestsPerSecond float64 `yaml:"requests_per_second"`
	Burst             int     `yaml:"burst"`
}

// CircuitBreakerConfig defines per-route/backend breaker policies.
type CircuitBreakerConfig struct {
	Enabled                  bool          `yaml:"enabled"`
	Window                   time.Duration `yaml:"window"`
	BucketCount              int           `yaml:"bucket_count"`
	MinimumRequests          int           `yaml:"minimum_requests"`
	FailureRateThreshold     float64       `yaml:"failure_rate_threshold"`
	OpenDuration             time.Duration `yaml:"open_duration"`
	HalfOpenMaxProbes        int           `yaml:"half_open_max_probes"`
	HalfOpenSuccessesToClose int           `yaml:"half_open_successes_to_close"`
	CountedStatusCodes       []int         `yaml:"counted_status_codes"`
}

// HealthCheckConfig defines active health probing parameters.
type HealthCheckConfig struct {
	Path               string        `yaml:"path"`
	Interval           time.Duration `yaml:"interval"`
	Timeout            time.Duration `yaml:"timeout"`
	HealthyStatusCodes []int         `yaml:"healthy_status_codes"`
	UnhealthyThreshold int           `yaml:"unhealthy_threshold"`
	HealthyThreshold   int           `yaml:"healthy_threshold"`
}

// BackendConfig defines an individual upstream target.
type BackendConfig struct {
	ID     string `yaml:"id"`
	URL    string `yaml:"url"`
	Weight int    `yaml:"weight"`
}

// RouteConfig defines routing rules and policies for a path prefix.
type RouteConfig struct {
	ID             string               `yaml:"id"`
	PathPrefix     string               `yaml:"path_prefix"`
	Methods        []string             `yaml:"methods"`
	AuthRequired   bool                 `yaml:"auth_required"`
	LoadBalancer   string               `yaml:"load_balancer"`
	Timeout        time.Duration        `yaml:"timeout"`
	RateLimit      RateLimitConfig      `yaml:"rate_limit"`
	CircuitBreaker CircuitBreakerConfig `yaml:"circuit_breaker"`
	HealthCheck    HealthCheckConfig    `yaml:"health_check"`
	Backends       []BackendConfig      `yaml:"backends"`
}

// Config is the root configuration object for SentinelProxy.
type Config struct {
	Server        ServerConfig        `yaml:"server"`
	Observability ObservabilityConfig `yaml:"observability"`
	Security      SecurityConfig      `yaml:"security"`
	Routes        []RouteConfig       `yaml:"routes"`
}

// LoadFromBytes strictly parses and validates YAML configuration bytes.
func LoadFromBytes(data []byte) (*Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // Reject unknown fields

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("configuration is empty")
		}
		return nil, fmt.Errorf("yaml parse error: %w", err)
	}

	// Check if there are trailing documents
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("multiple YAML documents not permitted")
	}

	if err := Validate(&cfg); err != nil {
		return nil, fmt.Errorf("configuration validation error: %w", err)
	}

	return &cfg, nil
}

// LoadFromFile reads and validates configuration from a file path.
func LoadFromFile(filePath string) (*Config, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %q: %w", filePath, err)
	}
	return LoadFromBytes(data)
}
