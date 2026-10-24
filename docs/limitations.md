# SentinelProxy Limitations and Operational Boundaries

This document provides a transparent engineering appraisal of design choices, scope boundaries, and architectural limitations in SentinelProxy.

---

## 1. Single-Instance / Local-Only State

- **Local Token Bucket:** The rate limiter uses an in-memory token bucket per instance. In a multi-node gateway deployment behind an external L4 load balancer, rate limits apply per node rather than globally.
  - *Future Work:* Introduce an optional distributed backend (e.g. Redis sliding window or Raft consensus) for global rate limiting across horizontal clusters.
- **Node-Local Circuit Breaker:** Circuit breaker state is tracked within each gateway instance. If one instance trips a breaker, sibling gateway instances evaluate their own traffic independently.
  - *Rationale:* Local circuit breaking avoids distributed consensus latency on critical request paths and prevents network partition split-brain issues.

---

## 2. In-Memory Sliding-Window Breaker Memory Footprint

- Each route/backend pair instantiates a sliding ring buffer with a configured number of buckets (default 10 buckets across 10 seconds).
- For gateways with thousands of dynamic upstream targets, memory scales linearly `O(routes × backends)`.
- *Mitigation:* Inactive breakers can be garbage collected using an idle eviction policy similar to the rate limiter cache.

---

## 3. JWT Cryptographic Algorithms

- Currently, SentinelProxy strictly implements HMAC-SHA256 (`HS256`) symmetric token verification.
- Asymmetric algorithms (`RS256`, `ES256`, `EdDSA`) and JWKS (JSON Web Key Set) remote endpoint polling are not yet supported.
  - *Rationale:* Kept deliberately minimal to avoid network fetch dependencies during boot/reload and to guarantee sub-microsecond token validation.

---

## 4. HTTP Protocol Support

- SentinelProxy reverse-proxying is built on Go's `net/http/httputil.ReverseProxy`, supporting HTTP/1.1 and HTTP/2 over cleartext and TLS.
- HTTP/3 (QUIC) and WebSocket full-duplex upgrades are not enabled in this baseline specification.
  - *Future Work:* Add connection hijacking for bidirectional WebSocket streaming and QUIC listener support.

---

## 5. Hot Reload Resource Retirement

- Hot reloads atomically swap the `RuntimeSnapshot` pointer. In-flight requests continue executing on the old snapshot's transport pool.
- Active TCP connections to removed upstream backends drain naturally via keep-alive idle timeouts (60s). An abrupt hard close of the old transport is avoided to prevent in-flight connection resets.
