//go:build integration

package connect_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	schedulingv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/scheduling/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/scheduling/v1/schedulingv1connect"
	validationv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/validation/v1"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
	identityrpc "github.com/Zefanrakh/kue-preorder/internal/identity/connect"
	"github.com/Zefanrakh/kue-preorder/internal/identity/identitytest"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
	"github.com/Zefanrakh/kue-preorder/internal/scheduling"
	schedulingrpc "github.com/Zefanrakh/kue-preorder/internal/scheduling/connect"
	"github.com/Zefanrakh/kue-preorder/internal/scheduling/postgres"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

// Monday 5 October 2026, 10.00 WIB.
var now = time.Date(2026, time.October, 5, 10, 0, 0, 0, clock.Jakarta)

type server struct {
	url    string
	http   *http.Client
	issuer *identitytest.TokenIssuer
	roles  *identitytest.Repository
}

// newServer serves scheduling as cmd/api does, with the real auth
// interceptor, over HTTP, backed by a fresh database. Roles live in memory.
func newServer(t *testing.T) *server {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	roles := identitytest.NewRepository(dbtest.DefaultTenantID)
	issuer := identitytest.NewTokenIssuer(t)
	keys, err := identity.NewJWKS(t.Context(), issuer.JWKSURL, logger)
	if err != nil {
		t.Fatalf("NewJWKS() error = %v", err)
	}
	tenants, err := identity.ResolveSingleTenant(t.Context(), roles)
	if err != nil {
		t.Fatalf("ResolveSingleTenant() error = %v", err)
	}
	verifier := identity.NewTokenVerifier(keys, identitytest.Issuer, clock.NewFake(now))
	svc := scheduling.NewService(postgres.NewRepository(dbtest.New(t)), identity.NewService(roles, tenants), clock.NewFake(now))

	mux := http.NewServeMux()
	mux.Handle(schedulingv1connect.NewScheduleAdminServiceHandler(schedulingrpc.NewHandler(svc, logger),
		connect.WithInterceptors(identityrpc.NewAuthInterceptor(verifier, logger))))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &server{url: srv.URL, http: srv.Client(), issuer: issuer, roles: roles}
}

// as returns a client signed in as a new user holding roles.
func (s *server) as(t *testing.T, roles ...identity.Role) schedulingv1connect.ScheduleAdminServiceClient {
	t.Helper()
	user := uuid.New()
	if len(roles) > 0 {
		s.roles.Grant(dbtest.DefaultTenantID, user, roles...)
	}
	token := s.issuer.Sign(t, identitytest.Claims(user, now))
	return schedulingv1connect.NewScheduleAdminServiceClient(s.http, s.url, connect.WithInterceptors(
		connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				req.Header().Set("Authorization", "Bearer "+token)
				return next(ctx, req)
			}
		})))
}

func call[Req, Res any](t *testing.T, rpc func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error), msg *Req) *Res {
	t.Helper()
	res, err := rpc(t.Context(), connect.NewRequest(msg))
	if err != nil {
		t.Fatalf("%T: %v", msg, err)
	}
	return res.Msg
}

func fieldErrors(t *testing.T, err error, want connect.Code) map[string]string {
	t.Helper()
	var cerr *connect.Error
	if !errors.As(err, &cerr) || cerr.Code() != want {
		t.Fatalf("error = %v, want %v", err, want)
	}
	for _, d := range cerr.Details() {
		if v, derr := d.Value(); derr == nil {
			if fe, ok := v.(*validationv1.FieldErrors); ok {
				return fe.GetFields()
			}
		}
	}
	t.Fatalf("error %v has no FieldErrors detail", err)
	return nil
}

func TestScheduleAPI_Settings(t *testing.T) {
	s := newServer(t)
	boss, cook := s.as(t, identity.RoleOwner), s.as(t, identity.RoleKitchen)

	got := call(t, cook.GetScheduleSettings, &schedulingv1.GetScheduleSettingsRequest{}).GetSettings()
	if got.GetShoppingBufferHours() != 12 || got.DailyCapacityMinutes != nil || got.GetPickupWindowStart() != "09:00" ||
		got.GetPickupWindowEnd() != "17:00" || got.GetUpdatedAt() != nil {
		t.Errorf("GetScheduleSettings() = %v, want the defaults, never updated", got)
	}

	limit := int32(300)
	got = call(t, boss.UpdateScheduleSettings, &schedulingv1.UpdateScheduleSettingsRequest{
		ShoppingBufferHours: 8, DailyCapacityMinutes: &limit, PickupWindowStart: "07:30", PickupWindowEnd: "20:00",
	}).GetSettings()
	if got.GetShoppingBufferHours() != 8 || got.GetDailyCapacityMinutes() != 300 || got.GetPickupWindowStart() != "07:30" ||
		!got.GetUpdatedAt().AsTime().Equal(now) {
		t.Errorf("UpdateScheduleSettings() = %v", got)
	}

	_, err := cook.UpdateScheduleSettings(t.Context(), connect.NewRequest(&schedulingv1.UpdateScheduleSettingsRequest{
		ShoppingBufferHours: 1, PickupWindowStart: "09:00", PickupWindowEnd: "17:00",
	}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("kitchen UpdateScheduleSettings() code = %v, want PermissionDenied", connect.CodeOf(err))
	}

	_, err = boss.UpdateScheduleSettings(t.Context(), connect.NewRequest(&schedulingv1.UpdateScheduleSettingsRequest{
		ShoppingBufferHours: 12, PickupWindowStart: "sembilan", PickupWindowEnd: "08:00",
	}))
	if fields := fieldErrors(t, err, connect.CodeInvalidArgument); fields["pickup_window_start"] == "" {
		t.Errorf("fields = %v, want pickup_window_start", fields)
	}
	_, err = boss.UpdateScheduleSettings(t.Context(), connect.NewRequest(&schedulingv1.UpdateScheduleSettingsRequest{
		ShoppingBufferHours: 12, PickupWindowStart: "17:00", PickupWindowEnd: "09:00",
	}))
	if fields := fieldErrors(t, err, connect.CodeInvalidArgument); fields["pickup_window_end"] == "" {
		t.Errorf("fields = %v, want pickup_window_end after the start", fields)
	}
}

func TestScheduleAPI_ClosedDates(t *testing.T) {
	s := newServer(t)
	cook := s.as(t, identity.RoleKitchen)

	added := call(t, cook.AddClosedDate, &schedulingv1.AddClosedDateRequest{Date: "2026-10-12", Reason: "Ibu ke luar kota"}).GetClosedDate()
	if added.GetDate() != "2026-10-12" || added.GetReason() != "Ibu ke luar kota" {
		t.Errorf("AddClosedDate() = %v", added)
	}
	listed := call(t, cook.ListClosedDates, &schedulingv1.ListClosedDatesRequest{FromDate: "2026-10-01", ToDate: "2026-10-31"}).GetClosedDates()
	if len(listed) != 1 || listed[0].GetDate() != "2026-10-12" {
		t.Errorf("ListClosedDates() = %v, want the 12th", listed)
	}

	_, err := cook.AddClosedDate(t.Context(), connect.NewRequest(&schedulingv1.AddClosedDateRequest{Date: "2026-10-12", Reason: "Lagi"}))
	if fields := fieldErrors(t, err, connect.CodeAlreadyExists); fields["date"] == "" {
		t.Errorf("fields = %v, want date", fields)
	}
	_, err = cook.AddClosedDate(t.Context(), connect.NewRequest(&schedulingv1.AddClosedDateRequest{Date: "12/10/2026", Reason: "Libur"}))
	if fields := fieldErrors(t, err, connect.CodeInvalidArgument); fields["date"] != "Tanggal tidak valid, tulis seperti 2026-10-05." {
		t.Errorf("fields = %v, want the date format", fields)
	}
	_, err = cook.ListClosedDates(t.Context(), connect.NewRequest(&schedulingv1.ListClosedDatesRequest{FromDate: "kemarin", ToDate: "besok"}))
	if fields := fieldErrors(t, err, connect.CodeInvalidArgument); fields["from_date"] == "" || fields["to_date"] == "" {
		t.Errorf("fields = %v, want both dates", fields)
	}

	call(t, cook.RemoveClosedDate, &schedulingv1.RemoveClosedDateRequest{Date: "2026-10-12"})
	_, err = cook.RemoveClosedDate(t.Context(), connect.NewRequest(&schedulingv1.RemoveClosedDateRequest{Date: "2026-10-12"}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("RemoveClosedDate(again) code = %v, want NotFound", connect.CodeOf(err))
	}

	_, err = s.as(t).ListClosedDates(t.Context(), connect.NewRequest(&schedulingv1.ListClosedDatesRequest{FromDate: "2026-10-01", ToDate: "2026-10-31"}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("customer ListClosedDates() code = %v, want PermissionDenied", connect.CodeOf(err))
	}
}
