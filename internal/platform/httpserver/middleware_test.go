package httpserver_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Zefanrakh/kue-preorder/internal/platform/httpserver"
	"github.com/Zefanrakh/kue-preorder/internal/platform/log"
)

// captureID returns a handler that records the correlation id it sees.
func captureID(got *string) http.Handler {
	return http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		*got = log.CorrelationID(r.Context())
	})
}

func TestCorrelationID_ReusesIncomingHeader(t *testing.T) {
	var seen string
	req := get(t, "/")
	req.Header.Set(httpserver.CorrelationIDHeader, "fly-7f3a:01")
	rec := httptest.NewRecorder()

	httpserver.CorrelationID(captureID(&seen)).ServeHTTP(rec, req)

	if seen != "fly-7f3a:01" {
		t.Errorf("context id = %q, want %q", seen, "fly-7f3a:01")
	}
	if got := rec.Header().Get(httpserver.CorrelationIDHeader); got != "fly-7f3a:01" {
		t.Errorf("response header = %q, want %q", got, "fly-7f3a:01")
	}
}

func TestCorrelationID_GeneratesWhenMissingOrInvalid(t *testing.T) {
	tests := []struct {
		name   string
		header string
	}{
		{"missing", ""},
		{"too long", strings.Repeat("a", 129)},
		{"log injection", "abc\n{\"level\":\"ERROR\"}"},
		{"spaces", "a b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen string
			req := get(t, "/")
			if tt.header != "" {
				req.Header[httpserver.CorrelationIDHeader] = []string{tt.header}
			}
			rec := httptest.NewRecorder()

			httpserver.CorrelationID(captureID(&seen)).ServeHTTP(rec, req)

			if seen == "" || seen == tt.header {
				t.Errorf("context id = %q, want a fresh id", seen)
			}
			if got := rec.Header().Get(httpserver.CorrelationIDHeader); got != seen {
				t.Errorf("response header = %q, want %q", got, seen)
			}
		})
	}
}

func TestCorrelationID_GeneratesDistinctIDs(t *testing.T) {
	var first, second string
	httpserver.CorrelationID(captureID(&first)).ServeHTTP(httptest.NewRecorder(), get(t, "/"))
	httpserver.CorrelationID(captureID(&second)).ServeHTTP(httptest.NewRecorder(), get(t, "/"))

	if first == second {
		t.Errorf("two requests got the same id %q", first)
	}
}

func TestCorrelationID_AppearsInLogs(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, slog.LevelInfo)
	handler := httpserver.CorrelationID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		logger.InfoContext(r.Context(), "handled")
	}))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, get(t, "/"))

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("log output is not JSON: %v", err)
	}
	if want := rec.Header().Get(httpserver.CorrelationIDHeader); entry[log.CorrelationIDKey] != want {
		t.Errorf("logged %s = %v, want %q", log.CorrelationIDKey, entry[log.CorrelationIDKey], want)
	}
}
