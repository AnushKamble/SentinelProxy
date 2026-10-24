# SentinelProxy Requirements Traceability Matrix

This matrix maps every mandatory requirement from the Technical Specification to implementation code and verification tests.

| Section / ID | Requirement Summary | Implementation File(s) | Verification Test(s) |
|---|---|---|---|
| **Sec 4 & 6: Config** | Strict YAML parsing, no unknown fields where practical, validation of all fields, URLs, durations, algorithms, backends | `internal/config/config.go`, `internal/config/validate.go` | `internal/config/config_test.go` |
| **Sec 6: Snapshot** | Immutable runtime snapshot, atomic pointer exchange, retirement of old resources | `internal/config/snapshot.go` | `internal/config/snapshot_test.go` |
| **Sec 5 & 2: Server** | HTTP server lifecycle, graceful shutdown with bounded deadline, signal handling | `internal/app/app.go`, `cmd/sentinelproxy/main.go` | `tests/integration/lifecycle_test.go` |
| **Sec 5 & 2: Proxy** | Standard Go `httputil.ReverseProxy`, pooled `http.Transport`, timeout/deadline propagation, streaming body support | `internal/proxy/proxy.go`, `internal/proxy/transport.go` | `internal/proxy/proxy_test.go`, `tests/integration/streaming_test.go` |
| **Sec 8: Router** | Deterministic longest-prefix path matching, explicit method list, 404 for unknown path, 405 with `Allow` header | `internal/router/router.go` | `internal/router/router_test.go` |
| **Sec 8: Balancer** | Round-robin and least-connections load balancing, concurrency safety, in-flight accounting lifecycle | `internal/balancer/balancer.go`, `internal/balancer/roundrobin.go`, `internal/balancer/leastconn.go` | `internal/balancer/balancer_test.go` |
| **Sec 9: Circuit Breaker** | CLOSED/OPEN/HALF_OPEN state transitions, fixed ring sliding-time window, minimum sample threshold, explicit failure classifier, fake clock | `internal/circuitbreaker/breaker.go`, `internal/circuitbreaker/window.go`, `internal/circuitbreaker/classifier.go` | `internal/circuitbreaker/breaker_test.go` |
| **Sec 9: Breaker Permits** | `TryAcquire` admission permit API, bounded half-open probe concurrency, terminal outcome reporting, no counter/permit leaks | `internal/circuitbreaker/permit.go` | `internal/circuitbreaker/permit_test.go` |
| **Sec 10: Health Checks** | Active HTTP health checks, independent client, interval/timeout/threshold hysteresis, graceful shutdown, health state registry | `internal/health/checker.go`, `internal/health/registry.go` | `internal/health/checker_test.go` |
| **Sec 11: Rate Limiting** | Local in-memory token bucket, monotonic refill, burst cap, key derivation (JWT sub > IP > trusted proxy CIDR), bounded cache with idle expiry | `internal/ratelimit/limiter.go`, `internal/ratelimit/key.go`, `internal/ratelimit/cache.go` | `internal/ratelimit/limiter_test.go`, `internal/ratelimit/cache_test.go` |
| **Sec 12: Authentication** | Strict JWT verification via maintained library (`golang-jwt/jwt/v5`), fixed algorithm allowlist (HS256), exp/nbf/iss/aud validation | `internal/auth/jwt.go` | `internal/auth/jwt_test.go` |
| **Sec 12: Security** | Header sanitation, X-Request-ID creation/validation, untrusted forward strip, no user-controlled upstream targets, secret redaction | `internal/middleware/security.go`, `internal/middleware/request_id.go` | `tests/security/security_test.go` |
| **Sec 13: Tracing** | OpenTelemetry Go SDK, W3C Trace Context propagation (`traceparent`), gateway server span, outbound client span | `internal/telemetry/tracing.go` | `internal/telemetry/tracing_test.go`, `tests/integration/trace_test.go` |
| **Sec 13: Metrics** | Prometheus metrics with bounded label cardinality (no arbitrary URLs, user IDs, or unconstrained error strings) | `internal/telemetry/metrics.go` | `internal/telemetry/metrics_test.go` |
| **Sec 13: Logging** | JSON structured logging via `log/slog` with request ID and trace ID correlation, secret redaction | `internal/telemetry/logging.go` | `internal/telemetry/logging_test.go` |
| **Sec 14: Hot Reload** | Atomic config reload, fsnotify/polling watcher with debounce, validation before swap, fallback to last-known-good, state continuity | `internal/config/watcher.go`, `internal/config/reload.go` | `tests/integration/reload_test.go` |
| **Sec 15: Endpoints** | Built-in `/healthz`, `/readyz`, `/metrics`, and protected `/-/status`, protected from custom route shadowing | `internal/admin/handlers.go` | `internal/admin/handlers_test.go` |
| **Sec 16: Mock Backend** | Controllable mock backend with deterministic fault injection (delay, status errors, hangs, health flap, reset) | `cmd/mock-backend/main.go`, `internal/mock/backend.go` | `cmd/mock-backend/main_test.go` |
| **Sec 16: Scenarios** | 10 required end-to-end resilience scenarios (breaker fast fail, isolation, recovery, timeouts, reload safety) | `tests/integration/scenarios_test.go` | `tests/integration/scenarios_test.go` |
| **Sec 17: Benchmarks** | Go workload generator (`cmd/loadgen`), repeatable runner, raw CSV/JSON outputs and calculated summaries in `results/` | `cmd/loadgen/main.go`, `bench/runner.go` | Execution outputs in `results/` |
| **Sec 18 & 21: Quality** | Race detection (`go test -race`), static analysis (`go vet`), deterministic tests, no fake tests | Codebase-wide | `go test -race ./...`, `go vet ./...` |
| **Sec 12 & 20: Deploy** | Docker Compose and native multi-process runner scripts | `deploy/docker-compose.yml`, `scripts/` | Script execution tests |
| **Sec 13 & 20: Docs** | Architecture, configuration, threat model, failure semantics, benchmark protocol, quick demo | `README.md`, `docs/*.md` | Document review |
