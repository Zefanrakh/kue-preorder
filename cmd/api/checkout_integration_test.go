//go:build integration

package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/protobuf/types/known/timestamppb"

	ordersv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/orders/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/orders/v1/ordersv1connect"
	validationv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/validation/v1"
	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	catalogpg "github.com/Zefanrakh/kue-preorder/internal/catalog/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/identity/identitytest"
	identitypg "github.com/Zefanrakh/kue-preorder/internal/identity/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
	"github.com/Zefanrakh/kue-preorder/internal/platform/ratelimit"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func wib(day, hour, minute int) time.Time {
	return time.Date(2026, time.October, day, hour, minute, 0, 0, clock.Jakarta)
}

type ownerOf uuid.UUID

func (o ownerOf) Principal(context.Context) (identity.Principal, error) {
	return identity.Principal{AuthUserID: uuid.New(), TenantID: uuid.UUID(o), Roles: []identity.Role{identity.RoleOwner}}, nil
}

// wiredCheckout serves the API exactly as serve wires it, over a fresh
// database holding one donut at 8,000 rupiah (90 minutes of production, 12
// hours' notice) with Wednesday 7 October closed. It is Monday 5 October,
// 10.00.
func wiredCheckout(t *testing.T, opts ...connect.ClientOption) (ordersv1connect.CheckoutServiceClient, catalog.Variant) {
	t.Helper()
	ctx := t.Context()
	d := dbtest.New(t)
	clk := clock.NewFake(wib(5, 10, 0))
	logger := slog.New(slog.DiscardHandler)

	cms := catalog.NewService(catalogpg.NewRepository(d), ownerOf(dbtest.DefaultTenantID), clk)
	product, err := cms.CreateProduct(ctx, catalog.ProductInput{Name: "Donut", Slug: "donut", Active: true})
	noErr(t, err)
	donut, err := cms.CreateVariant(ctx, product.ID, catalog.VariantInput{SKU: "DNT", Name: "Coklat", ProductionMinutes: 90, MinNoticeHours: 12, Active: true}, 8000)
	noErr(t, err)
	_, err = d.Pool().Exec(ctx, "insert into closed_dates (tenant_id, date, reason, created_at) values ($1, '2026-10-07', 'Libur', now())", dbtest.DefaultTenantID)
	noErr(t, err)

	identityRepo := identitypg.NewRepository(d.Pool())
	tenants, err := identity.ResolveSingleTenant(ctx, identityRepo)
	noErr(t, err)
	issuer := identitytest.NewTokenIssuer(t)
	keys, err := identity.NewJWKS(ctx, issuer.JWKSURL, logger)
	noErr(t, err)

	deps := wire(d, identity.NewService(identityRepo, tenants), tenants, clk, logger)
	deps.tracerProvider = sdktrace.NewTracerProvider()
	deps.verifier = identity.NewTokenVerifier(keys, identitytest.Issuer, clk)
	deps.limiter = ratelimit.New(clk, rateLimits)
	handler, err := newHandler(deps)
	noErr(t, err)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return ordersv1connect.NewCheckoutServiceClient(srv.Client(), srv.URL, opts...), donut
}

func noErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func quote(t *testing.T, client ordersv1connect.CheckoutServiceClient, variant string, quantity int32, pickup time.Time) (*connect.Response[ordersv1.QuoteOrderResponse], error) {
	t.Helper()
	return client.QuoteOrder(t.Context(), connect.NewRequest(&ordersv1.QuoteOrderRequest{
		Items: []*ordersv1.CartItem{{VariantId: variant, Quantity: quantity}}, PickupAt: timestamppb.New(pickup),
	}))
}

// Sent as GET, as the storefront does: through the rate limit, the cache
// headers, and every real repository.
func TestQuoteOrder_Schedule(t *testing.T) {
	client, donut := wiredCheckout(t, connect.WithHTTPGet())

	res, err := quote(t, client, donut.ID.String(), 3, wib(8, 9, 0))

	noErr(t, err)
	got, s := res.Msg, res.Msg.GetSchedule()
	if got.GetTotalIdr() != 24000 || got.GetDpRequiredIdr() != 12000 || len(got.GetItems()) != 1 || got.GetItems()[0].GetProductName() != "Donut" {
		t.Errorf("QuoteOrder() = %v, want 3 × 8,000 with a DP of 12,000", got)
	}
	if s == nil || s.GetProductionDate() != "2026-10-08" || !s.GetDpDueAt().AsTime().Equal(wib(5, 13, 0)) ||
		!s.GetBalanceDueAt().AsTime().Equal(wib(7, 19, 30)) || s.GetFullPaymentRequired() {
		t.Errorf("schedule = %v, want Thursday's batch, the DP within 3 hours, the balance Wednesday 19.30", s)
	}
	if cc := res.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store: a quote depends on the moment", cc)
	}
}

func TestQuoteOrder_ClosedDaySuggestsAnother(t *testing.T) {
	client, donut := wiredCheckout(t)

	res, err := quote(t, client, donut.ID.String(), 1, wib(7, 9, 0))

	noErr(t, err)
	r := res.Msg.GetRejection()
	if r == nil || r.GetProblem() != ordersv1.PickupProblem_PICKUP_PROBLEM_CLOSED || r.GetMessage() == "" ||
		!r.GetSuggestedPickupAt().AsTime().Equal(wib(8, 9, 0)) || res.Msg.GetDpRequiredIdr() != 0 || res.Msg.GetTotalIdr() != 8000 {
		t.Errorf("QuoteOrder() = %v, want closed with Thursday 09.00 suggested, the price but no DP", res.Msg)
	}
}

func TestQuoteOrder_MalformedVariant(t *testing.T) {
	client, _ := wiredCheckout(t)

	_, err := quote(t, client, "bukan-uuid", 1, wib(8, 9, 0))

	var cerr *connect.Error
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("QuoteOrder() error = %v, want InvalidArgument", err)
	}
	for _, d := range cerr.Details() {
		if v, _ := d.Value(); v != nil {
			if fe, ok := v.(*validationv1.FieldErrors); ok && fe.GetFields()["items"] != "" {
				return
			}
		}
	}
	t.Errorf("error %v has no FieldErrors on items", err)
}
