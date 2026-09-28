//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/orders"
	"github.com/Zefanrakh/kue-preorder/internal/orders/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/payments"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/audit"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func wib(day, hour, minute int) time.Time {
	return time.Date(2026, time.October, day, hour, minute, 0, 0, clock.Jakarta)
}

func date(day int) clock.Date { return clock.Date{Year: 2026, Month: time.October, Day: day} }

var now = wib(5, 10, 0)

func newCustomer(t *testing.T, d *db.DB, tenant uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := d.Pool().QueryRow(t.Context(), "insert into customers (tenant_id, auth_user_id, name, phone) values ($1, $2, 'Sari', '+6281234567890') returning id", tenant, uuid.New()).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool().Exec(t.Context(), "insert into channels (tenant_id, key, name) values ($1, 'web', 'Website') on conflict do nothing", tenant); err != nil {
		t.Fatal(err)
	}
	return id
}

// order is a donut order picked up on day at 09.00, produced from 07.30.
func order(customer uuid.UUID, status orders.Status, day int, cutoff time.Time) orders.Order {
	pickup := wib(day, 9, 0)
	return orders.Order{
		ID: uuid.New(), CustomerID: customer, Code: orders.NewCode(), Status: status, Payment: payments.Unpaid, Fulfillment: orders.Pickup,
		Items:       []orders.QuotedItem{{VariantID: uuid.Nil, ProductName: "Donut", VariantName: "Coklat", Quantity: 2, UnitPriceIDR: 8000, LineTotalIDR: 16000, ProductionMinutes: 90, MinNoticeHours: 12}},
		SubtotalIDR: 16000, TotalIDR: 16000, DPRequiredIDR: 8000,
		PickupAt: pickup, ProductionStart: pickup.Add(-90 * time.Minute), ProductionDate: date(day),
		ShoppingCutoffAt: cutoff, DPDueAt: cutoff, BalanceDueAt: cutoff,
		Notes: "Tulisan: Selamat", CustomerName: "Sari", CustomerPhone: "+6281234567890", TermsVersion: orders.TermsVersion,
	}
}

// insert stores o, with a real variant so the item's foreign key holds.
func insert(t *testing.T, d *db.DB, repo *postgres.Repository, tenant uuid.UUID, o orders.Order) orders.Order {
	t.Helper()
	ctx := t.Context()
	var product, variant uuid.UUID
	if err := d.Pool().QueryRow(ctx, "insert into products (tenant_id, name, slug) values ($1, 'Donut', $2) returning id", tenant, "donut-"+uuid.NewString()[:8]).Scan(&product); err != nil {
		t.Fatal(err)
	}
	if err := d.Pool().QueryRow(ctx, `insert into product_variants (tenant_id, product_id, sku, name, price_idr, production_minutes, min_notice_hours)
		values ($1, $2, $3, 'Coklat', 8000, 90, 12) returning id`, tenant, product, "SKU-"+uuid.NewString()[:8]).Scan(&variant); err != nil {
		t.Fatal(err)
	}
	o.Items[0].VariantID = variant
	ok, err := repo.Insert(ctx, tenant, o, uuid.New(), now)
	if err != nil || !ok {
		t.Fatalf("Insert() = %v, %v", ok, err)
	}
	return o
}

func TestInsertAndRead(t *testing.T) {
	d := dbtest.New(t)
	repo := postgres.NewRepository(d)
	ctx := t.Context()
	tenant := dbtest.DefaultTenantID
	customer := newCustomer(t, d, tenant)
	o := insert(t, d, repo, tenant, order(customer, orders.AwaitingDP, 7, wib(6, 19, 30)))

	got, err := repo.Get(ctx, tenant, o.ID)
	if err != nil || got.Code != o.Code || got.TotalIDR != 16000 || got.Notes != "Tulisan: Selamat" || got.CustomerPhone != "+6281234567890" ||
		got.ProductionDate != date(7) || !got.DPDueAt.Equal(wib(6, 19, 30)) || len(got.Items) != 1 || got.Items[0].LineTotalIDR != 16000 {
		t.Errorf("Get() = %+v, %v", got, err)
	}
	if byCode, err := repo.FindCustomerOrder(ctx, tenant, customer, o.Code); err != nil || byCode.ID != o.ID {
		t.Errorf("FindCustomerOrder() = %v, %v", byCode.ID, err)
	}
	if _, err := repo.FindCustomerOrder(ctx, tenant, uuid.New(), o.Code); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("FindCustomerOrder(another customer) error = %v, want ErrNotFound", err)
	}
	list, err := repo.ListCustomerOrders(ctx, tenant, customer, 10)
	if err != nil || len(list) != 1 || list[0].FirstItem != "Donut Coklat" || list[0].ItemCount != 1 {
		t.Errorf("ListCustomerOrders() = %+v, %v", list, err)
	}
	if n, err := repo.CountByStatus(ctx, tenant, customer, orders.AwaitingDP); err != nil || n != 1 {
		t.Errorf("CountByStatus() = %d, %v; want 1", n, err)
	}

	var events int
	if err := d.Pool().QueryRow(ctx, "select count(*) from outbox where event_type = 'order.placed' and payload->>'code' = $1", o.Code).Scan(&events); err != nil || events != 1 {
		t.Errorf("order.placed events = %d, %v; want one, written with the order", events, err)
	}
}

// A taken code stores nothing and says so, without aborting the
// transaction it runs in.
func TestInsert_TakenCode(t *testing.T) {
	d := dbtest.New(t)
	repo := postgres.NewRepository(d)
	tenant := dbtest.DefaultTenantID
	customer := newCustomer(t, d, tenant)
	first := insert(t, d, repo, tenant, order(customer, orders.AwaitingDP, 7, wib(6, 19, 30)))

	err := d.Tx(t.Context(), func(ctx context.Context) error {
		clash := order(customer, orders.AwaitingDP, 8, wib(7, 19, 30))
		clash.Code = first.Code
		clash.Items[0].VariantID = first.Items[0].VariantID
		ok, err := repo.Insert(ctx, tenant, clash, uuid.New(), now)
		if err != nil || ok {
			t.Errorf("Insert(taken code) = %v, %v; want false", ok, err)
		}
		retry := order(customer, orders.AwaitingDP, 8, wib(7, 19, 30))
		retry.Items[0].VariantID = first.Items[0].VariantID
		ok, err = repo.Insert(ctx, tenant, retry, uuid.New(), now)
		if err != nil || !ok {
			t.Errorf("Insert(fresh code) in the same transaction = %v, %v; want stored", ok, err)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFindByIdempotencyKey(t *testing.T) {
	d := dbtest.New(t)
	repo := postgres.NewRepository(d)
	tenant := dbtest.DefaultTenantID
	customer := newCustomer(t, d, tenant)
	o := order(customer, orders.AwaitingDP, 7, wib(6, 19, 30))
	o = insert(t, d, repo, tenant, o)
	var key uuid.UUID
	if err := d.Pool().QueryRow(t.Context(), "select idempotency_key from orders where id = $1", o.ID).Scan(&key); err != nil {
		t.Fatal(err)
	}

	if id, err := repo.FindByIdempotencyKey(t.Context(), tenant, customer, key); err != nil || id != o.ID {
		t.Errorf("FindByIdempotencyKey() = %s, %v; want %s", id, err, o.ID)
	}
	if _, err := repo.FindByIdempotencyKey(t.Context(), tenant, uuid.New(), key); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("another customer's key error = %v, want ErrNotFound", err)
	}
}

func TestBatchCutoffs(t *testing.T) {
	d := dbtest.New(t)
	repo := postgres.NewRepository(d)
	tenant := dbtest.DefaultTenantID
	customer := newCustomer(t, d, tenant)
	insert(t, d, repo, tenant, order(customer, orders.Confirmed, 7, wib(6, 19, 30)))
	insert(t, d, repo, tenant, order(customer, orders.Confirmed, 7, wib(6, 17, 0))) // earlier: the batch's cutoff
	insert(t, d, repo, tenant, order(customer, orders.AwaitingDP, 7, wib(6, 8, 0)))
	insert(t, d, repo, tenant, order(customer, orders.Expired, 7, wib(6, 7, 0)))
	insert(t, d, repo, tenant, order(customer, orders.Confirmed, 8, wib(7, 19, 30)))
	insert(t, d, repo, tenant, order(customer, orders.Confirmed, 20, wib(19, 19, 30))) // outside the range
	other := dbtest.CreateTenant(t, d, "Toko Lain")
	insert(t, d, repo, other, order(newCustomer(t, d, other), orders.Confirmed, 7, wib(6, 6, 0)))
	committed := []orders.Status{orders.Confirmed, orders.InProduction, orders.Ready, orders.OutForDelivery, orders.Completed}

	got, err := repo.BatchCutoffs(t.Context(), tenant, date(6), date(10), committed)

	if err != nil || len(got) != 2 || !got[date(7)].Equal(wib(6, 17, 0)) || !got[date(8)].Equal(wib(7, 19, 30)) {
		t.Errorf("BatchCutoffs() = %v, %v; want the 7th at Tuesday 17.00 (committed orders only) and the 8th", got, err)
	}
}

// An order produced the evening before its pickup keeps both days on hold.
func TestActiveOrderDays(t *testing.T) {
	d := dbtest.New(t)
	repo := postgres.NewRepository(d)
	tenant := dbtest.DefaultTenantID
	customer := newCustomer(t, d, tenant)
	insert(t, d, repo, tenant, order(customer, orders.Confirmed, 7, wib(6, 19, 30)))
	insert(t, d, repo, tenant, order(customer, orders.AwaitingDP, 7, wib(6, 19, 30)))
	insert(t, d, repo, tenant, order(customer, orders.Cancelled, 7, wib(6, 19, 30)))
	overnight := order(customer, orders.Confirmed, 9, wib(8, 12, 0))
	overnight.PickupAt, overnight.ProductionStart, overnight.ProductionDate = wib(9, 1, 0), wib(8, 23, 30), date(8)
	insert(t, d, repo, tenant, overnight)
	active := []orders.Status{orders.AwaitingDP, orders.Confirmed, orders.InProduction, orders.Ready, orders.OutForDelivery}

	got, err := repo.ActiveOrderDays(t.Context(), tenant, date(1), date(31), active)

	if err != nil || got[date(7)] != 2 || got[date(8)] != 1 || got[date(9)] != 1 || len(got) != 3 {
		t.Errorf("ActiveOrderDays() = %v, %v; want 2 on the 7th and the overnight order on the 8th and the 9th", got, err)
	}
}

func TestStaffReads(t *testing.T) {
	d := dbtest.New(t)
	repo := postgres.NewRepository(d)
	ctx := t.Context()
	tenant := dbtest.DefaultTenantID
	customer := newCustomer(t, d, tenant)
	early := insert(t, d, repo, tenant, order(customer, orders.Confirmed, 7, wib(6, 19, 30)))
	late := order(customer, orders.AwaitingDP, 7, wib(6, 19, 30))
	late.PickupAt, late.CustomerName, late.CustomerPhone = wib(7, 15, 0), "Budi Santoso", "+6285700001111"
	late = insert(t, d, repo, tenant, late)
	friday := insert(t, d, repo, tenant, order(customer, orders.Confirmed, 9, wib(8, 19, 30)))

	if o, err := repo.GetByCode(ctx, tenant, early.Code); err != nil || o.ID != early.ID || len(o.Items) != 1 {
		t.Errorf("GetByCode() = %+v, %v", o, err)
	}
	if _, err := repo.GetByCode(ctx, dbtest.CreateTenant(t, d, "Toko Lain"), early.Code); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("GetByCode(another tenant) error = %v, want ErrNotFound", err)
	}

	list := func(f orders.StaffFilter) []string {
		t.Helper()
		rows, err := repo.ListStaff(ctx, tenant, f, 10)
		if err != nil {
			t.Fatal(err)
		}
		var codes []string
		for _, r := range rows {
			codes = append(codes, r.Code)
		}
		return codes
	}
	day7 := orders.StaffFilter{From: date(7), To: date(7)}
	if got := list(day7); !slices.Equal(got, []string{early.Code, late.Code}) {
		t.Errorf("Wednesday = %v, want both, by pickup time", got)
	}
	rows, _ := repo.ListStaff(ctx, tenant, day7, 10)
	if r := rows[1]; r.CustomerName != "Budi Santoso" || r.CustomerPhone != "+6285700001111" || r.ProductionDate != date(7) ||
		r.FirstItem != "Donut Coklat" || r.ItemCount != 1 || r.Status != orders.AwaitingDP {
		t.Errorf("row = %+v", r)
	}
	for name, tt := range map[string]struct {
		f    orders.StaffFilter
		want []string
	}{
		"status":        {orders.StaffFilter{From: date(7), To: date(7), Statuses: []orders.Status{orders.AwaitingDP}}, []string{late.Code}},
		"code":          {orders.StaffFilter{From: date(1), To: date(31), Query: strings.ToLower(late.Code)}, []string{late.Code}},
		"phone":         {orders.StaffFilter{From: date(1), To: date(31), Query: "6285700001111"}, []string{late.Code}},
		"name":          {orders.StaffFilter{From: date(1), To: date(31), Query: "santoso"}, []string{late.Code}},
		"nothing":       {orders.StaffFilter{From: date(1), To: date(31), Query: "tidak ada"}, nil},
		"another range": {orders.StaffFilter{From: date(8), To: date(31)}, []string{friday.Code}},
	} {
		if got := list(tt.f); !slices.Equal(got, tt.want) {
			t.Errorf("%s: ListStaff() = %v, want %v", name, got, tt.want)
		}
	}
}

// A staff change locks the row, stores the statuses, and writes its audit
// entry and events in the caller's transaction: a failure later takes them
// all back.
func TestStaffWrites(t *testing.T) {
	d := dbtest.New(t)
	repo := postgres.NewRepository(d)
	ctx := t.Context()
	tenant := dbtest.DefaultTenantID
	o := insert(t, d, repo, tenant, order(newCustomer(t, d, tenant), orders.AwaitingDP, 7, wib(6, 19, 30)))
	actor := uuid.New()
	change := func(ctx context.Context) error {
		locked, err := repo.LockByCode(ctx, tenant, o.Code)
		if err != nil {
			return err
		}
		if err := repo.SetStatus(ctx, tenant, locked.ID, orders.Confirmed, payments.DPPaid, now); err != nil {
			return err
		}
		if err := repo.Audit(ctx, audit.Entry{TenantID: tenant, ActorID: actor, Action: "orders.payment.recorded_manually",
			Entity: "order", EntityID: o.ID, Reason: "Transfer langsung", At: now}); err != nil {
			return err
		}
		return repo.Publish(ctx, outbox.Event{TenantID: tenant, Aggregate: "order", Type: "order.confirmed", Payload: map[string]any{"code": o.Code}, At: now})
	}
	counts := func() (status string, audits, events int) {
		t.Helper()
		err := d.Pool().QueryRow(ctx, `select (select status from orders where id = $1),
			(select count(*) from audit_log where entity_id = $1),
			(select count(*) from outbox where event_type = 'order.confirmed' and payload->>'code' = $2)`, o.ID, o.Code).Scan(&status, &audits, &events)
		if err != nil {
			t.Fatal(err)
		}
		return status, audits, events
	}
	errBoom := errors.New("boom")

	if err := d.Tx(ctx, func(ctx context.Context) error {
		if err := change(ctx); err != nil {
			return err
		}
		return errBoom
	}); !errors.Is(err, errBoom) {
		t.Fatalf("Tx() error = %v", err)
	}
	if s, a, e := counts(); s != "awaiting_dp" || a != 0 || e != 0 {
		t.Errorf("after a rollback: %s, %d audits, %d events; want nothing changed", s, a, e)
	}

	if err := d.Tx(ctx, change); err != nil {
		t.Fatal(err)
	}
	if s, a, e := counts(); s != "confirmed" || a != 1 || e != 1 {
		t.Errorf("after a commit: %s, %d audits, %d events; want confirmed, one of each", s, a, e)
	}
	if got, _ := repo.GetByCode(ctx, tenant, o.Code); got.Payment != payments.DPPaid {
		t.Errorf("payment status = %s, want dp_paid", got.Payment)
	}
	if _, err := repo.LockByCode(ctx, tenant, "ZZZZZZ"); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("LockByCode(unknown) error = %v, want ErrNotFound", err)
	}
}
