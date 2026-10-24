package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

var (
	validLogLevels = map[string]bool{
		"debug": true,
		"info":  true,
		"warn":  true,
		"error": true,
	}

	validLogFormats = map[string]bool{
		"json": true,
		"text": true,
	}

	validHTTPMethods = map[string]bool{
		"GET":     true,
		"POST":    true,
		"PUT":     true,
		"DELETE":  true,
		"PATCH":   true,
		"HEAD":    true,
		"OPTIONS": true,
	}

	validLoadBalancers = map[string]bool{
		"round_robin":       true,
		"least_connections": true,
	}

	validJWTAlgorithms = map[string]bool{
		"HS256": true,
	}

	reservedPaths = map[string]bool{
		"/healthz":  true,
		"/readyz":   true,
		"/metrics":  true,
		"/-/status": true,
	}
)

// Validate checks the configuration for correctness, returning detailed errors.
func Validate(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	if err := validateServer(&cfg.Server); err != nil {
		return fmt.Errorf("server: %w", err)
	}

	if err := validateObservability(&cfg.Observability); err != nil {
		return fmt.Errorf("observability: %w", err)
	}

	if err := validateSecurity(&cfg.Security, cfg.Routes); err != nil {
		return fmt.Errorf("security: %w", err)
	}

	if err := validateRoutes(cfg.Routes); err != nil {
		return fmt.Errorf("routes: %w", err)
	}

	return nil
}

func validateServer(s *ServerConfig) error {
	if strings.TrimSpace(s.ListenAddr) == "" {
		return fmt.Errorf("listen_addr must not be empty")
	}
	if s.ReadHeaderTimeout <= 0 {
		return fmt.Errorf("read_header_timeout must be positive")
	}
	if s.IdleTimeout <= 0 {
		return fmt.Errorf("idle_timeout must be positive")
	}
	if s.ShutdownTimeout <= 0 {
		return fmt.Errorf("shutdown_timeout must be positive")
	}
	return nil
}

func validateObservability(o *ObservabilityConfig) error {
	level := strings.ToLower(strings.TrimSpace(o.LogLevel))
	if !validLogLevels[level] {
		return fmt.Errorf("invalid log_level %q (must be debug, info, warn, error)", o.LogLevel)
	}

	format := strings.ToLower(strings.TrimSpace(o.LogFormat))
	if !validLogFormats[format] {
		return fmt.Errorf("invalid log_format %q (must be json or text)", o.LogFormat)
	}

	if !strings.HasPrefix(o.MetricsPath, "/") {
		return fmt.Errorf("metrics_path must start with '/' (got %q)", o.MetricsPath)
	}

	if o.Tracing.Enabled {
		if strings.TrimSpace(o.Tracing.ServiceName) == "" {
			return fmt.Errorf("tracing.service_name must not be empty when tracing is enabled")
		}
		if o.Tracing.SamplingRatio < 0.0 || o.Tracing.SamplingRatio > 1.0 {
			return fmt.Errorf("tracing.sampling_ratio must be between 0.0 and 1.0 (got %f)", o.Tracing.SamplingRatio)
		}
	}
	return nil
}

func validateSecurity(sec *SecurityConfig, routes []RouteConfig) error {
	if strings.TrimSpace(sec.RequestIDHeader) == "" {
		return fmt.Errorf("request_id_header must not be empty")
	}

	for _, cidr := range sec.TrustedProxyCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("invalid trusted_proxy_cidr %q: %w", cidr, err)
		}
	}

	// Check if any route requires auth
	authRequired := sec.JWT.Enabled
	for _, r := range routes {
		if r.AuthRequired {
			authRequired = true
			break
		}
	}

	if authRequired {
		if !validJWTAlgorithms[sec.JWT.Algorithm] {
			return fmt.Errorf("jwt.algorithm %q is unsupported (allowlist: HS256)", sec.JWT.Algorithm)
		}
		if strings.TrimSpace(sec.JWT.Issuer) == "" {
			return fmt.Errorf("jwt.issuer must not be empty when authentication is required")
		}
		if strings.TrimSpace(sec.JWT.Audience) == "" {
			return fmt.Errorf("jwt.audience must not be empty when authentication is required")
		}
		if strings.TrimSpace(sec.JWT.SecretEnv) == "" {
			return fmt.Errorf("jwt.secret_env must not be empty when authentication is required")
		}
	}

	return nil
}

func validateRoutes(routes []RouteConfig) error {
	if len(routes) == 0 {
		return fmt.Errorf("at least one route must be configured")
	}

	seenRouteIDs := make(map[string]bool)
	seenPathPrefixes := make(map[string]bool)

	for i, r := range routes {
		if strings.TrimSpace(r.ID) == "" {
			return fmt.Errorf("route[%d]: id must not be empty", i)
		}
		if seenRouteIDs[r.ID] {
			return fmt.Errorf("route[%d]: duplicate route id %q", i, r.ID)
		}
		seenRouteIDs[r.ID] = true

		if !strings.HasPrefix(r.PathPrefix, "/") {
			return fmt.Errorf("route %q: path_prefix must start with '/' (got %q)", r.ID, r.PathPrefix)
		}
		if reservedPaths[r.PathPrefix] {
			return fmt.Errorf("route %q: path_prefix %q conflicts with reserved system path", r.ID, r.PathPrefix)
		}
		if seenPathPrefixes[r.PathPrefix] {
			return fmt.Errorf("route %q: duplicate path_prefix %q", r.ID, r.PathPrefix)
		}
		seenPathPrefixes[r.PathPrefix] = true

		if len(r.Methods) == 0 {
			return fmt.Errorf("route %q: methods list must not be empty", r.ID)
		}
		seenMethods := make(map[string]bool)
		for _, m := range r.Methods {
			if !validHTTPMethods[m] {
				return fmt.Errorf("route %q: unsupported HTTP method %q", r.ID, m)
			}
			if seenMethods[m] {
				return fmt.Errorf("route %q: duplicate method %q", r.ID, m)
			}
			seenMethods[m] = true
		}

		if !validLoadBalancers[r.LoadBalancer] {
			return fmt.Errorf("route %q: invalid load_balancer %q (must be round_robin or least_connections)", r.ID, r.LoadBalancer)
		}

		if r.Timeout <= 0 {
			return fmt.Errorf("route %q: timeout must be positive", r.ID)
		}

		if r.RateLimit.Enabled {
			if r.RateLimit.RequestsPerSecond <= 0 {
				return fmt.Errorf("route %q: rate_limit.requests_per_second must be positive", r.ID)
			}
			if r.RateLimit.Burst < 1 {
				return fmt.Errorf("route %q: rate_limit.burst must be >= 1", r.ID)
			}
		}

		if r.CircuitBreaker.Enabled {
			cb := r.CircuitBreaker
			if cb.Window <= 0 {
				return fmt.Errorf("route %q: circuit_breaker.window must be positive", r.ID)
			}
			if cb.BucketCount < 1 {
				return fmt.Errorf("route %q: circuit_breaker.bucket_count must be >= 1", r.ID)
			}
			if cb.Window/time.Duration(cb.BucketCount) < 10*time.Millisecond {
				return fmt.Errorf("route %q: circuit_breaker bucket duration (window/bucket_count) too small (< 10ms)", r.ID)
			}
			if cb.MinimumRequests < 1 {
				return fmt.Errorf("route %q: circuit_breaker.minimum_requests must be >= 1", r.ID)
			}
			if cb.FailureRateThreshold <= 0.0 || cb.FailureRateThreshold > 1.0 {
				return fmt.Errorf("route %q: circuit_breaker.failure_rate_threshold must be in range (0.0, 1.0]", r.ID)
			}
			if cb.OpenDuration <= 0 {
				return fmt.Errorf("route %q: circuit_breaker.open_duration must be positive", r.ID)
			}
			if cb.HalfOpenMaxProbes < 1 {
				return fmt.Errorf("route %q: circuit_breaker.half_open_max_probes must be >= 1", r.ID)
			}
			if cb.HalfOpenSuccessesToClose < 1 {
				return fmt.Errorf("route %q: circuit_breaker.half_open_successes_to_close must be >= 1", r.ID)
			}
			if len(cb.CountedStatusCodes) == 0 {
				return fmt.Errorf("route %q: circuit_breaker.counted_status_codes must not be empty", r.ID)
			}
			for _, sc := range cb.CountedStatusCodes {
				if sc < 100 || sc > 599 {
					return fmt.Errorf("route %q: circuit_breaker.counted_status_codes contains invalid HTTP status %d", r.ID, sc)
				}
			}
		}

		hc := r.HealthCheck
		if strings.TrimSpace(hc.Path) != "" {
			if !strings.HasPrefix(hc.Path, "/") {
				return fmt.Errorf("route %q: health_check.path must start with '/'", r.ID)
			}
			if hc.Interval <= 0 {
				return fmt.Errorf("route %q: health_check.interval must be positive", r.ID)
			}
			if hc.Timeout <= 0 {
				return fmt.Errorf("route %q: health_check.timeout must be positive", r.ID)
			}
			if hc.Timeout > hc.Interval {
				return fmt.Errorf("route %q: health_check.timeout (%v) cannot exceed interval (%v)", r.ID, hc.Timeout, hc.Interval)
			}
			if hc.UnhealthyThreshold < 1 {
				return fmt.Errorf("route %q: health_check.unhealthy_threshold must be >= 1", r.ID)
			}
			if hc.HealthyThreshold < 1 {
				return fmt.Errorf("route %q: health_check.healthy_threshold must be >= 1", r.ID)
			}
			if len(hc.HealthyStatusCodes) == 0 {
				return fmt.Errorf("route %q: health_check.healthy_status_codes must not be empty", r.ID)
			}
			for _, code := range hc.HealthyStatusCodes {
				if code < 100 || code > 599 {
					return fmt.Errorf("route %q: health_check.healthy_status_codes contains invalid HTTP status %d", r.ID, code)
				}
			}
		}

		if len(r.Backends) == 0 {
			return fmt.Errorf("route %q: at least one backend is required", r.ID)
		}

		seenBackendIDs := make(map[string]bool)
		for j, b := range r.Backends {
			if strings.TrimSpace(b.ID) == "" {
				return fmt.Errorf("route %q backend[%d]: id must not be empty", r.ID, j)
			}
			if seenBackendIDs[b.ID] {
				return fmt.Errorf("route %q: duplicate backend id %q", r.ID, b.ID)
			}
			seenBackendIDs[b.ID] = true

			u, err := url.Parse(b.URL)
			if err != nil {
				return fmt.Errorf("route %q backend %q: invalid url %q: %w", r.ID, b.ID, b.URL, err)
			}
			if u.Scheme != "http" && u.Scheme != "https" {
				return fmt.Errorf("route %q backend %q: scheme must be http or https (got %q)", r.ID, b.ID, u.Scheme)
			}
			if u.Host == "" {
				return fmt.Errorf("route %q backend %q: url host must not be empty", r.ID, b.ID)
			}
			if u.User != nil {
				return fmt.Errorf("route %q backend %q: url must not contain user credentials", r.ID, b.ID)
			}
			if b.Weight < 1 {
				return fmt.Errorf("route %q backend %q: weight must be >= 1", r.ID, b.ID)
			}
		}
	}

	return nil
}
