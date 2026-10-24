# SentinelProxy Threat Model and Security Architecture

This document defines the security boundaries, assets, threats, and defensive controls implemented in SentinelProxy.

---

## 1. Trust Boundaries and System Actors

```mermaid
flowchart LR
    subgraph UntrustedZone ["Untrusted Public Network"]
        Attacker["Potential Adversary / Malicious Client"]
        Client["Legitimate Client"]
    end

    subgraph Perimeter ["Gateway Perimeter"]
        SentinelProxy["SentinelProxy Edge Gateway"]
    end

    subgraph TrustedZone ["Internal Private Network (VPC / Loopback)"]
        BackendA["Upstream Microservice A"]
        BackendB["Upstream Microservice B"]
        Prometheus["Prometheus Scraper"]
    end

    Client -->|Public HTTP/TLS| SentinelProxy
    Attacker -->|Spoofed Headers / Exploits| SentinelProxy
    SentinelProxy -->|Sanitized Reverse Proxy| BackendA
    SentinelProxy -->|Sanitized Reverse Proxy| BackendB
    Prometheus -->|Internal Metrics Scrape| SentinelProxy
```

---

## 2. Threat Analysis and Implemented Defenses

### Threat 1: Request Header Injection & Spoofing (`X-Forwarded-For`)
- **Risk:** An attacker sends `X-Forwarded-For: 127.0.0.1` to bypass IP-based rate limiting or gain administrative access.
- **Defense:** SentinelProxy enforces strict CIDR validation via `trusted_proxy_cidrs`.
  - If the direct TCP peer (`RemoteAddr`) is NOT in `trusted_proxy_cidrs`, the incoming `X-Forwarded-For` header is stripped and ignored.
  - The rate limiter falls back to using the verified direct peer IP.

### Threat 2: Hop-by-Hop & HTTP Smuggling Headers
- **Risk:** Malicious clients send hop-by-hop headers (`Transfer-Encoding`, `Upgrade`, `Keep-Alive`, `Proxy-Authorization`) to induce protocol desynchronization or proxy bypass between the gateway and upstream servers.
- **Defense:** The `httputil.ReverseProxy` Rewrite hook strips all hop-by-hop headers:
  - `Keep-Alive`, `Proxy-Authenticate`, `Proxy-Authorization`, `TE`, `Trailer`, `Transfer-Encoding`, and `Upgrade`.
  - The `Host` header is explicitly overridden to match the upstream target URL, preventing host-header poisoning.

### Threat 3: High-Cardinality Metrics Denial of Service (Prometheus Memory Explosion)
- **Risk:** An attacker generates millions of distinct request URLs or user-agents, aiming to explode the Prometheus metrics label set and trigger Out-Of-Memory (OOM) crashes.
- **Defense:** SentinelProxy enforces strict bounded label cardinality on all Prometheus metrics:
  - Labels on `sentinelproxy_requests_total`: `route_id`, `method`, `status_class`.
  - HTTP status codes are bucketed into categorical classes: `2xx`, `3xx`, `4xx`, `5xx`, `error`.
  - Arbitrary paths, query strings, user IDs, and client IP addresses are **NEVER** placed into Prometheus labels.

### Threat 4: JWT Token Manipulation and Algorithm Substitution (Key Confusion / "none" Attack)
- **Risk:** Attackers forge tokens by altering headers to `alg: none` or confusing HMAC with RSA public keys.
- **Defense:** SentinelProxy uses a maintained and audited JWT library (`github.com/golang-jwt/jwt/v5`) and enforces:
  - Strict algorithm allowlisting: only `HS256` is accepted; all other algorithms (including `none`, `RS256`, `ES256`) are rejected at signature parse time.
  - Secret keys must be at least 32 bytes (256 bits).
  - Claims `exp`, `nbf`, `iss`, and `aud` are validated with strict bounds.

### Threat 5: Sensitive Credential Leakage in Logs and Headers
- **Risk:** Passwords, API tokens, Authorization headers, or JWTs leaked into centralized log aggregators.
- **Defense:**
  - Structured logger (`log/slog`) specifically redacts Authorization and Cookie headers.
  - Upstream backend URLs with embedded credentials (`http://user:pass@host`) are rejected during configuration validation.
  - JWT secret is loaded only from environment variables (`secret_env`), never committed to source files.

### Threat 6: Cascading Outage and Thread Starvation (Blast Radius)
- **Risk:** A slow or hung downstream service consumes all gateway worker threads, starving healthy services.
- **Defense:**
  - Every route has an independent context timeout deadline.
  - Sliding-window circuit breakers track errors per route/backend pair.
  - When a backend degrades, its circuit breaker trips to `OPEN`, immediately returning fast `503` (< 1ms) without waiting for timeouts.
  - Load balancers automatically divert traffic to surviving healthy candidates. Unrelated healthy routes remain completely isolated and unaffected.
