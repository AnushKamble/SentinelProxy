# SentinelProxy Operational Failure Semantics

This document defines the deterministic error codes, conditions, and failure lifecycle transitions enforced by SentinelProxy.

---

## 1. Gateway Status Code Mapping

SentinelProxy produces deterministic status codes across all failure modes:

| Status Code | Condition / Failure Root Cause | Headers Returned | Counts toward Breaker? |
|---|---|---|---|
| **`200 OK`** | Successful upstream response forwarding. | `X-Request-ID`, `X-Backend-ID`, upstream headers | **No** (Counts as success) |
| **`401 Unauthorized`** | Missing or malformed `Authorization: Bearer` header, invalid signature, expired `exp`, or failed `iss`/`aud` claims. | `Content-Type: application/json` | **No** (Client error; does not penalize upstream) |
| **`404 Not Found`** | Incoming request path matches no configured route prefix. | `Content-Type: application/json` | **No** |
| **`405 Method Not Allowed`** | Path matched a route, but the HTTP verb is not listed in `methods`. | `Allow: <supported methods>`, `Content-Type: application/json` | **No** |
| **`429 Too Many Requests`** | Client key (JWT `sub`, verified IP, or remote IP) exhausted token bucket burst capacity. | `Retry-After: <seconds>`, `Content-Type: application/json` | **No** (Does not penalize upstream) |
| **`502 Bad Gateway`** | Upstream backend TCP connection refused, reset, or unexpected EOF while reading response headers. | `Content-Type: application/json` | **Yes** (Recorded as upstream failure) |
| **`503 Service Unavailable`** | Circuit breaker is `OPEN`, all backends are uneligible/unhealthy, or `HALF_OPEN` probe concurrency limit reached. | `Content-Type: application/json` | **Yes** (If resulting from upstream failure) |
| **`504 Gateway Timeout`** | Upstream backend failed to complete response within `route.timeout` deadline. | `Content-Type: application/json` | **Yes** (Recorded as upstream timeout) |

---

## 2. Circuit Breaker State Transition Rules

Each route/backend pair maintains an isolated circuit breaker state machine:

### From `CLOSED` to `OPEN`
- **Precondition 1:** Total requests evaluated within the sliding window must be `>= minimum_requests` (default: 10).
- **Precondition 2:** Failure percentage (`failures / total_requests`) must be `>= failure_rate_threshold` (default: 0.50).
- **Action:** Transition to `StateOpen`. Set `openedAt = now`. Log warning and increment `sentinelproxy_circuit_breaker_transitions_total`.
- **Effect:** Subsequent requests to this backend fail fast immediately with `503 Service Unavailable`, bypassing reverse proxy transport and preserving timeout budgets.

### From `OPEN` to `HALF_OPEN`
- **Precondition:** Current time `>= openedAt + open_duration` (default: 5s).
- **Action:** Transition to `StateHalfOpen`. Reset `consecutiveSuccesses = 0`, `halfOpenInFlight = 0`.
- **Effect:** Admits up to `half_open_max_probes` concurrent requests to probe backend recovery.

### From `HALF_OPEN` to `CLOSED`
- **Precondition:** Backend successfully responds to `half_open_successes_to_close` consecutive probe requests.
- **Action:** Transition to `StateClosed`. Reset window error counters. Full normal traffic resumes.

### From `HALF_OPEN` to `OPEN`
- **Precondition:** Any probe request encounters an error, timeout, or counted failure status code (`500`, `502`, `503`, `504`).
- **Action:** Transition back to `StateOpen`. Reset `openedAt = now`. Cooldown timer restarts.

---

## 3. Active Health Checker Hysteresis

The background health checker prevents flapping by requiring consecutive confirmations before changing health state:

```
Unhealthy State ──────────────> [2 Consecutive 200 OK probes] ──────────────> Healthy State
      ▲                                                                             │
      │                                                                             │
      └────────────── [3 Consecutive Failed / Timeout probes] <─────────────────────┘
```

- **Hysteresis Guarantee:** A single transient error will NOT evict a backend from the healthy pool.
- **Independent Client:** Probes use a dedicated HTTP client with separate connection pools and strict probe timeouts (`timeout < interval`), ensuring prober execution never interferes with production client traffic.
