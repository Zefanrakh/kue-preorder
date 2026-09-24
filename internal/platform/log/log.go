// Package log builds the structured JSON logger and carries the correlation id
// of a request or job through its context.
//
// Log with the context-aware methods (InfoContext, ErrorContext, ...) so each
// record picks up the correlation id.
package log

import (
	"context"
	"io"
	"log/slog"
)

// CorrelationIDKey is the log field that holds the correlation id.
const CorrelationIDKey = "correlation_id"

type correlationIDKey struct{}

// WithCorrelationID returns a copy of ctx carrying id.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationIDKey{}, id)
}

// CorrelationID returns the id stored in ctx, or "" when there is none.
func CorrelationID(ctx context.Context) string {
	id, _ := ctx.Value(correlationIDKey{}).(string)
	return id
}

// New returns a JSON logger that writes records at level or above to w.
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(contextHandler{slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})})
}

// contextHandler adds the correlation id from the context to every record.
type contextHandler struct {
	slog.Handler
}

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := CorrelationID(ctx); id != "" {
		r.AddAttrs(slog.String(CorrelationIDKey, id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}
