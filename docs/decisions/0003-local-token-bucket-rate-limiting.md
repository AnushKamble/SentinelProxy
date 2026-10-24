# ADR 0003: Local Token-Bucket Rate Limiting

## Context
API gateways need rate limiting to protect downstreams from resource exhaustion and noisy neighbors. While distributed rate limiters rely on external stores like Redis, adding an external dependency introduces operational complexity, latency overhead, and single points of failure.

## Decision
1. **In-Process Local Token Bucket:** Implement an in-memory token bucket per route and caller key.
2. **Key Derivation Hierarchy:**
   - Authenticated: Verified JWT `sub` (or configured stable claim).
   - Unauthenticated: Client IP extracted from `RemoteAddr`.
   - `X-Forwarded-For` is trusted only if the immediate peer's IP falls within explicitly configured `trusted_proxy_cidrs`.
3. **Bounded Memory:** Use a concurrent hash map with an LRU/idle-expiry cleaner and hard capacity bound. If the cache reaches capacity, deny or evict idle buckets safely rather than leaking memory or bypassing limits.
4. **Local-Only Semantics:** Explicitly document that rate limits are process-local; multiple gateway replicas do not share quota.

## Consequences
- **Positive:** Sub-microsecond evaluation, zero external network dependency, deterministic monotonic refill.
- **Negative:** Multiple gateway replicas enforce local rather than globally aggregated limits.
