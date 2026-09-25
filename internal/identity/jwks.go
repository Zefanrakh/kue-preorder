package identity

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"golang.org/x/time/rate"
)

// unknownKIDBudget bounds, for a token with an unknown kid, both the wait for
// the refresh rate limiter and the JWKS request itself: jwkset uses one
// context for both. It must leave room for a real network round trip.
const unknownKIDBudget = 5 * time.Second

// NewJWKS returns a KeySource backed by the Supabase Auth JWKS endpoint.
//
//   - Keys refresh hourly in the background until ctx ends.
//   - A token with an unknown kid (after a key rotation) triggers at most one
//     refresh per minute. When the next refresh slot is further away than
//     unknownKIDBudget, the limiter fails at once instead of waiting, so
//     tokens with made-up kids cannot pile up blocked requests.
//   - A failed first fetch does not stop startup: keys load on a later
//     refresh, and tokens are rejected until then.
func NewJWKS(ctx context.Context, url string, logger *slog.Logger) (KeySource, error) {
	noErrorOnFirstFetch := true
	keys, err := keyfunc.NewDefaultOverrideCtx(ctx, []string{url}, keyfunc.Override{
		HTTPTimeout:               10 * time.Second,
		NoErrorReturnFirstHTTPReq: &noErrorOnFirstFetch,
		RefreshInterval:           time.Hour,
		RefreshUnknownKID:         rate.NewLimiter(rate.Every(time.Minute), 1),
		RateLimitWaitMax:          unknownKIDBudget,
		RefreshErrorHandlerFunc: func(u string) func(context.Context, error) {
			return func(ctx context.Context, err error) {
				logger.ErrorContext(ctx, "refresh JWKS failed", slog.String("url", u), slog.Any("error", err))
			}
		},
	})
	if err != nil {
		return nil, fmt.Errorf("set up JWKS from %s: %w", url, err)
	}
	return keys, nil
}
