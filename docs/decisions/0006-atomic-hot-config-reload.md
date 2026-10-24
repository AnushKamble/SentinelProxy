# ADR 0006: Atomic Hot Config Reload

## Context
Operators need to update routing rules, timeouts, rate limits, and upstream lists without restarting the gateway process. In-place mutation of live configuration causes race conditions and partial states (e.g., an updated timeout paired with a deleted backend). Furthermore, an invalid configuration edit should never take down active traffic.

## Decision
1. **Immutable Snapshot Pattern:** Runtime state is represented as an immutable struct pointer (`atomic.Pointer[RuntimeSnapshot]`).
2. **Atomic Swap:** Upon detecting file changes (via fsnotify or polling with debouncing):
   - Parse and deeply validate the candidate YAML.
   - Build candidate transports, balancers, and route handlers.
   - If validation or construction fails, log structured errors and retain the current active snapshot (last-known-good).
   - If construction succeeds, atomically swap the snapshot pointer.
3. **State Continuity:** Where route IDs, backend IDs, and policy parameters match between snapshots, circuit breaker statistics, active health states, and token bucket counters are transferred safely.
4. **Safe Retirement:** Old transport connections and active requests finish on their respective snapshots before resources are freed.

## Consequences
- **Positive:** Zero downtime updates, zero mixed-state requests, resilience against bad configuration files.
- **Negative:** Requires state transfer logic to preserve breaker/health continuity across reloads.
