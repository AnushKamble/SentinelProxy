package auth_test

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/sentinelproxy/sentinelproxy/internal/auth"
)

var (
	testSecret        = []byte("test-super-secret-key-minimum-32-chars-long")
	wrongSecret       = []byte("different-secret-key-32-chars-long-abcdef")
	validIssuer       = "sentinelproxy-auth"
	validAudience     = "sentinelproxy-clients"
	validSubject      = "user-12345"
)

func TestHMACVerifier_PositiveAndNegativeMatrix(t *testing.T) {
	v := auth.NewHMACVerifierWithSecret(validIssuer, validAudience, testSecret)

	// 1. Positive: valid token
	validToken, err := auth.SignTestToken(testSecret, validSubject, validIssuer, validAudience, 5*time.Minute, time.Time{})
	if err != nil {
		t.Fatalf("failed to sign test token: %v", err)
	}

	claims, err := v.Verify(validToken)
	if err != nil {
		t.Fatalf("expected valid token to verify, got: %v", err)
	}
	if claims["sub"] != validSubject {
		t.Errorf("expected subject %s, got %v", validSubject, claims["sub"])
	}

	// 2. Negative: empty/missing token
	_, err = v.Verify("")
	if !errors.Is(err, auth.ErrMissingToken) {
		t.Errorf("expected ErrMissingToken, got %v", err)
	}

	// 3. Negative: malformed token
	_, err = v.Verify("not.a.valid.jwt.token")
	if err == nil {
		t.Errorf("expected error for malformed token, got nil")
	}

	// 4. Negative: wrong signature
	wrongSigToken, _ := auth.SignTestToken(wrongSecret, validSubject, validIssuer, validAudience, 5*time.Minute, time.Time{})
	_, err = v.Verify(wrongSigToken)
	if !errors.Is(err, auth.ErrInvalidSignature) {
		t.Errorf("expected ErrInvalidSignature, got %v", err)
	}

	// 5. Negative: unsupported algorithm (e.g. none)
	noneToken := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"sub": validSubject,
		"iss": validIssuer,
		"aud": validAudience,
	})
	noneString, _ := noneToken.SignedString(jwt.UnsafeAllowNoneSignatureType)
	_, err = v.Verify(noneString)
	if err == nil {
		t.Errorf("expected error for 'none' algorithm token, got nil")
	}

	// 6. Negative: expired token
	expiredToken, _ := auth.SignTestToken(testSecret, validSubject, validIssuer, validAudience, -1*time.Minute, time.Time{})
	_, err = v.Verify(expiredToken)
	if !errors.Is(err, auth.ErrTokenExpired) {
		t.Errorf("expected ErrTokenExpired, got %v", err)
	}

	// 7. Negative: not-yet-valid token (nbf in future)
	futureNbf := time.Now().Add(10 * time.Minute)
	nbfToken, _ := auth.SignTestToken(testSecret, validSubject, validIssuer, validAudience, 30*time.Minute, futureNbf)
	_, err = v.Verify(nbfToken)
	if !errors.Is(err, auth.ErrTokenNotYetValid) {
		t.Errorf("expected ErrTokenNotYetValid, got %v", err)
	}

	// 8. Negative: issuer mismatch
	wrongIssuerToken, _ := auth.SignTestToken(testSecret, validSubject, "wrong-issuer", validAudience, 5*time.Minute, time.Time{})
	_, err = v.Verify(wrongIssuerToken)
	if !errors.Is(err, auth.ErrIssuerMismatch) {
		t.Errorf("expected ErrIssuerMismatch, got %v", err)
	}

	// 9. Negative: audience mismatch
	wrongAudToken, _ := auth.SignTestToken(testSecret, validSubject, validIssuer, "wrong-audience", 5*time.Minute, time.Time{})
	_, err = v.Verify(wrongAudToken)
	if !errors.Is(err, auth.ErrAudienceMismatch) {
		t.Errorf("expected ErrAudienceMismatch, got %v", err)
	}

	// 10. Negative: missing subject
	noSubToken, _ := auth.SignTestToken(testSecret, "", validIssuer, validAudience, 5*time.Minute, time.Time{})
	_, err = v.Verify(noSubToken)
	if !errors.Is(err, auth.ErrMissingSubject) {
		t.Errorf("expected ErrMissingSubject, got %v", err)
	}
}
