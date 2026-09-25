package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"connectrpc.com/connect"
)

// Recover turns a panic in next into a 500 response and logs it at error
// level with its stack, which also reports it to Sentry. The panic of
// http.ErrAbortHandler passes through: net/http uses it to abort a response
// on purpose.
func Recover(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(v)
			}
			logger.ErrorContext(r.Context(), "panic while serving request",
				slog.Any("error", fmt.Errorf("panic: %v", v)),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("stack", string(debug.Stack())))
			w.WriteHeader(http.StatusInternalServerError)
		}()
		next.ServeHTTP(w, r)
	})
}

// ConnectRecover makes Connect handlers answer a panic with an Internal error
// and log it like Recover does. Connect recovers inside the handler, so
// Recover never sees these panics.
func ConnectRecover(logger *slog.Logger) connect.HandlerOption {
	return connect.WithRecover(func(ctx context.Context, spec connect.Spec, _ http.Header, v any) error {
		logger.ErrorContext(ctx, "panic in rpc handler",
			slog.Any("error", fmt.Errorf("panic: %v", v)),
			slog.String("procedure", spec.Procedure),
			slog.String("stack", string(debug.Stack())))
		return connect.NewError(connect.CodeInternal, errors.New("internal error"))
	})
}
