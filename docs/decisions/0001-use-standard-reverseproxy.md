# ADR 0001: Use Go Standard Library ReverseProxy

## Context
An API gateway requires robust HTTP/1.1 forwarding, connection reuse, chunked transfer encoding, trailer support, and standard hop-by-hop header removal. Hand-rolling low-level HTTP client forwarding often introduces subtle RFC violations, connection leaks, header smuggling bugs, and body buffering bottlenecks.

## Decision
SentinelProxy delegates low-level HTTP proxying to Go's standard `net/http/httputil.ReverseProxy`.
- We configure a custom pooled `http.Transport` with explicit dial timeouts, keep-alive parameters, TLS handshakes, header timeouts, and connection pool sizing.
- We customize the `Rewrite` function to safely rewrite target URLs, strip hop-by-hop headers, set forwarding headers, and prevent user-controlled upstream injection.
- We wrap the round-trip and response body reading to track lifecycle metrics, in-flight connection counts, and circuit breaker outcomes without modifying low-level protocol parsing.

## Consequences
- **Positive:** High performance, zero hand-rolled protocol vulnerabilities, native HTTP/2 backend compatibility where supported, standard streaming semantics.
- **Negative:** Need to carefully hook into the `RoundTripper` or `ModifyResponse` to ensure accurate in-flight accounting until response bodies are fully closed by clients.
