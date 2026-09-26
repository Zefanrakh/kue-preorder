package identity

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

const (
	// Supabase puts both values in every access token of a signed-in user.
	tokenAudience = "authenticated"
	tokenRole     = "authenticated"

	// clockSkew tolerates small clock differences with Supabase Auth.
	clockSkew = 30 * time.Second
)

// signingMethods are the only accepted algorithms. Supabase signs with an
// asymmetric key from the JWKS; HS256 (the legacy shared secret) and "none"
// are refused, so a token cannot choose a weaker algorithm.
var signingMethods = []string{"ES256", "RS256"}

// KeySource looks up the public key that signed a token.
type KeySource interface {
	KeyfuncCtx(ctx context.Context) jwt.Keyfunc
}

// TokenVerifier checks Supabase Auth access tokens.
type TokenVerifier struct {
	keys   KeySource
	issuer string
	clock  clock.Clock
}

// NewTokenVerifier returns a verifier that accepts tokens from issuer, the
// project's "<SUPABASE_URL>/auth/v1".
func NewTokenVerifier(keys KeySource, issuer string, clk clock.Clock) *TokenVerifier {
	return &TokenVerifier{keys: keys, issuer: issuer, clock: clk}
}

type accessTokenClaims struct {
	jwt.RegisteredClaims
	Role        string `json:"role"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
	IsAnonymous bool   `json:"is_anonymous"`
}

// Verify returns the user the access token was issued to. Every failure wraps
// ErrInvalidToken; the wrapped detail is for logs, not for clients.
func (v *TokenVerifier) Verify(ctx context.Context, token string) (AuthUser, error) {
	var claims accessTokenClaims
	_, err := jwt.ParseWithClaims(token, &claims, v.keys.KeyfuncCtx(ctx),
		jwt.WithValidMethods(signingMethods),
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(tokenAudience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(clockSkew),
		jwt.WithTimeFunc(v.clock.Now),
	)
	if err != nil {
		return AuthUser{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	if claims.Role != tokenRole || claims.IsAnonymous {
		return AuthUser{}, fmt.Errorf("%w: role %q, anonymous %t: not a signed-in user", ErrInvalidToken, claims.Role, claims.IsAnonymous)
	}
	id, err := uuid.Parse(claims.Subject)
	if err != nil {
		return AuthUser{}, fmt.Errorf("%w: subject is not a user id", ErrInvalidToken)
	}
	return AuthUser{ID: id, Email: claims.Email, Phone: NormalizePhone(claims.Phone)}, nil
}

// e164 is a phone number in E.164: a plus and 7 to 15 digits.
var e164 = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

// NormalizePhone turns Supabase's phone claim, digits without the plus
// (6281234567890), into E.164. A malformed number counts as no number: the
// user simply cannot order until they sign in with WhatsApp.
func NormalizePhone(claim string) string {
	p := strings.TrimSpace(claim)
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "+") {
		p = "+" + p
	}
	if !e164.MatchString(p) {
		return ""
	}
	return p
}
