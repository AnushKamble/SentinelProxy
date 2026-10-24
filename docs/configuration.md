# SentinelProxy Configuration Guide

SentinelProxy is configured using a strict YAML document. Every field is strongly typed and strictly validated at boot time and during hot reloads. Unknown fields are rejected.

---

## 1. Minimal Working Configuration

```yaml
server:
  listen_addr: "127.0.0.1:8080"
  read_header_timeout: 5s
  idle_timeout: 60s
  shutdown_timeout: 10s

observability:
  log_level: info
  log_format: json
  metrics_path: /metrics

security:
  request_id_header: X-Request-ID
  trusted_proxy_cidrs:
    - "127.0.0.1/32"

routes:
  - id: api-v1
    path_prefix: /api/v1
    methods: [GET, POST]
    load_balancer: round_robin
    timeout: 2s
    backends:
      - id: backend-primary
        url: http://127.0.0.1:9001
        weight: 1
```

---

## 2. Configuration Sections and Field Reference

### `server` Section
Controls TCP listening, timeouts, and graceful shutdown lifecycle.

| Field | Type | Required | Default | Description / Constraints |
|---|---|---|---|---|
| `listen_addr` | string | **Yes** | — | Host:Port to bind (e.g. `127.0.0.1:8080`, `0.0.0.0:8080`). |
| `read_header_timeout` | duration | No | `5s` | Max time allowed to read HTTP request headers. |
| `idle_timeout` | duration | No | `60s` | Keep-alive connection idle duration before closing. |
| `shutdown_timeout` | duration | No | `10s` | Maximum time allowed for active requests to finish during graceful shutdown. |

---

### `observability` Section
Configures structured logging, metrics endpoints, and OpenTelemetry tracing.

| Field | Type | Required | Default | Description / Constraints |
|---|---|---|---|---|
| `log_level` | string | No | `info` | Allowed: `debug`, `info`, `warn`, `error`. |
| `log_format` | string | No | `json` | Allowed: `json`, `text`. `json` is recommended for production. |
| `metrics_path` | string | **Yes** | `/metrics` | Prometheus metrics scrape path. Must start with `/`. |
| `tracing.enabled` | bool | No | `false` | Enable OpenTelemetry tracing. |
| `tracing.service_name` | string | No | `sentinelproxy` | Service name tag on spans. |
| `tracing.otlp_endpoint` | string | No | `""` | OTLP gRPC/HTTP collector endpoint. If empty, uses stdout/noop tracer. |
| `tracing.sampling_ratio` | float | No | `1.0` | Span sampling ratio between `0.0` and `1.0`. |

---

### `security` Section
Protects gateway and downstream backends against header spoofing and unauthorized access.

| Field | Type | Required | Default | Description / Constraints |
|---|---|---|---|---|
| `request_id_header` | string | No | `X-Request-ID` | Header name for request tracking. Created if missing. |
| `trusted_proxy_cidrs` | []string | No | `[]` | List of trusted upstream CIDRs (e.g. `10.0.0.0/8`, `127.0.0.1/32`). Only proxies in this list are trusted for `X-Forwarded-For`. |
| `jwt.enabled` | bool | No | `false` | Enable JWT authentication globally. |
| `jwt.algorithm` | string | If enabled | `HS256` | Allowed: `HS256`. |
| `jwt.issuer` | string | No | `""` | Expected `iss` claim. |
| `jwt.audience` | string | No | `""` | Expected `aud` claim. |
| `jwt.secret_env` | string | If enabled | — | Name of environment variable containing HMAC secret key (>= 32 bytes). |

---

### `routes` Section
List of route definitions. Each route must have a unique `id` and non-overlapping unique `path_prefix`.

| Field | Type | Required | Default | Description / Constraints |
|---|---|---|---|---|
| `id` | string | **Yes** | — | Unique identifier for the route. |
| `path_prefix` | string | **Yes** | — | Path prefix to match (e.g. `/api/v1/payments`). |
| `methods` | []string | **Yes** | — | Allowed HTTP methods (e.g. `[GET, POST, PUT, DELETE]`). |
| `auth_required` | bool | No | `false` | Enforce JWT verification before routing. |
| `load_balancer` | string | **Yes** | `round_robin` | Allowed: `round_robin`, `least_connections`. |
| `timeout` | duration | **Yes** | `5s` | Maximum time allowed for upstream processing per request. |

---

### `routes[].rate_limit` Section (Per-Route)
Local in-memory token-bucket rate limiter.

| Field | Type | Required | Default | Description / Constraints |
|---|---|---|---|---|
| `enabled` | bool | No | `false` | Enable rate limiting on this route. |
| `requests_per_second` | int | If enabled | — | Token refill rate per second (> 0). |
| `burst` | int | If enabled | — | Maximum bucket capacity (> 0). |

---

### `routes[].circuit_breaker` Section (Per-Route/Backend)
Sliding-window circuit breaker protecting backends.

| Field | Type | Required | Default | Description / Constraints |
|---|---|---|---|---|
| `enabled` | bool | No | `true` | Enable circuit breaker. |
| `window` | duration | No | `10s` | Total duration of the sliding window buffer. |
| `bucket_count` | int | No | `10` | Number of time buckets in the ring buffer. |
| `minimum_requests` | int | No | `10` | Minimum sample count before error rate threshold is evaluated. |
| `failure_rate_threshold`| float | No | `0.5` | Failure fraction `(0.0 to 1.0)` that trips breaker to `OPEN`. |
| `open_duration` | duration | No | `5s` | Cooldown period in `OPEN` before transitioning to `HALF_OPEN`. |
| `half_open_max_probes` | int | No | `2` | Maximum concurrent probe requests allowed in `HALF_OPEN`. |
| `half_open_successes_to_close` | int | No | `2` | Consecutive successful probes required to close circuit. |
| `counted_status_codes` | []int | No | `[500,502,503,504]` | HTTP status codes counted as upstream failures. |

---

### `routes[].health_check` Section (Active Prober)
Non-overlapping background HTTP health checker.

| Field | Type | Required | Default | Description / Constraints |
|---|---|---|---|---|
| `path` | string | If enabled | `/healthz` | Health check endpoint path on backends. |
| `interval` | duration | If enabled | `5s` | Probe interval. |
| `timeout` | duration | If enabled | `1s` | Probe timeout. Must be `< interval`. |
| `healthy_status_codes` | []int | No | `[200]` | Status codes indicating backend health. |
| `unhealthy_threshold` | int | No | `3` | Consecutive failures before marking backend UNHEALTHY. |
| `healthy_threshold` | int | No | `2` | Consecutive successes before marking backend HEALTHY. |

---

### `routes[].backends` Section
Upstream targets for the route.

| Field | Type | Required | Default | Description / Constraints |
|---|---|---|---|---|
| `id` | string | **Yes** | — | Unique backend identifier within the route. |
| `url` | string | **Yes** | — | Target URL (e.g. `http://10.0.0.1:9001`). Credentials not allowed. |
| `weight` | int | No | `1` | Weight for load balancing (>= 1). |

---

## 3. Validation Rules Enforced

The parser rejects configurations with:
- Unknown YAML keys (`StrictDecoder`).
- Empty backend lists for any route.
- Duplicate route IDs or duplicate backend IDs within a route.
- Overlapping identical path prefixes.
- Reserved paths shadow attempts (`/healthz`, `/readyz`, `/metrics`, `/-/status`).
- Health check `timeout >= interval`.
- Breaker failure rate threshold `<= 0` or `> 1.0`.
- Rate limit `burst < 1` or `requests_per_second <= 0`.
- Missing environment variable for `secret_env` when JWT is enabled.
- Backends with embedded user credentials (e.g. `http://user:pass@host`).
