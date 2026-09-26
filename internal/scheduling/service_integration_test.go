//go:build integration

package scheduling_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
	"github.com/Zefanrakh/kue-preorder/internal/scheduling"
	"github.com/Zefanrakh/kue-preorder/internal/scheduling/postgres"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

type principals struct {
	p   identity.Principal
	err error
}

func (s principals) Principal(context.Context) (identity.Principal, error) { return s.p, s.err }

// orderCounts stands in for the orders module: the active orders per day.
type orderCounts map[clock.Date]int

func (c orderCounts) ActiveOrders(context.Context, uuid.UUID, clock.Date, clock.Date) (map[clock.Date]int, error) {
	return c, nil
}

// service returns the service as a caller with roles in tenant; no roles
// means a signed-in customer.
func service(d *db.DB, tenant uuid.UUID, roles ...identity.Role) *scheduling.Service {
	p := identity.Principal{AuthUserID: uuid.New(), TenantID: tenant, Roles: roles}
	return scheduling.NewService(postgres.NewRepository(d), principals{p: p}, orderCounts{date(12): 3}, clock.NewFake(monday10))
}

func noErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func validationFields(t *testing.T, err error) (map[string]string, bool) {
	t.Helper()
	var v *scheduling.ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
	return v.Fields, v.Conflict
}

func TestService_Settings(t *testing.T) {
	d := dbtest.New(t)
	boss := service(d, dbtest.DefaultTenantID, identity.RoleOwner)
	ctx := t.Context()

	got, err := boss.Settings(ctx)
	noErr(t, err)
	if got != scheduling.DefaultSettings() {
		t.Errorf("Settings() before any change = %+v, want the defaults", got)
	}

	limit := int32(300)
	want := scheduling.Settings{ShoppingBufferHours: 8, DailyCapacityMinutes: &limit, PickupStart: 7*60 + 30, PickupEnd: 20 * 60}
	saved, err := boss.UpdateSettings(ctx, want)
	noErr(t, err)
	got, err = boss.Settings(ctx)
	noErr(t, err)
	for _, s := range []scheduling.Settings{saved, got} {
		if s.ShoppingBufferHours != 8 || s.DailyCapacityMinutes == nil || *s.DailyCapacityMinutes != 300 ||
			s.PickupStart.String() != "07:30" || s.PickupEnd.String() != "20:00" || !s.UpdatedAt.Equal(monday10) {
			t.Errorf("settings = %+v, want what was saved, stamped now", s)
		}
	}

	// Removing the limit stores null, not zero.
	want.DailyCapacityMinutes = nil
	got, err = boss.UpdateSettings(ctx, want)
	noErr(t, err)
	if got.DailyCapacityMinutes != nil {
		t.Errorf("DailyCapacityMinutes = %d, want no limit", *got.DailyCapacityMinutes)
	}
}

func TestService_SettingsValidation(t *testing.T) {
	boss := service(dbtest.New(t), dbtest.DefaultTenantID, identity.RoleOwner)
	zero := int32(0)

	_, err := boss.UpdateSettings(t.Context(), scheduling.Settings{
		ShoppingBufferHours: 200, DailyCapacityMinutes: &zero, PickupStart: 17 * 60, PickupEnd: 9 * 60,
	})

	fields, _ := validationFields(t, err)
	for _, f := range []string{"shopping_buffer_hours", "daily_capacity_minutes", "pickup_window_end"} {
		if fields[f] == "" {
			t.Errorf("fields = %v, want %s", fields, f)
		}
	}
}

func TestService_Roles(t *testing.T) {
	d := dbtest.New(t)
	tenant := dbtest.DefaultTenantID
	ctx := t.Context()
	kitchen := service(d, tenant, identity.RoleKitchen)
	customer := service(d, tenant)
	anonymous := scheduling.NewService(postgres.NewRepository(d), principals{err: identity.ErrUnauthenticated}, orderCounts{}, clock.NewFake(monday10))

	if _, err := kitchen.Settings(ctx); err != nil {
		t.Errorf("kitchen Settings() error = %v, want allowed", err)
	}
	if _, err := kitchen.UpdateSettings(ctx, scheduling.DefaultSettings()); !errors.Is(err, scheduling.ErrForbidden) {
		t.Errorf("kitchen UpdateSettings() error = %v, want ErrForbidden: only the owner sets the rules", err)
	}
	if _, err := kitchen.AddClosedDate(ctx, date(9), "Ibu sakit"); err != nil {
		t.Errorf("kitchen AddClosedDate() error = %v, want allowed", err)
	}
	if _, err := customer.Settings(ctx); !errors.Is(err, scheduling.ErrForbidden) {
		t.Errorf("customer Settings() error = %v, want ErrForbidden", err)
	}
	if _, err := anonymous.ClosedDates(ctx, date(1), date(31)); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Errorf("anonymous ClosedDates() error = %v, want ErrUnauthenticated", err)
	}
}

func TestService_ClosedDates(t *testing.T) {
	d := dbtest.New(t)
	cook := service(d, dbtest.DefaultTenantID, identity.RoleKitchen)
	ctx := t.Context()

	for _, day := range []int{20, 5, 12} { // today (the 5th) may be closed
		_, err := cook.AddClosedDate(ctx, date(day), "  Libur  ")
		noErr(t, err)
	}
	got, err := cook.ClosedDates(ctx, date(5), date(20))
	noErr(t, err)
	if len(got) != 3 || got[0].Date != date(5) || got[1].Date != date(12) || got[2].Date != date(20) ||
		got[0].Reason != "Libur" || !got[0].CreatedAt.Equal(monday10) {
		t.Errorf("ClosedDates() = %+v, want 5, 12, 20 October in order, trimmed reasons", got)
	}
	// The 12th still holds 3 active orders: it is on hold, not yet a day off.
	if len(got) == 3 && (got[1].ActiveOrders != 3 || got[0].ActiveOrders != 0 || got[2].ActiveOrders != 0) {
		t.Errorf("active orders = %d %d %d, want 0 3 0", got[0].ActiveOrders, got[1].ActiveOrders, got[2].ActiveOrders)
	}
	if got, _ := cook.ClosedDates(ctx, date(6), date(19)); len(got) != 1 {
		t.Errorf("ClosedDates(6..19) = %+v, want only the 12th", got)
	}

	_, err = cook.AddClosedDate(ctx, date(12), "Lagi")
	if fields, conflict := validationFields(t, err); !conflict || fields["date"] == "" {
		t.Errorf("duplicate AddClosedDate() = %v, want a conflict on date", err)
	}
	_, err = cook.AddClosedDate(ctx, date(4), "Kemarin")
	if fields, _ := validationFields(t, err); fields["date"] == "" {
		t.Errorf("AddClosedDate(yesterday) fields = %v, want date", fields)
	}
	_, err = cook.AddClosedDate(ctx, date(25), " ")
	if fields, _ := validationFields(t, err); fields["reason"] == "" {
		t.Errorf("AddClosedDate(no reason) fields = %v, want reason", fields)
	}
	_, err = cook.ClosedDates(ctx, date(20), date(5))
	if fields, _ := validationFields(t, err); fields["to_date"] == "" {
		t.Errorf("ClosedDates(backwards) fields = %v, want to_date", fields)
	}
	_, err = cook.ClosedDates(ctx, date(1), date(1).AddDays(367))
	if fields, _ := validationFields(t, err); fields["to_date"] == "" {
		t.Errorf("ClosedDates(over a year) fields = %v, want to_date", fields)
	}

	noErr(t, cook.RemoveClosedDate(ctx, date(12)))
	if err := cook.RemoveClosedDate(ctx, date(12)); !errors.Is(err, scheduling.ErrNotFound) {
		t.Errorf("RemoveClosedDate(again) error = %v, want ErrNotFound", err)
	}
}

func TestService_ClosedDatesAreTenantScoped(t *testing.T) {
	d := dbtest.New(t)
	other := dbtest.CreateTenant(t, d, "Toko Lain")
	mine := service(d, dbtest.DefaultTenantID, identity.RoleOwner)
	theirs := service(d, other, identity.RoleOwner)
	ctx := t.Context()
	_, err := theirs.AddClosedDate(ctx, date(9), "Libur mereka")
	noErr(t, err)

	if got, _ := mine.ClosedDates(ctx, date(1), date(31)); len(got) != 0 {
		t.Errorf("ClosedDates() = %+v, want none of the other tenant's", got)
	}
	if err := mine.RemoveClosedDate(ctx, date(9)); !errors.Is(err, scheduling.ErrNotFound) {
		t.Errorf("RemoveClosedDate(other tenant's) error = %v, want ErrNotFound", err)
	}
	// Each tenant may close the same day.
	if _, err := mine.AddClosedDate(ctx, date(9), "Libur kami"); err != nil {
		t.Errorf("AddClosedDate(same day, own tenant) error = %v", err)
	}
}

// Checkout reads the rules of a tenant without a signed-in person.
func TestReader(t *testing.T) {
	d := dbtest.New(t)
	reader := scheduling.NewReader(postgres.NewRepository(d))
	ctx := t.Context()

	if s, err := reader.Settings(ctx, dbtest.DefaultTenantID); err != nil || s != scheduling.DefaultSettings() {
		t.Errorf("Settings() = %+v, %v; want the defaults", s, err)
	}
	owner := service(d, dbtest.DefaultTenantID, identity.RoleOwner)
	_, err := owner.AddClosedDate(ctx, date(9), "Libur")
	noErr(t, err)
	_, err = owner.AddClosedDate(ctx, date(20), "Libur lagi")
	noErr(t, err)

	closed, err := reader.ClosedDates(ctx, dbtest.DefaultTenantID, date(5), date(10))
	noErr(t, err)
	if len(closed) != 1 || closed[date(9)] != "Libur" {
		t.Errorf("ClosedDates() = %v, want only the 9th with its reason", closed)
	}
}
