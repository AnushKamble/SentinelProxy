package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"regexp"
)

type requestIDKey struct{}

var validRequestIDRegex = regexp.MustCompile(`^[a-zA-Z0-9_\-]{1,64}$`)

// GenerateRequestID creates a cryptographically random 16-byte hex ID.
func GenerateRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// WithRequestID stores the request ID in context.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFromContext retrieves the request ID from context.
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}

// ValidateAndExtractRequestID validates caller-supplied request ID or generates a new one.
func ValidateAndExtractRequestID(r *http.Request, headerName string) string {
	if headerName == "" {
		headerName = "X-Request-ID"
	}

	incomingID := r.Header.Get(headerName)
	if incomingID != "" && validRequestIDRegex.MatchString(incomingID) {
		return incomingID
	}

	return GenerateRequestID()
}
