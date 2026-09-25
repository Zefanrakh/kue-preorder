package log_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/Zefanrakh/kue-preorder/internal/platform/log"
)

func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("log output is not JSON: %v\n%s", err, buf)
	}
	return rec
}

func TestLogger_AddsCorrelationIDFromContext(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, slog.LevelInfo)
	ctx := log.WithCorrelationID(context.Background(), "req-123")

	logger.InfoContext(ctx, "order confirmed", slog.String("order_id", "o-1"))

	rec := decode(t, &buf)
	if got := rec[log.CorrelationIDKey]; got != "req-123" {
		t.Errorf("%s = %v, want %q", log.CorrelationIDKey, got, "req-123")
	}
	if got := rec["order_id"]; got != "o-1" {
		t.Errorf("order_id = %v, want %q", got, "o-1")
	}
}

func TestLogger_OmitsCorrelationIDWhenAbsent(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, slog.LevelInfo)

	logger.InfoContext(context.Background(), "started")

	if _, ok := decode(t, &buf)[log.CorrelationIDKey]; ok {
		t.Errorf("record has %s, want none", log.CorrelationIDKey)
	}
}

func TestLogger_KeepsCorrelationIDAfterWith(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, slog.LevelInfo).With(slog.String("module", "orders"))
	ctx := log.WithCorrelationID(context.Background(), "req-456")

	logger.InfoContext(ctx, "transition")

	rec := decode(t, &buf)
	if got := rec[log.CorrelationIDKey]; got != "req-456" {
		t.Errorf("%s = %v, want %q", log.CorrelationIDKey, got, "req-456")
	}
	if got := rec["module"]; got != "orders" {
		t.Errorf("module = %v, want %q", got, "orders")
	}
}

func TestLogger_AddsActiveTraceSpan(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, slog.LevelInfo)
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6, 0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36},
		SpanID:     trace.SpanID{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	logger.InfoContext(ctx, "query")

	rec := decode(t, &buf)
	if got := rec[log.TraceIDKey]; got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("%s = %v, want the span's trace id", log.TraceIDKey, got)
	}
	if got := rec[log.SpanIDKey]; got != "00f067aa0ba902b7" {
		t.Errorf("%s = %v, want the span's id", log.SpanIDKey, got)
	}
}

func TestLogger_OmitsTraceWithoutSpan(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, slog.LevelInfo)

	logger.InfoContext(context.Background(), "started")

	rec := decode(t, &buf)
	if _, ok := rec[log.TraceIDKey]; ok {
		t.Errorf("record has %s without an active span", log.TraceIDKey)
	}
}

func TestLogger_DropsRecordsBelowLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, slog.LevelInfo)

	logger.DebugContext(context.Background(), "noisy")

	if buf.Len() != 0 {
		t.Errorf("debug record written at info level: %s", buf.String())
	}
}
