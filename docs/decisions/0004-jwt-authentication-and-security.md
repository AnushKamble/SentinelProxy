# ADR 0004: JWT Authentication and Security Controls

## Context
API gateways often enforce edge authentication. Hand-rolled JWT parsers or loose validation (e.g., trusting token header algorithm or omitting audience checks) cause critical security vulnerabilities (e.g., algorithm confusion, signature bypass).

## Decision
1. **Maintained Library:** Use `github.com/golang-jwt/jwt/v5` for parsing and validation. No custom cryptographic implementations.
2. **Fixed Algorithm Allowlist:** Hardcode/configure explicit signing algorithms (e.g., `HS256` for the reference deployment), sourced via configuration and environment variables. Never accept an algorithm specified solely in the token header.
3. **Strict Claim Validation:** Always validate `exp`, `nbf` (when present), `iss` (issuer), `aud` (audience), and required claims.
4. **Fail Closed:** If a route requires authentication and the verifier is unconfigured or misconfigured, immediately fail closed with 401/500 (safe internal error, no secret leak).
5. **No Secret Leakage:** Never print authorization headers, bearer tokens, or signature errors to logs or metrics labels.

## Consequences
- **Positive:** Robust defense against CVEs, standard compliant, secure defaults.
- **Negative:** Requires secret/key provisioning via environment variables.
