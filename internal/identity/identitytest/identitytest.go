// Package identitytest supports tests that need Supabase access tokens: a
// TokenIssuer that signs tokens with keys served from a local JWKS endpoint,
// and an in-memory identity.Repository.
package identitytest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/MicahParks/jwkset"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	// SupabaseURL is the fake project URL the issuer pretends to be.
	SupabaseURL = "https://test.supabase.co"
	// Issuer is the "iss" claim of issued tokens.
	Issuer = SupabaseURL + "/auth/v1"
)

// TokenIssuer signs access tokens like Supabase Auth and serves its public
// keys as a JWKS over HTTP. It is safe for concurrent use.
type TokenIssuer struct {
	// JWKSURL serves the public keys of every key the issuer has used.
	JWKSURL string

	mu   sync.Mutex
	keys []signingKey // the last one signs
}

type signingKey struct {
	kid string
	key *ecdsa.PrivateKey
}

// NewTokenIssuer starts a JWKS server with one ES256 key. The server stops
// when the test ends.
func NewTokenIssuer(t *testing.T) *TokenIssuer {
	t.Helper()
	iss := &TokenIssuer{}
	iss.Rotate(t)
	srv := httptest.NewServer(http.HandlerFunc(iss.serveJWKS))
	t.Cleanup(srv.Close)
	iss.JWKSURL = srv.URL + "/auth/v1/.well-known/jwks.json"
	return iss
}

// Rotate starts signing with a new key. Old keys stay published, as Supabase
// keeps previously used keys until they are revoked.
func (i *TokenIssuer) Rotate(t *testing.T) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("identitytest: generate key: %v", err)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.keys = append(i.keys, signingKey{kid: "key-" + strconv.Itoa(len(i.keys)+1), key: key})
}

// KID returns the key id of the signing key.
func (i *TokenIssuer) KID() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.keys[len(i.keys)-1].kid
}

// Sign signs claims with ES256 and the current key.
func (i *TokenIssuer) Sign(t *testing.T, claims jwt.Claims) string {
	t.Helper()
	i.mu.Lock()
	k := i.keys[len(i.keys)-1]
	i.mu.Unlock()

	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = k.kid
	signed, err := token.SignedString(k.key)
	if err != nil {
		t.Fatalf("identitytest: sign token: %v", err)
	}
	return signed
}

// Claims returns the claims of a valid access token for user issued at now.
// Tests change them to build invalid tokens.
func Claims(user uuid.UUID, now time.Time) jwt.MapClaims {
	return jwt.MapClaims{
		"iss":          Issuer,
		"aud":          "authenticated",
		"sub":          user.String(),
		"role":         "authenticated",
		"email":        "user@example.com",
		"is_anonymous": false,
		"iat":          now.Unix(),
		"exp":          now.Add(time.Hour).Unix(),
	}
}

func (i *TokenIssuer) serveJWKS(w http.ResponseWriter, _ *http.Request) {
	i.mu.Lock()
	keys := slices.Clone(i.keys)
	i.mu.Unlock()

	var set jwkset.JWKSMarshal
	for _, k := range keys {
		jwk, err := jwkset.NewJWKFromKey(&k.key.PublicKey, jwkset.JWKOptions{
			Metadata: jwkset.JWKMetadataOptions{ALG: jwkset.AlgES256, KID: k.kid, USE: jwkset.UseSig},
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		set.Keys = append(set.Keys, jwk.Marshal())
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(set)
}
