package log_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

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

func TestLogger_DropsRecordsBelowLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, slog.LevelInfo)

	logger.DebugContext(context.Background(), "noisy")

	if buf.Len() != 0 {
		t.Errorf("debug record written at info level: %s", buf.String())
	}
}
