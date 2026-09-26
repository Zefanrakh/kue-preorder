package identity_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/identity/identitytest"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

var now = time.Date(2026, 9, 24, 3, 0, 0, 0, time.UTC)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newVerifier(t *testing.T, jwksURL string, clk clock.Clock) *identity.TokenVerifier {
	t.Helper()
	keys, err := identity.NewJWKS(t.Context(), jwksURL, discardLogger())
	if err != nil {
		t.Fatalf("NewJWKS() error = %v", err)
	}
	return identity.NewTokenVerifier(keys, identitytest.Issuer, clk)
}

func TestVerify_AcceptsValidToken(t *testing.T) {
	iss := identitytest.NewTokenIssuer(t)
	v := newVerifier(t, iss.JWKSURL, clock.NewFake(now))
	user := uuid.New()

	got, err := v.Verify(t.Context(), iss.Sign(t, identitytest.Claims(user, now)))

	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if got.ID != user || got.Email != "user@example.com" {
		t.Errorf("Verify() = %+v, want user %s with email user@example.com", got, user)
	}
}

func TestVerify_AcceptsExpiryWithinClockSkew(t *testing.T) {
	iss := identitytest.NewTokenIssuer(t)
	v := newVerifier(t, iss.JWKSURL, clock.NewFake(now))
	claims := identitytest.Claims(uuid.New(), now.Add(-time.Hour))
	claims["exp"] = now.Add(-10 * time.Second).Unix()

	if _, err := v.Verify(t.Context(), iss.Sign(t, claims)); err != nil {
		t.Errorf("Verify() error = %v, want accepted within the 30s skew", err)
	}
}

func TestVerify_ExpiresWithTheClock(t *testing.T) {
	iss := identitytest.NewTokenIssuer(t)
	clk := clock.NewFake(now)
	v := newVerifier(t, iss.JWKSURL, clk)
	token := iss.Sign(t, identitytest.Claims(uuid.New(), now))

	if _, err := v.Verify(t.Context(), token); err != nil {
		t.Fatalf("Verify() before expiry error = %v", err)
	}
	clk.Advance(2 * time.Hour)
	if _, err := v.Verify(t.Context(), token); !errors.Is(err, identity.ErrInvalidToken) {
		t.Errorf("Verify() after expiry error = %v, want ErrInvalidToken", err)
	}
}

func TestVerify_RejectsInvalidTokens(t *testing.T) {
	iss := identitytest.NewTokenIssuer(t)
	v := newVerifier(t, iss.JWKSURL, clock.NewFake(now))
	user := uuid.New()
	valid := func() jwt.MapClaims { return identitytest.Claims(user, now) }
	with := func(key string, value any) string {
		c := valid()
		if value == nil {
			delete(c, key)
		} else {
			c[key] = value
		}
		return iss.Sign(t, c)
	}

	// Guard: the verifier works, so each rejection below is for its own reason.
	if _, err := v.Verify(t.Context(), iss.Sign(t, valid())); err != nil {
		t.Fatalf("Verify(valid token) error = %v", err)
	}

	tests := []struct {
		name       string
		token      string
		wantReason string
	}{
		{"expired beyond clock skew", with("exp", now.Add(-time.Minute).Unix()), "token is expired"},
		{"issued in the future", with("iat", now.Add(5*time.Minute).Unix()), "token used before issued"},
		{"no expiry", with("exp", nil), "exp claim is required"},
		{"another project's issuer", with("iss", "https://other.supabase.co/auth/v1"), "invalid issuer"},
		{"wrong audience", with("aud", "someone-else"), "invalid audience"},
		{"anon role", with("role", "anon"), "not a signed-in user"},
		{"service_role", with("role", "service_role"), "not a signed-in user"},
		{"anonymous sign-in", with("is_anonymous", true), "not a signed-in user"},
		{"subject is not a user id", with("sub", "not-a-uuid"), "subject is not a user id"},
		{"HS256 under a real key id", signHS256(t, iss.KID(), valid()), "signing method HS256 is invalid"},
		{"alg none", signNone(t, valid()), "signing method none is invalid"},
		{"signed by an unknown key", signWithForeignKey(t, valid()), "keyfunc"},
		{"tampered payload", tamper(t, iss.Sign(t, valid())), "signature is invalid"},
		{"not a JWT", "hello", "malformed"},
		{"empty", "", "malformed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := v.Verify(t.Context(), tt.token)
			if !errors.Is(err, identity.ErrInvalidToken) {
				t.Fatalf("Verify() error = %v, want ErrInvalidToken", err)
			}
			if !strings.Contains(err.Error(), tt.wantReason) {
				t.Errorf("Verify() error = %q, want reason %q", err, tt.wantReason)
			}
		})
	}
}

func TestVerify_AcceptsTokenFromRotatedKey(t *testing.T) {
	iss := identitytest.NewTokenIssuer(t)
	v := newVerifier(t, iss.JWKSURL, clock.NewFake(now))

	// A real JWKS fetch takes a network round trip; the refresh must allow for it.
	iss.SetLatency(50 * time.Millisecond)
	iss.Rotate(t)
	token := iss.Sign(t, identitytest.Claims(uuid.New(), now))

	if _, err := v.Verify(t.Context(), token); err != nil {
		t.Errorf("Verify() with the new key error = %v, want the unknown kid to trigger a JWKS refresh", err)
	}
}

// Made-up kids must not hold requests: once the minute's refresh is used,
// further unknown kids fail at once instead of waiting for the next slot.
func TestVerify_UnknownKIDsDoNotBlockRequests(t *testing.T) {
	iss := identitytest.NewTokenIssuer(t)
	v := newVerifier(t, iss.JWKSURL, clock.NewFake(now))
	claims := identitytest.Claims(uuid.New(), now)

	if _, err := v.Verify(t.Context(), signWithForeignKey(t, claims)); !errors.Is(err, identity.ErrInvalidToken) {
		t.Fatalf("first unknown kid: error = %v, want ErrInvalidToken", err)
	}

	second := signWithForeignKey(t, claims) // t.Fatal must stay on the test goroutine
	done := make(chan error, 1)
	go func() {
		_, err := v.Verify(t.Context(), second)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, identity.ErrInvalidToken) {
			t.Errorf("second unknown kid: error = %v, want ErrInvalidToken", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Verify blocked waiting for the JWKS refresh rate limit")
	}
}

func TestNewJWKS_StartsWhileEndpointIsDown(t *testing.T) {
	down := httptest.NewServer(nil)
	down.Close()

	v := newVerifier(t, down.URL+"/auth/v1/.well-known/jwks.json", clock.NewFake(now))

	iss := identitytest.NewTokenIssuer(t)
	_, err := v.Verify(t.Context(), iss.Sign(t, identitytest.Claims(uuid.New(), now)))
	if !errors.Is(err, identity.ErrInvalidToken) {
		t.Errorf("Verify() without keys error = %v, want ErrInvalidToken", err)
	}
}

func signHS256(t *testing.T, kid string, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	token.Header["kid"] = kid
	signed, err := token.SignedString([]byte("legacy-shared-secret"))
	if err != nil {
		t.Fatalf("sign HS256: %v", err)
	}
	return signed
}

func signNone(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	signed, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign none: %v", err)
	}
	return signed
}

func signWithForeignKey(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = "not-in-the-jwks"
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("sign ES256: %v", err)
	}
	return signed
}

// tamper swaps the payload for one naming another user, keeping the signature.
func tamper(t *testing.T, token string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	payload, err := json.Marshal(identitytest.Claims(uuid.New(), now))
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	parts[1] = base64.RawURLEncoding.EncodeToString(payload)
	return strings.Join(parts, ".")
}

// Supabase writes the phone without the plus; a user who signed in by email
// has none. A malformed number is no number rather than a broken token.
func TestVerify_ReadsThePhone(t *testing.T) {
	iss := identitytest.NewTokenIssuer(t)
	v := newVerifier(t, iss.JWKSURL, clock.NewFake(now))
	tests := map[string]string{
		"6281234567890":  "+6281234567890",
		"+6281234567890": "+6281234567890",
		"":               "",
		"0812345":        "",
		"not a phone":    "",
	}
	for claim, want := range tests {
		claims := identitytest.Claims(uuid.New(), now)
		claims["phone"] = claim
		got, err := v.Verify(t.Context(), iss.Sign(t, claims))
		if err != nil || got.Phone != want {
			t.Errorf("phone claim %q: Verify() = %q, %v; want %q", claim, got.Phone, err, want)
		}
	}
}
