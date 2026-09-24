package httpserver_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Zefanrakh/kue-preorder/internal/platform/httpserver"
)

type pingerFunc func(ctx context.Context) error

func (f pingerFunc) Ping(ctx context.Context) error { return f(ctx) }

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func get(t *testing.T, path string) *http.Request {
	t.Helper()
	return httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
}

func serve(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, get(t, path))
	return rec
}

func TestHealthz_ReturnsOK(t *testing.T) {
	rec := serve(t, httpserver.Healthz(), "/healthz")

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Errorf("body = %q, want %q", got, "ok")
	}
}

func TestReadyz_DBUp_Returns200(t *testing.T) {
	db := pingerFunc(func(context.Context) error { return nil })

	rec := serve(t, httpserver.Readyz(db, discardLogger()), "/readyz")

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestReadyz_DBDown_Returns503WithoutLeakingError(t *testing.T) {
	db := pingerFunc(func(context.Context) error {
		return errors.New("dial tcp 10.0.0.5:5432: password authentication failed for user admin")
	})
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	rec := serve(t, httpserver.Readyz(db, logger), "/readyz")

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if strings.Contains(rec.Body.String(), "password") {
		t.Errorf("body leaks the database error: %q", rec.Body.String())
	}
	if !strings.Contains(logs.String(), "password authentication failed") {
		t.Errorf("database error not logged; logs = %s", logs.String())
	}
}

func TestReadyz_BoundsPingWithDeadline(t *testing.T) {
	var hasDeadline bool
	db := pingerFunc(func(ctx context.Context) error {
		_, hasDeadline = ctx.Deadline()
		return nil
	})

	serve(t, httpserver.Readyz(db, discardLogger()), "/readyz")

	if !hasDeadline {
		t.Error("Ping called without a deadline; a hung database would hang /readyz")
	}
}
