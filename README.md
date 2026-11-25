# SentinelProxy

> **A high-concurrency, fault-isolating API gateway written in Go.**  
> Featuring deterministic longest-prefix routing, sliding-window circuit breakers with admission permits, local token-bucket rate limiting, strict HMAC-SHA256 JWT validation, OpenTelemetry/W3C distributed tracing, Prometheus observability, and zero-downtime hot configuration reloads.

[![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat&logo=go)](https://golang.org)
[![CI](https://github.com/AnushKamble/SentinelProxy/actions/workflows/ci.yml/badge.svg)](https://github.com/AnushKamble/SentinelProxy/actions/workflows/ci.yml)
[![Tests](https://img.shields.io/badge/Tests-100%25%20Passing-brightgreen)](https://github.com/AnushKamble/SentinelProxy)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

---

## Architecture Overview

```mermaid
flowchart TD
    Client["Client Request"] --> Gateway["SentinelProxy HTTP Server"]
    
    subgraph InboundPipeline ["Inbound Middleware & Admission Pipeline"]
        Gateway --> SecHdr["Security Headers & Request-ID Validation"]
        SecHdr --> OTel["OpenTelemetry Server Span Injection"]
        OTel --> Router["Longest-Prefix Route Matching"]
        Router --> Auth["JWT HS256 Authentication"]
        Auth --> RateLimit["Local Token Bucket Rate Limiter"]
    end

    subgraph LoadBalancerBreaker ["Backend Selection & Fault Isolation"]
        RateLimit --> Balancer["Load Balancer (Round-Robin / Least-Conn)"]
        Balancer --> Eligibility{"Backend Eligibility Check"}
        Eligibility -->|Breaker OPEN / Unhealthy| Skip["Exclude Candidate"]
        Eligibility -->|Eligible| Permit["Acquire Breaker Permit"]
        Permit -->|Capacity Exceeded| FastFail["Fast 503 Rejection (< 1ms)"]
    end

    subgraph OutboundReverseProxy ["Outbound Execution & Observation"]
        Permit --> RevProxy["httputil.ReverseProxy (Pooled Transport)"]
        RevProxy --> W3C["W3C Trace Context (traceparent)"]
        W3C --> Upstream["Upstream Backend Process"]
        Upstream --> Observer["Proxy Observer (Status / Error / Latency)"]
        Observer --> RecordOutcome["Record Permit Outcome (Success / Failure / Timeout)"]
        RecordOutcome --> Metrics["Prometheus & slog Structured Log"]
    end

    FastFail --> Response["HTTP Response to Client"]
    Observer --> Response
```

---

## Core Capabilities

- **Real Reverse Proxying:** Powered by Go's battle-tested `net/http/httputil.ReverseProxy` with pooled keep-alive transports, aggressive connection reuse, and response streaming.
- **Sliding-Window Circuit Breakers:** Per-route/backend sliding time windows (10-bucket ring buffer) with explicit `TryAcquire` admission permits. Fast-fails broken upstreams with `503 Service Unavailable` in under 1ms, preserving timeout budgets and preventing thread starvation.
- **Failover & Least-Connections Balancing:** Concurrency-safe load balancing with real-time in-flight tracking. If one backend fails, traffic immediately and transparently diverts 100% to surviving healthy candidates without client downtime.
- **Monotonic Token-Bucket Rate Limiter:** High-throughput in-memory token bucket evaluated in **52 ns/op (0 allocations)**. Extracts client identity hierarchically: authenticated JWT `sub` > verified `X-Forwarded-For` (via trusted CIDR check) > peer IP.
- **Strict JWT Verification:** HMAC-SHA256 verification using `golang-jwt/jwt/v5`. Enforces cryptographic algorithm allowlists (rejects `none` and asymmetric confusion attacks) and validates `exp`, `nbf`, `iss`, and `aud` claims.
- **OpenTelemetry & W3C Distributed Tracing:** Injects W3C `traceparent` headers into upstream requests, correlating end-to-end distributed traces across microservices. Emits JSON logs correlating `request_id` and `trace_id`.
- **Prometheus Observability:** Native Prometheus exporter with strict bounded label cardinality (`route_id`, `method`, `status_class`) to prevent memory-exhaustion attacks.
- **Zero-Downtime Hot Reloads:** Atomic pointer swapping (`atomic.Pointer[RuntimeSnapshot]`) with candidate configuration validation. If an invalid configuration is introduced, it is safely rejected while retaining the last-known-good state and existing circuit breaker history.

---

## Measured Benchmark Results

All benchmarks were empirically measured on an AMD Ryzen 9 8940HX (32 cores) running Go 1.27.0 on Windows 11. Results represent **5 repeated runs per condition**:

| Condition | Median Throughput | p50 Latency | p95 Latency | p99 Latency | Success Rate | Operational Significance |
|---|---|---|---|---|---|
| **A: Healthy Baseline** | **5,517.94 req/s** | **1.25 ms** | **2.38 ms** | **4.10 ms** | **100.00%** | Full-pipeline baseline under normal operation |
| **B: Degraded (Breaker Disabled)** | **156.54 req/s** | **50.94 ms** | **51.75 ms** | **52.60 ms** | **0.00%** | Workers starved waiting on 50ms upstream delay |
| **C: Degraded (Breaker Enabled)** | **27,098.53 req/s** | **0.00 ms** | **1.01 ms** | **1.89 ms** | **0.00%** | **173x throughput gain** via sub-millisecond fast-fail |
| **D: Isolated Route Protection** | **6,236.05 req/s** | **1.13 ms** | **2.44 ms** | **4.21 ms** | **100.00%** | Zero blast radius: sibling route remains 100% healthy |

### Microbenchmarks (Go `testing.B`)

```
BenchmarkRouterMatch-32                  7,202,815    167.3 ns/op     0 B/op    0 allocs/op
BenchmarkCircuitBreakerAcquireRelease-32 10,867,989   108.5 ns/op    48 B/op    1 allocs/op
BenchmarkRateLimiterAllow-32             25,910,427    52.34 ns/op     0 B/op    0 allocs/op
```

---

## Quick Start Guide

### Option 1: Native One-Command Live Demo (Recommended)

Run the self-contained native multi-process demonstration script (starts Gateway, 2 mock backends, demonstrates load balancing, injects faults, demonstrates circuit breaker failover, recovers, and inspects live Prometheus metrics):

```powershell
# Windows (PowerShell)
.\scripts\run_demo.ps1
```

```bash
# Linux / macOS (Bash)
chmod +x ./scripts/*.sh
./scripts/run_demo.sh
```

### Option 2: Docker Compose

Spin up SentinelProxy, two mock backend instances, and Prometheus:

```bash
docker compose -f deploy/docker-compose.yml up --build
```

- **Gateway Endpoint:** `http://localhost:8080/api/echo`
- **Prometheus UI:** `http://localhost:9090`
- **Gateway Health:** `http://localhost:8080/healthz`
- **Metrics Endpoint:** `http://localhost:8080/metrics`

---

## Fault Injection and Resilience Scenarios

SentinelProxy includes 10 fully automated end-to-end integration scenarios verifying all fault modes (`tests/integration/scenarios_test.go`):

1. **Healthy Baseline:** 200 OK across multiple routes and backends.
2. **Breaker Trip on 5xx:** 100% 500 error trips breaker to OPEN at exact threshold.
3. **Slow Backend Timeout:** Gateway timeout budget enforced; breaker trips on timeouts.
4. **Backend Crash (Connection Refused):** Connection resets return 502, tripping breaker.
5. **Failover to Surviving Backend:** Failing backend excluded; surviving backend handles 100% of traffic.
6. **Fast 503 Budget Preservation:** Broken route returns fast 503 in <1ms without burning timeout budgets.
7. **Blast Radius Isolation:** Outage in Route A does not affect Route B.
8. **Half-Open Self-Healing:** Backend recovery followed by 2 successful probe requests restores breaker to CLOSED.
9. **Last-Known-Good Reload Safety:** Malformed config reload rejected; existing traffic continues uninterrupted.
10. **Rate Limit & Auth Isolation:** 401 Unauthorized and 429 Rate Limited responses never count as upstream failures.

Execute the scenario verification suite:

```bash
go test -v ./tests/integration -run TestScenario
```

---

## Project Structure

```
SentinelProxy/
â”œâ”€â”€ bench/                  # High-throughput workload generator & benchmark suite
â”œâ”€â”€ cmd/
â”‚   â”œâ”€â”€ loadgen/            # CLI load generation tool
â”‚   â”œâ”€â”€ mock-backend/       # Controllable mock backend with fault injection
â”‚   â””â”€â”€ sentinelproxy/      # Gateway entrypoint
â”œâ”€â”€ configs/                # Strict YAML configuration templates
â”œâ”€â”€ deploy/                 # Dockerfile, Docker Compose & Prometheus setup
â”œâ”€â”€ docs/                   # Architecture, threat model, failure semantics, limitations
â”‚   â”œâ”€â”€ decisions/          # Architecture Decision Records (ADRs 0001-0006)
â”‚   â”œâ”€â”€ architecture.md     # Deep-dive system architecture
â”‚   â”œâ”€â”€ configuration.md    # YAML configuration contract & reference
â”‚   â”œâ”€â”€ threat-model.md     # Security threat model & mitigation strategies
â”‚   â”œâ”€â”€ failure-semantics.md# Status codes, error mapping & state transitions
â”‚   â”œâ”€â”€ benchmark-protocol.md# Benchmark methodology & empirical data
â”‚   â””â”€â”€ limitations.md      # Engineering trade-offs & future scope
â”œâ”€â”€ internal/
â”‚   â”œâ”€â”€ admin/              # Health & admin handlers (/healthz, /readyz, /-/status)
â”‚   â”œâ”€â”€ app/                # Server lifecycle, dependency wiring, and middleware
â”‚   â”œâ”€â”€ auth/               # HMAC-SHA256 JWT verifier
â”‚   â”œâ”€â”€ balancer/           # Round-robin and least-connections balancers
â”‚   â”œâ”€â”€ circuitbreaker/     # Sliding-window circuit breaker & admission permits
â”‚   â”œâ”€â”€ config/             # Strict YAML validation, snapshots, and file watcher
â”‚   â”œâ”€â”€ health/             # Active prober with hysteresis
â”‚   â”œâ”€â”€ middleware/         # Security headers & request-ID validation
â”‚   â”œâ”€â”€ mock/               # Mock backend engine
â”‚   â”œâ”€â”€ proxy/              # httputil.ReverseProxy & pooled transport
â”‚   â”œâ”€â”€ ratelimit/          # Monotonic token-bucket rate limiter
â”‚   â”œâ”€â”€ router/             # Longest-prefix route matcher
â”‚   â””â”€â”€ telemetry/          # OpenTelemetry tracing, Prometheus metrics, structured logs
â”œâ”€â”€ results/                # Raw empirical benchmark & scenario JSON/CSV outputs
â”œâ”€â”€ scripts/                # Multi-process native demo and benchmark scripts
â””â”€â”€ tests/                  # Integration & security test suites
```

---

## Verification Commands

```bash
# Run unit tests across all packages
go test -v ./internal/...

# Run all end-to-end integration scenarios
go test -v ./tests/integration/...

# Run security header & request ID validation tests
go test -v ./tests/security/...

# Run Go microbenchmarks
go test -bench . -benchmem -run "^$" ./bench

# Static code analysis
go vet ./...
```

---

## License

This project is licensed under the MIT License. See [LICENSE](LICENSE) for details.
