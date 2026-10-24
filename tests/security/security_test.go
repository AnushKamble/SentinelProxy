package security_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sentinelproxy/sentinelproxy/internal/middleware"
)

func TestSecurity_RequestIDValidation(t *testing.T) {
	// 1. Valid custom request ID accepted
	req1 := httptest.NewRequest("GET", "/", nil)
	req1.Header.Set("X-Request-ID", "custom-req-id_12345")
	id1 := middleware.ValidateAndExtractRequestID(req1, "X-Request-ID")
	if id1 != "custom-req-id_12345" {
		t.Errorf("expected valid custom ID to be preserved, got %q", id1)
	}

	// 2. Malicious / invalid request ID (e.g. CRLF injection, too long, special characters) rejected and replaced
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Header.Set("X-Request-ID", "evil\r\nInjected-Header: true")
	id2 := middleware.ValidateAndExtractRequestID(req2, "X-Request-ID")
	if id2 == req2.Header.Get("X-Request-ID") || strings.Contains(id2, "\r") {
		t.Errorf("expected malicious ID to be rejected and regenerated, got %q", id2)
	}
	if len(id2) != 32 {
		t.Errorf("expected generated ID length 32 hex chars, got %d", len(id2))
	}
}

func TestSecurity_ResponseHeaders(t *testing.T) {
	innerHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	secured := middleware.SecurityHeadersMiddleware(innerHandler)
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	secured.ServeHTTP(w, req)
	resp := w.Result()
	_ = resp.Body.Close()

	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("missing X-Content-Type-Options nosniff")
	}
	if resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Errorf("missing X-Frame-Options DENY")
	}
	if resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("missing Referrer-Policy no-referrer")
	}
}

func TestSecurity_HeaderRedaction(t *testing.T) {
	headers := http.Header{
		"Authorization":   []string{"Bearer my_super_secret_token_12345"},
		"Cookie":          []string{"session_id=abcdef123456"},
		"Set-Cookie":      []string{"session_id=abcdef123456; Secure"},
		"X-Custom-Header": []string{"safe-value"},
	}

	redacted := middleware.RedactHeaders(headers)

	if redacted.Get("Authorization") != "[REDACTED]" {
		t.Errorf("expected Authorization to be redacted, got %q", redacted.Get("Authorization"))
	}
	if redacted.Get("Cookie") != "[REDACTED]" {
		t.Errorf("expected Cookie to be redacted, got %q", redacted.Get("Cookie"))
	}
	if redacted.Get("Set-Cookie") != "[REDACTED]" {
		t.Errorf("expected Set-Cookie to be redacted, got %q", redacted.Get("Set-Cookie"))
	}
	if redacted.Get("X-Custom-Header") != "safe-value" {
		t.Errorf("expected non-sensitive header to be preserved, got %q", redacted.Get("X-Custom-Header"))
	}
}
