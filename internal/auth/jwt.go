package auth

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/sentinelproxy/sentinelproxy/internal/config"
)

var (
	ErrMissingToken       = errors.New("missing authorization token")
	ErrInvalidTokenFormat = errors.New("invalid authorization header format")
	ErrUnsupportedAlgo    = errors.New("unsupported signing algorithm")
	ErrInvalidSignature   = errors.New("invalid token signature")
	ErrTokenExpired       = errors.New("token is expired")
	ErrTokenNotYetValid   = errors.New("token is not yet valid")
	ErrIssuerMismatch     = errors.New("token issuer mismatch")
	ErrAudienceMismatch   = errors.New("token audience mismatch")
	ErrMissingSubject     = errors.New("token missing required subject")
)

// Verifier inspects and validates incoming JWTs.
type Verifier interface {
	Verify(tokenString string) (jwt.MapClaims, error)
}

// HMACVerifier strictly validates HS256 signed JWT tokens using golang-jwt/jwt/v5.
type HMACVerifier struct {
	algorithm string
	issuer    string
	audience  string
	secret    []byte
}

// NewHMACVerifier creates a verifier from JWTConfig, reading secret from the environment.
func NewHMACVerifier(cfg config.JWTConfig) (*HMACVerifier, error) {
	if cfg.Algorithm != "HS256" {
		return nil, fmt.Errorf("unsupported jwt algorithm %q: only HS256 is permitted", cfg.Algorithm)
	}

	secretVal := os.Getenv(cfg.SecretEnv)
	if strings.TrimSpace(secretVal) == "" {
		return nil, fmt.Errorf("jwt secret environment variable %q is not set or empty", cfg.SecretEnv)
	}

	return &HMACVerifier{
		algorithm: cfg.Algorithm,
		issuer:    cfg.Issuer,
		audience:  cfg.Audience,
		secret:    []byte(secretVal),
	}, nil
}

// NewHMACVerifierWithSecret creates a verifier with an explicit secret byte slice (useful for tests).
func NewHMACVerifierWithSecret(issuer, audience string, secret []byte) *HMACVerifier {
	return &HMACVerifier{
		algorithm: "HS256",
		issuer:    issuer,
		audience:  audience,
		secret:    secret,
	}
}

// Verify parses and strictly validates signature, expiry, not-before, issuer, audience, and subject.
func (v *HMACVerifier) Verify(tokenString string) (jwt.MapClaims, error) {
	if strings.TrimSpace(tokenString) == "" {
		return nil, ErrMissingToken
	}

	// Parse with strict parser options: fixed algorithm allowlist only
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (any, error) {
		// Strict algorithm check: prevent algorithm confusion attacks (e.g. none or RSA public key trick)
		if t.Method.Alg() != v.algorithm {
			return nil, fmt.Errorf("%w: expected %s, got %s", ErrUnsupportedAlgo, v.algorithm, t.Method.Alg())
		}
		return v.secret, nil
	},
		jwt.WithValidMethods([]string{v.algorithm}),
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(v.audience),
		jwt.WithLeeway(0), // No clock skew leniency for strict validation
	)

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		if errors.Is(err, jwt.ErrTokenNotValidYet) {
			return nil, ErrTokenNotYetValid
		}
		if errors.Is(err, jwt.ErrTokenInvalidIssuer) {
			return nil, ErrIssuerMismatch
		}
		if errors.Is(err, jwt.ErrTokenInvalidAudience) {
			return nil, ErrAudienceMismatch
		}
		if errors.Is(err, jwt.ErrTokenSignatureInvalid) {
			return nil, ErrInvalidSignature
		}
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return nil, ErrInvalidSignature
	}

	// Validate required subject claim
	sub, ok := claims["sub"].(string)
	if !ok || strings.TrimSpace(sub) == "" {
		return nil, ErrMissingSubject
	}

	return claims, nil
}

// Helper to sign tokens for testing.
func SignTestToken(secret []byte, sub, issuer, audience string, validFor time.Duration, notBefore time.Time) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"sub": sub,
		"iss": issuer,
		"aud": audience,
		"iat": now.Unix(),
	}
	if validFor > 0 {
		claims["exp"] = now.Add(validFor).Unix()
	} else if validFor < 0 {
		claims["exp"] = now.Add(validFor).Unix() // Expired
	}
	if !notBefore.IsZero() {
		claims["nbf"] = notBefore.Unix()
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}
