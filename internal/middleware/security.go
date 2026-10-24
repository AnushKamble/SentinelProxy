package middleware

import (
	"net/http"
	"strings"
)

var sensitiveHeaders = map[string]bool{
	"authorization": true,
	"cookie":        true,
	"set-cookie":    true,
	"proxy-authorization": true,
}

// SecurityHeadersMiddleware sets defensive security headers on all responses.
func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// RedactHeaders returns a shallow copy of headers with sensitive authorization/cookie fields redacted.
func RedactHeaders(h http.Header) http.Header {
	safe := make(http.Header, len(h))
	for k, vv := range h {
		lower := strings.ToLower(k)
		if sensitiveHeaders[lower] {
			safe[k] = []string{"[REDACTED]"}
		} else {
			safe[k] = vv
		}
	}
	return safe
}
