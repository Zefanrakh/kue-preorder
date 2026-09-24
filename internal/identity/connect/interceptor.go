// Package connect serves the identity module over ConnectRPC and holds the
// authentication interceptor every Connect handler uses.
package connect

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	"github.com/Zefanrakh/kue-preorder/internal/identity"
)

// Verifier checks an access token.
type Verifier interface {
	Verify(ctx context.Context, token string) (identity.AuthUser, error)
}

// AuthInterceptor authenticates requests from their bearer token.
//
// A request without an Authorization header continues as anonymous, and each
// service decides what anonymous callers may do (authorization, §22). A header
// that is present but does not hold a valid token fails with Unauthenticated,
// so a client whose session broke never silently continues as anonymous.
type AuthInterceptor struct {
	verifier Verifier
	logger   *slog.Logger
}

var _ connect.Interceptor = (*AuthInterceptor)(nil)

// NewAuthInterceptor returns an interceptor that verifies tokens with verifier.
func NewAuthInterceptor(verifier Verifier, logger *slog.Logger) *AuthInterceptor {
	return &AuthInterceptor{verifier: verifier, logger: logger}
}

// WrapUnary implements connect.Interceptor.
func (i *AuthInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if req.Spec().IsClient {
			return next(ctx, req)
		}
		ctx, err := i.authenticate(ctx, req.Header())
		if err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

// WrapStreamingClient implements connect.Interceptor.
func (i *AuthInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler implements connect.Interceptor.
func (i *AuthInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx, err := i.authenticate(ctx, conn.RequestHeader())
		if err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

func (i *AuthInterceptor) authenticate(ctx context.Context, header http.Header) (context.Context, error) {
	raw := header.Get("Authorization")
	if raw == "" {
		return ctx, nil
	}
	// The scheme is case-insensitive (RFC 9110 §11.1).
	scheme, token, ok := strings.Cut(raw, " ")
	token = strings.TrimSpace(token)
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return ctx, connect.NewError(connect.CodeUnauthenticated, errors.New(`authorization header must be "Bearer <access token>"`))
	}
	user, err := i.verifier.Verify(ctx, token)
	if err != nil {
		// Expired sessions are routine; the detail helps debugging but stays out of the response.
		i.logger.InfoContext(ctx, "access token rejected", slog.Any("error", err))
		return ctx, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or expired access token"))
	}
	return identity.WithAuthUser(ctx, user), nil
}
