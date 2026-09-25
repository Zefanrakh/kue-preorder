package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	catalogv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/catalog/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/catalog/v1/catalogv1connect"
	identityv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/identity/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/identity/v1/identityv1connect"
	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/identity/identitytest"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/httpserver"
	"github.com/Zefanrakh/kue-preorder/internal/platform/log"
)

var now = time.Date(2026, 9, 25, 3, 0, 0, 0, time.UTC)

// syncBuffer is a log sink safe for the server's goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// records returns the JSON log records written so far.
func (b *syncBuffer) records(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(b.buf.Bytes()))
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("log line is not JSON: %s", sc.Text())
		}
		out = append(out, rec)
	}
	return out
}

type pingerFunc func(context.Context) error

func (f pingerFunc) Ping(ctx context.Context) error { return f(ctx) }

type apiServer struct {
	url     string
	client  identityv1connect.IdentityServiceClient
	catalog catalogv1connect.CatalogAdminServiceClient
	shop    catalogv1connect.StorefrontServiceClient
	spans   *tracetest.SpanRecorder
	logs    *syncBuffer
	issuer  *identitytest.TokenIssuer
}

// newAPIServer serves newHandler, the production wiring, with an in-memory
// repository, a local token issuer, and a span recorder.
func newAPIServer(t *testing.T, db httpserver.Pinger) *apiServer {
	t.Helper()
	logs := &syncBuffer{}
	logger := log.New(logs, slog.LevelDebug)
	spans := tracetest.NewSpanRecorder()
	tenant := uuid.New()
	repo := identitytest.NewRepository(tenant)
	issuer := identitytest.NewTokenIssuer(t)

	keys, err := identity.NewJWKS(t.Context(), issuer.JWKSURL, logger)
	if err != nil {
		t.Fatalf("NewJWKS() error = %v", err)
	}
	tenants, err := identity.ResolveSingleTenant(t.Context(), repo)
	if err != nil {
		t.Fatalf("ResolveSingleTenant() error = %v", err)
	}
	identitySvc := identity.NewService(repo, tenants)
	handler, err := newHandler(handlerDeps{
		logger:         logger,
		tracerProvider: sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)),
		verifier:       identity.NewTokenVerifier(keys, identitytest.Issuer, clock.NewFake(now)),
		identity:       identitySvc,
		// No catalog repository: these tests never reach the database.
		// internal/catalog/connect tests the catalog over a real one.
		catalog:    catalog.NewService(nil, identitySvc, clock.NewFake(now)),
		storefront: catalog.NewStorefront(nil, tenants),
		db:         db,
	})
	if err != nil {
		t.Fatalf("newHandler() error = %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &apiServer{
		url:     srv.URL,
		client:  identityv1connect.NewIdentityServiceClient(srv.Client(), srv.URL),
		catalog: catalogv1connect.NewCatalogAdminServiceClient(srv.Client(), srv.URL),
		shop:    catalogv1connect.NewStorefrontServiceClient(srv.Client(), srv.URL, connect.WithHTTPGet()),
		spans:   spans,
		logs:    logs,
		issuer:  issuer,
	}
}

func (s *apiServer) whoAmI(t *testing.T, token string) error {
	t.Helper()
	req := connect.NewRequest(&identityv1.WhoAmIRequest{})
	req.Header().Set("Authorization", "Bearer "+token)
	_, err := s.client.WhoAmI(t.Context(), req)
	return err
}

// get returns the status and headers of GET path.
func (s *apiServer) get(t *testing.T, path string) (int, http.Header) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.url+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	_ = res.Body.Close()
	return res.StatusCode, res.Header
}

func healthyDB() httpserver.Pinger { return pingerFunc(func(context.Context) error { return nil }) }

const whoAmISpan = "kuepreorder.identity.v1.IdentityService/WhoAmI"

func (s *apiServer) findSpan(name string) sdktrace.ReadOnlySpan {
	for _, sp := range s.spans.Ended() {
		if sp.Name() == name {
			return sp
		}
	}
	return nil
}

func TestAPI_TracesRPCs(t *testing.T) {
	s := newAPIServer(t, healthyDB())

	if err := s.whoAmI(t, s.issuer.Sign(t, identitytest.Claims(uuid.New(), now))); err != nil {
		t.Fatalf("WhoAmI() error = %v", err)
	}

	span := s.findSpan(whoAmISpan)
	if span == nil {
		t.Fatalf("no %q span; got %d spans", whoAmISpan, len(s.spans.Ended()))
	}
	if span.SpanKind() != trace.SpanKindServer {
		t.Errorf("span kind = %v, want server", span.SpanKind())
	}
}

// Tracing runs before auth: a rejected token still leaves a span, and the
// rejection log line carries that span's trace id.
func TestAPI_RejectedTokenIsTracedAndLinkedFromTheLog(t *testing.T) {
	s := newAPIServer(t, healthyDB())
	expired := s.issuer.Sign(t, identitytest.Claims(uuid.New(), now.Add(-2*time.Hour)))

	err := s.whoAmI(t, expired)

	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("WhoAmI() code = %v, want Unauthenticated", connect.CodeOf(err))
	}
	span := s.findSpan(whoAmISpan)
	if span == nil {
		t.Fatal("rejected request left no span; the tracing interceptor must run before auth")
	}
	var rejection map[string]any
	for _, rec := range s.logs.records(t) {
		if rec["msg"] == "access token rejected" {
			rejection = rec
		}
	}
	if rejection == nil {
		t.Fatal("no 'access token rejected' log record")
	}
	if rejection[log.TraceIDKey] != span.SpanContext().TraceID().String() {
		t.Errorf("log trace_id = %v, want the span's %s", rejection[log.TraceIDKey], span.SpanContext().TraceID())
	}
	if rejection[log.CorrelationIDKey] == nil {
		t.Error("log record has no correlation_id")
	}
}

func TestAPI_HealthChecksAreNotTraced(t *testing.T) {
	s := newAPIServer(t, healthyDB())

	for _, path := range []string{"/healthz", "/readyz"} {
		if status, _ := s.get(t, path); status != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, status)
		}
	}

	if n := len(s.spans.Ended()); n != 0 {
		t.Errorf("health checks produced %d spans, want 0", n)
	}
}

func TestAPI_RecoversPanicsWithCorrelatedErrorLog(t *testing.T) {
	s := newAPIServer(t, pingerFunc(func(context.Context) error { panic("pool is nil") }))

	status, header := s.get(t, "/readyz")

	if status != http.StatusInternalServerError {
		t.Errorf("GET /readyz = %d, want 500", status)
	}
	id := header.Get(httpserver.CorrelationIDHeader)
	for _, rec := range s.logs.records(t) {
		if rec["msg"] == "panic while serving request" {
			if rec["level"] != "ERROR" || rec[log.CorrelationIDKey] != id || !strings.Contains(rec["error"].(string), "pool is nil") {
				t.Errorf("panic record = %v, want ERROR with correlation_id %s and the panic value", rec, id)
			}
			return
		}
	}
	t.Error("panic was not logged")
}

// The catalog is mounted behind the same interceptors: a signed-in user
// without a staff role is refused, and a caller without a token too.
func TestAPI_ServesTheCatalog(t *testing.T) {
	s := newAPIServer(t, healthyDB())
	tests := []struct {
		name  string
		token string
		want  connect.Code
	}{
		{"anonymous", "", connect.CodeUnauthenticated},
		{"no staff role", s.issuer.Sign(t, identitytest.Claims(uuid.New(), now)), connect.CodePermissionDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := connect.NewRequest(&catalogv1.ListProductsRequest{})
			if tt.token != "" {
				req.Header().Set("Authorization", "Bearer "+tt.token)
			}
			_, err := s.catalog.ListProducts(t.Context(), req)
			if connect.CodeOf(err) != tt.want {
				t.Errorf("ListProducts() code = %v, want %v", connect.CodeOf(err), tt.want)
			}
		})
	}
	if s.findSpan("kuepreorder.catalog.v1.CatalogAdminService/ListProducts") == nil {
		t.Error("catalog request left no span")
	}
}

// The storefront is mounted behind the same interceptors: open to anonymous
// callers, but a broken session still fails rather than turning anonymous.
func TestAPI_ServesTheStorefront(t *testing.T) {
	s := newAPIServer(t, healthyDB())
	req := connect.NewRequest(&catalogv1.ListShopProductsRequest{})
	req.Header().Set("Authorization", "Bearer not-a-jwt")

	_, err := s.shop.ListShopProducts(t.Context(), req)

	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("ListShopProducts() code = %v, want Unauthenticated", connect.CodeOf(err))
	}
	if s.findSpan("kuepreorder.catalog.v1.StorefrontService/ListShopProducts") == nil {
		t.Error("storefront request left no span")
	}
}
