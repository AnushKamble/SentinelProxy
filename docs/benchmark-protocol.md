# SentinelProxy Benchmark Protocol and Evaluation Methodology

This document details the experimental methodology, workload parameters, hardware/software environment, and observed empirical results for the SentinelProxy benchmark evaluation.

---

## 1. Experimental Design and Controlled Conditions

We evaluate SentinelProxy under four rigorous, like-for-like experimental conditions:

1. **Condition A — Healthy Baseline:**
   - Both mock backends healthy and responding normally.
   - Circuit breakers enabled.
   - Establishes maximum baseline throughput and clean p50/p95/p99 latency distribution.

2. **Condition B — Breaker-Disabled Degradation:**
   - Upstream backend injected with 100% failure (`HTTP 500`) and 50ms artificial latency.
   - Circuit breaker disabled (`enabled: false`).
   - Simulates legacy gateways where worker threads block on slow, failing backends, resulting in catastrophic throughput collapse and connection starvation.

3. **Condition C — Breaker-Enabled Fault Isolation:**
   - Identical workload and failure injection as Condition B (100% `HTTP 500`, 50ms latency).
   - Circuit breaker enabled (`enabled: true`, sliding window 10s, min samples 5, failure threshold 50%, cooldown 2s).
   - Evaluates the gateway's ability to trip to `OPEN` and fast-fail subsequent requests with `503 Service Unavailable` (< 1ms), freeing worker goroutines.

4. **Condition D — Blast Radius & Healthy-Route Isolation:**
   - Route A (`/api/primary`) undergoing full degradation and failure.
   - Route B (`/api/isolated`) targeting an independent healthy backend.
   - Evaluates whether failures on Route A leak or degrade latency and success rates on Route B.

---

## 2. Benchmark Execution Protocol

- **Warm-up Phase:** Each run executes a 200ms warm-up phase to prime connection pools, JIT/runtime caches, and goroutine allocations before metrics collection begins.
- **Duration & Concurrency:** Concurrency = 8 client workers, measurement duration = 1.5 seconds per run.
- **Repetitions:** Every condition is executed across **5 independent runs**.
- **Metrics Aggregated:**
  - Throughput (req/s): median, min, max.
  - Latency (ms): p50, p90, p95, p99, min, max, mean.
  - Success Ratio (%): fraction of requests returning HTTP 2xx.
  - All raw per-run CSVs and aggregated JSON summaries are persisted under `results/benchmarks/`.

---

## 3. Test Environment Specifications

- **Operating System:** Windows 11 (AMD64)
- **CPU:** AMD Ryzen 9 8940HX with Radeon Graphics (32 logical cores)
- **Go Version:** `go1.27.0 windows/amd64`
- **Network Interface:** Loopback (`127.0.0.1`) with pooled keep-alive TCP connections.
- **Measurement Tooling:** `bench.Runner` / `cmd/loadgen` (custom Go high-throughput workload generator with atomic accounting and nanosecond timing).

---

## 4. Empirical Benchmark Results

The following measurements are direct empirical results from `results/benchmarks/*.json`:

| Condition | Median Throughput | p50 Latency | p95 Latency | p99 Latency | Success Rate | Primary Failure Mode |
|---|---|---|---|---|---|---|
| **A: Healthy Baseline** | **5,517.94 req/s** | **1.25 ms** | **2.38 ms** | **4.10 ms** | **100.00%** | None (All 200 OK) |
| **B: Breaker Disabled (Degraded)** | **156.54 req/s** | **50.94 ms** | **51.75 ms** | **52.60 ms** | **0.00%** | Worker blocked on 50ms delay |
| **C: Breaker Enabled (Degraded)** | **27,098.53 req/s** | **0.00 ms** | **1.01 ms** | **1.89 ms** | **0.00%** | Fast 503 Circuit-Breaker Rejection |
| **D: Isolated Route Protection** | **6,236.05 req/s** | **1.13 ms** | **2.44 ms** | **4.21 ms** | **100.00%** | Unaffected by sibling route outage |

### Key Findings:
1. **173x Throughput Improvement on Degradation:** When backends degrade, enabling the circuit breaker increases request handling throughput from **156.54 req/s to 27,098.53 req/s**, as SentinelProxy rejects broken requests in under **1 millisecond** instead of stalling client workers for 50ms.
2. **Zero Blast Radius:** Route B maintained **100.00% availability** and **1.13 ms p50 latency** even while Route A was saturated with failing and delayed traffic.
3. **Microbenchmark Efficiency:**
   - Route Match: **167.3 ns/op (0 B/op, 0 allocs/op)**
   - Rate Limiter Allow: **52.34 ns/op (0 B/op, 0 allocs/op)**
   - Breaker TryAcquire + Record: **108.5 ns/op (48 B/op, 1 allocs/op)**
