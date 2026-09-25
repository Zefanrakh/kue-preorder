package telemetry

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/getsentry/sentry-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/Zefanrakh/kue-preorder/internal/platform/log"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestSetup_OffByDefault(t *testing.T) {
	tel, err := Setup(t.Context(), Config{Service: "api", Environment: "development"}, discardLogger())
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	_, span := tel.TracerProvider().Tracer("test").Start(t.Context(), "op")
	if span.IsRecording() || span.SpanContext().IsValid() {
		t.Error("span is recorded although tracing is off")
	}
	span.End()
	base := discardLogger()
	if tel.Logger(base) != base {
		t.Error("Logger() wrapped the logger although Sentry is off")
	}
	if err := tel.Shutdown(t.Context()); err != nil {
		t.Errorf("Shutdown() error = %v", err)
	}
}

// otlpCollector records the trace export requests it receives.
type otlpCollector struct {
	mu       sync.Mutex
	paths    []string
	requests []*coltracepb.ExportTraceServiceRequest
}

func (c *otlpCollector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body = gz
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req coltracepb.ExportTraceServiceRequest
	if err := proto.Unmarshal(raw, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	c.paths = append(c.paths, r.URL.Path)
	c.requests = append(c.requests, &req)
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/x-protobuf")
}

func TestSetup_ExportsSpansOverOTLP(t *testing.T) {
	collector := &otlpCollector{}
	srv := httptest.NewServer(collector)
	t.Cleanup(srv.Close)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)
	t.Cleanup(func() { otel.SetTracerProvider(noop.NewTracerProvider()) })

	tel, err := Setup(t.Context(), Config{Service: "api", Environment: "staging", OTLPEndpoint: srv.URL}, discardLogger())
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	_, span := tel.TracerProvider().Tracer("test").Start(t.Context(), "kuepreorder.identity.v1.IdentityService/WhoAmI")
	span.End()
	if err := tel.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	collector.mu.Lock()
	defer collector.mu.Unlock()
	if len(collector.requests) == 0 {
		t.Fatal("collector received no export request; Shutdown must flush buffered spans")
	}
	if !slices.Contains(collector.paths, "/v1/traces") {
		t.Errorf("export paths = %v, want /v1/traces", collector.paths)
	}
	rs := collector.requests[0].GetResourceSpans()[0]
	attrs := map[string]string{}
	for _, kv := range rs.GetResource().GetAttributes() {
		attrs[kv.GetKey()] = kv.GetValue().GetStringValue()
	}
	if attrs["service.name"] != "api" || attrs["deployment.environment.name"] != "staging" {
		t.Errorf("resource attributes = %v, want service.name=api and deployment.environment.name=staging", attrs)
	}
	if got := rs.GetScopeSpans()[0].GetSpans()[0].GetName(); got != "kuepreorder.identity.v1.IdentityService/WhoAmI" {
		t.Errorf("span name = %q", got)
	}
}

func newSentryTelemetry(t *testing.T) (*Telemetry, *sentry.MockTransport) {
	t.Helper()
	transport := &sentry.MockTransport{}
	tel, err := Setup(t.Context(), Config{
		Service:         "api",
		Environment:     "test",
		SentryDSN:       "https://publickey@o1.ingest.sentry.io/42",
		sentryTransport: transport,
	}, discardLogger())
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	return tel, transport
}

func TestLogger_ReportsErrorRecordsToSentry(t *testing.T) {
	tel, transport := newSentryTelemetry(t)
	var out bytes.Buffer
	logger := tel.Logger(log.New(&out, slog.LevelInfo)).With(slog.String("module", "identity"))
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:  trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
	})
	ctx := trace.ContextWithSpanContext(log.WithCorrelationID(t.Context(), "req-1"), sc)

	logger.ErrorContext(ctx, "identity request failed",
		slog.Any("error", errors.New("list staff roles: connection refused")),
		slog.Group("db", slog.String("op", "ListStaffRoles")))

	events := transport.Events()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	e := events[0]
	if e.Level != sentry.LevelError || e.Message != "identity request failed" {
		t.Errorf("event level/message = %q/%q", e.Level, e.Message)
	}
	if e.Tags[log.CorrelationIDKey] != "req-1" || e.Tags[log.TraceIDKey] != sc.TraceID().String() {
		t.Errorf("tags = %v, want correlation_id and trace_id", e.Tags)
	}
	fields := e.Contexts["log"]
	if fields["module"] != "identity" || fields["db.op"] != "ListStaffRoles" || fields["error"] != "list staff roles: connection refused" {
		t.Errorf("log context = %v", fields)
	}
	if len(e.Exception) != 1 || e.Exception[0].Value != "list staff roles: connection refused" {
		t.Fatalf("exception = %+v, want the logged error", e.Exception)
	}
	if !slices.Contains(e.Fingerprint, "identity request failed") {
		t.Errorf("fingerprint = %v, want grouping by message and call site", e.Fingerprint)
	}
	frames := e.Exception[0].Stacktrace.Frames
	if !slices.ContainsFunc(frames, func(f sentry.Frame) bool {
		return strings.Contains(f.Function, "TestLogger_ReportsErrorRecordsToSentry")
	}) {
		t.Error("stack trace misses the code that logged")
	}
	if slices.ContainsFunc(frames, func(f sentry.Frame) bool {
		return f.Module == "log/slog" || strings.HasPrefix(f.Function, "(*sentryHandler).")
	}) {
		t.Error("stack trace still contains logging frames (log/slog or the Sentry handler)")
	}
	if u := e.User; u.ID != "" || u.Email != "" || u.IPAddress != "" || u.Username != "" || e.Request != nil {
		t.Errorf("event carries personal data: user %+v, request %+v", e.User, e.Request)
	}
	if !strings.Contains(out.String(), `"correlation_id":"req-1"`) {
		t.Errorf("record not passed on to the JSON log; got %s", out.String())
	}
}

// Sentry's defaults collect user info, headers, cookies, bodies, and query
// parameters; the client must be configured to collect none of them.
func TestSetup_SentryCollectsNoPersonalData(t *testing.T) {
	tel, _ := newSentryTelemetry(t)

	dc := tel.sentry.Client().Options().DataCollection
	if dc == nil {
		t.Fatal("DataCollection is nil: Sentry would use its collecting defaults")
	}
	if !dc.UserInfo.IsSet || dc.UserInfo.Value {
		t.Error("UserInfo collection is not explicitly off")
	}
	if dc.HTTPBodies == nil || len(dc.HTTPBodies) != 0 {
		t.Errorf("HTTPBodies = %v, want an empty non-nil list (nil means all)", dc.HTTPBodies)
	}
	for name, b := range map[string]*sentry.KeyValueCollectionBehavior{
		"cookies":          dc.Cookies,
		"request headers":  dc.HTTPHeaders.Request,
		"response headers": dc.HTTPHeaders.Response,
		"query params":     dc.QueryParams,
	} {
		if b == nil || b.Mode != sentry.CollectionOff {
			t.Errorf("%s collection is not off", name)
		}
	}
}

func TestLogger_DoesNotReportBelowError(t *testing.T) {
	tel, transport := newSentryTelemetry(t)
	logger := tel.Logger(discardLogger())

	logger.InfoContext(t.Context(), "api listening")
	logger.WarnContext(t.Context(), "access token rejected")

	if n := len(transport.Events()); n != 0 {
		t.Errorf("events = %d, want 0 for info and warn", n)
	}
}

func TestSetup_RejectsInvalidDSNWithoutLeakingIt(t *testing.T) {
	_, err := Setup(t.Context(), Config{Service: "api", SentryDSN: "not-a-dsn-secretkey9"}, discardLogger())

	if err == nil {
		t.Fatal("Setup() error = nil, want error")
	}
	if strings.Contains(err.Error(), "secretkey9") {
		t.Errorf("error leaks the DSN: %v", err)
	}
}
