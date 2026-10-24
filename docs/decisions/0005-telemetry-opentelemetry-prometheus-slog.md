# ADR 0005: Telemetry Strategy (OpenTelemetry, Prometheus, slog)

## Context
Production gateways require end-to-end tracing, aggregatable metrics, and actionable structured logs. A common failure mode in gateway telemetry is metric cardinality explosion caused by including raw URLs, user IDs, or unconstrained error messages in labels.

## Decision
1. **W3C Trace Context:** Use OpenTelemetry Go SDK to parse inbound `traceparent`/`tracestate`, create a gateway server span, and propagate child client spans to upstreams.
2. **Prometheus Metrics with Bounded Cardinality:**
   - Strict label set: `route_id`, `backend_id`, `method`, `status_class`, `decision`, `state`.
   - Never use URLs, query strings, user IDs, IP addresses, or arbitrary error strings as metric labels.
   - Use Prometheus Histograms for latency measurements across instances.
3. **Structured Logging:** Use Go's standard library `log/slog` outputting JSON. Include `request_id`, `trace_id`, `route_id`, `backend_id`, and status. Automatically redact sensitive fields like `Authorization` and cookie headers.

## Consequences
- **Positive:** Standardized observability, safe Prometheus operational footprint, easy correlation between traces and logs.
- **Negative:** Telemetry wrappers must be applied consistently across the middleware chain.
