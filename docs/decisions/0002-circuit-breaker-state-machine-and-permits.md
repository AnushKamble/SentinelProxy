# ADR 0002: Circuit Breaker State Machine and Admission Permits

## Context
Circuit breakers in distributed gateways protect upstreams from cascading failures by failing fast when backends degrade. Simple boolean flags or global counters are prone to race conditions, especially during the HALF_OPEN state where only a bounded number of probes must be admitted. Furthermore, if outcomes are reported before response bodies finish streaming, failures during streaming are ignored and in-flight counters can leak.

## Decision
1. **Per-Route/Backend Breaker Scope:** Breakers are scoped strictly per route and backend ID pair so degradation on one route/backend does not disrupt unrelated routes.
2. **Sliding-Window Outcome Ring:** Outgoing results are bucketed into a fixed ring of sliding-time buckets using monotonic clocks.
3. **Admission Permit API:** Requests must call `TryAcquire() (Permit, error)`. In `HALF_OPEN`, atomic permit counters strictly enforce `half_open_max_probes`.
4. **Terminal Result Hook:** An admitted permit must receive exactly one terminal outcome (`RecordSuccess()`, `RecordFailure()`, or `ReleaseCancel()`). The proxy wraps the upstream response body with an `io.ReadCloser` observer to guarantee the permit and least-connection counter are finalized exactly once upon body close, EOF, or error.
5. **Deterministic Testing:** All time and timer interactions use a clock interface (`Clock`) allowing deterministic fake clocks in tests without arbitrary `time.Sleep`.

## Consequences
- **Positive:** Concurrency safety under high load, zero permit leaks, bounded half-open concurrency, deterministic testability.
- **Negative:** Slightly more wrapping code around the HTTP response body stream.
