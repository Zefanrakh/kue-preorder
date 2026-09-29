//go:build integration

package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	catalogpg "github.com/Zefanrakh/kue-preorder/internal/catalog/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
	identitypg "github.com/Zefanrakh/kue-preorder/internal/identity/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/inventory"
	inventorypg "github.com/Zefanrakh/kue-preorder/internal/inventory/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/orders"
	orderspg "github.com/Zefanrakh/kue-preorder/internal/orders/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/payments"
	paymentspg "github.com/Zefanrakh/kue-preorder/internal/payments/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
	"github.com/Zefanrakh/kue-preorder/internal/recipe"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func wib(day, hour, minute int) time.Time {
	return time.Date(2026, time.October, day, hour, minute, 0, 0, clock.Jakarta)
}

func noErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// placed stores an order picked up Thursday 8 October at 09.00: two donuts,
// 16,000, a DP of 8,000 due Monday 13.00 by bank transfer, the balance due
// Wednesday 19.30. The api placed orders like it; this package may not call
// the api.
func placed(t *testing.T, d *db.DB, at time.Time) orders.Order {
	t.Helper()
	ctx := t.Context()
	tenant := dbtest.DefaultTenantID
	var customer, product, variant uuid.UUID
	noErr(t, d.Pool().QueryRow(ctx, "insert into customers (tenant_id, auth_user_id, name, phone) values ($1, $2, 'Sari', '+6281234567890') returning id", tenant, uuid.New()).Scan(&customer))
	noErr(t, d.Pool().QueryRow(ctx, "insert into products (tenant_id, name, slug) values ($1, 'Donut', 'donut') returning id", tenant).Scan(&product))
	noErr(t, d.Pool().QueryRow(ctx, `insert into product_variants (tenant_id, product_id, sku, name, price_idr, production_minutes, min_notice_hours)
		values ($1, $2, 'DNT', 'Coklat', 8000, 90, 12) returning id`, tenant, product).Scan(&variant))
	pickup := wib(8, 9, 0)
	o := orders.Order{
		ID: uuid.New(), CustomerID: customer, Code: orders.NewCode(), Status: orders.AwaitingDP, Payment: payments.Unpaid, Fulfillment: orders.Pickup,
		Items:       []orders.QuotedItem{{VariantID: variant, ProductName: "Donut", VariantName: "Coklat", Quantity: 2, UnitPriceIDR: 8000, LineTotalIDR: 16000, ProductionMinutes: 90, MinNoticeHours: 12}},
		SubtotalIDR: 16000, TotalIDR: 16000, DPRequiredIDR: 8000,
		PickupAt: pickup, ProductionStart: pickup.Add(-90 * time.Minute), ProductionDate: clock.DateOf(pickup),
		ShoppingCutoffAt: wib(7, 19, 30), DPDueAt: wib(5, 13, 0), BalanceDueAt: wib(7, 19, 30),
		CustomerName: "Sari", CustomerPhone: "+6281234567890", TermsVersion: orders.TermsVersion,
	}
	ok, err := orderspg.NewRepository(d).Insert(ctx, tenant, o, uuid.New(), at)
	if err != nil || !ok {
		t.Fatalf("Insert() = %v, %v", ok, err)
	}
	dueAt := o.DPDueAt
	noErr(t, payments.NewLedger(paymentspg.NewRepository(d)).AddPending(ctx, tenant, payments.Payment{
		ID: uuid.New(), OrderID: o.ID, Kind: payments.KindDP, Provider: "dev", Method: payments.MethodBankTransfer,
		AmountIDR: 8000, FeeIDR: 4440, ExpiresAt: &dueAt,
	}, at))
	return o
}

// start runs the worker as main wires it, with the clock at now, until the
// test ends.
func start(t *testing.T, d *db.DB, now time.Time) *outbox.Publisher {
	t.Helper()
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	tenants, err := identity.ResolveSingleTenant(ctx, identitypg.NewRepository(d.Pool()))
	noErr(t, err)
	client, publisher, err := wire(d, tenants, payments.DevProvider{}, clock.NewFake(now), slog.New(slog.DiscardHandler), sdktrace.NewTracerProvider())
	noErr(t, err)
	noErr(t, client.Start(ctx))
	t.Cleanup(func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = client.Stop(stopCtx)
		cancel()
	})
	return publisher
}

// eventually polls check until it holds, for at most 20 seconds.
func eventually(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for !check() {
		select {
		case <-deadline:
			t.Fatalf("gave up waiting: %s", what)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// A DP arrives: the order.confirmed event goes out through the outbox,
// starts the balance invoice job, and the balance is billed with the
// method chosen at checkout.
func TestWorker_BillsTheBalanceAfterTheDP(t *testing.T) {
	d := dbtest.New(t)
	ctx := t.Context()
	o := placed(t, d, wib(5, 10, 0))
	_, err := d.Pool().Exec(ctx, "update payments set status = 'paid', paid_at = $2 where order_id = $1", o.ID, wib(5, 10, 5))
	noErr(t, err)
	_, err = d.Pool().Exec(ctx, "update orders set status = 'confirmed', payment_status = 'dp_paid' where id = $1", o.ID)
	noErr(t, err)
	noErr(t, outbox.Append(ctx, d.Pool(), outbox.Event{
		TenantID: dbtest.DefaultTenantID, Aggregate: "order", Type: "order.confirmed", At: wib(5, 10, 5),
		Payload: map[string]any{"order_id": o.ID, "code": o.Code, "production_date": "2026-10-08"},
	}))
	publisher := start(t, d, wib(5, 10, 6))

	n, err := publisher.PublishBatch(ctx)

	if err != nil || n != 2 {
		t.Fatalf("PublishBatch() = %d, %v; want order.placed and order.confirmed out", n, err)
	}
	var amount, fee int64
	var method, url string
	var expires time.Time
	eventually(t, "the balance invoice", func() bool {
		err := d.Pool().QueryRow(ctx, `select amount_idr, fee_idr, method, coalesce(checkout_url, ''), expires_at from payments
			where order_id = $1 and kind = 'balance' and status = 'pending'`, o.ID).Scan(&amount, &fee, &method, &url, &expires)
		return err == nil && url != ""
	})
	if amount != 8000 || fee != 4440 || method != "bank_transfer" || !expires.Equal(o.BalanceDueAt) {
		t.Errorf("balance = %d + %d by %s until %v, want 8,000 + Rp4.440 by bank transfer until the balance deadline", amount, fee, method, expires)
	}
	var state string
	eventually(t, "the job completed", func() bool {
		err := d.Pool().QueryRow(ctx, "select state from river.river_job where kind = 'create-balance-invoice'").Scan(&state)
		return err == nil && state == "completed"
	})
}

// An order whose DP never came expires on the sweep River runs at start.
func TestWorker_ExpiresUnpaidOrders(t *testing.T) {
	d := dbtest.New(t)
	ctx := t.Context()
	o := placed(t, d, wib(5, 10, 0))

	start(t, d, o.DPDueAt.Add(orders.Grace))

	var status, invoice string
	eventually(t, "the order expired", func() bool {
		err := d.Pool().QueryRow(ctx, `select o.status, p.status from orders o join payments p on p.order_id = o.id where o.id = $1`, o.ID).Scan(&status, &invoice)
		return err == nil && status == "expired"
	})
	if invoice != "expired" {
		t.Errorf("DP invoice = %s, want expired with the order", invoice)
	}
	var automatic bool
	noErr(t, d.Pool().QueryRow(ctx, "select (payload->>'automatic')::boolean from outbox where event_type = 'order.expired' and payload->>'code' = $1", o.Code).Scan(&automatic))
	if !automatic {
		t.Error("order.expired is not marked automatic")
	}
}

// withRecipe gives the order's variant one portion of dough per donut, and
// the dough 50 g of flour per portion, bought in packs of 1 kg at Rp14.000.
// It returns the flour.
func withRecipe(t *testing.T, d *db.DB, o orders.Order) uuid.UUID {
	t.Helper()
	ctx := t.Context()
	tenant := dbtest.DefaultTenantID
	repo := catalogpg.NewRepository(d)
	at := wib(5, 9, 0)
	dough, err := repo.CreateComponent(ctx, tenant, catalog.ComponentInput{Name: "Adonan donut", UnitLabel: "porsi"}, at)
	noErr(t, err)
	flour, err := repo.CreateIngredient(ctx, tenant, catalog.IngredientInput{Name: "Tepung", BaseUnit: catalog.Gram, LeftoverPolicy: catalog.LeftoverAuto}, at)
	noErr(t, err)
	_, err = repo.SetVariantComponents(ctx, tenant, o.Items[0].VariantID, []catalog.VariantComponent{{ComponentID: dough.ID, UnitsPerItem: 1}}, at)
	noErr(t, err)
	perPortion, err := recipe.NewAffine(0, 50)
	noErr(t, err)
	params, err := recipe.Params(perPortion)
	noErr(t, err)
	_, err = repo.SetRecipeLine(ctx, tenant, catalog.RecipeLineWrite{
		ComponentID: dough.ID, IngredientID: flour.ID, ModelType: recipe.Affine, Params: params, WasteFactor: 1,
	}, at)
	noErr(t, err)
	supplier, err := repo.CreateSupplier(ctx, tenant, catalog.SupplierInput{Name: "Toko Sinar", Adapter: catalog.AdapterManual}, at)
	noErr(t, err)
	kilo := int64(14000)
	pack, err := repo.CreatePack(ctx, tenant, flour.ID, catalog.PackInput{SupplierID: supplier.ID, Size: 1000, Unit: "kg", PriceIDR: &kilo}, at)
	noErr(t, err)
	_, err = repo.SetDefaultPack(ctx, tenant, pack.ID, at)
	noErr(t, err)
	return flour.ID
}

func emit(t *testing.T, d *db.DB, typ string, o orders.Order, at time.Time) {
	t.Helper()
	noErr(t, outbox.Append(t.Context(), d.Pool(), outbox.Event{
		TenantID: dbtest.DefaultTenantID, Aggregate: "order", Type: typ, At: at,
		Payload: map[string]any{"order_id": o.ID, "code": o.Code, "production_date": o.ProductionDate.String()},
	}))
}

// A confirmed order puts its ingredients on its day's shopping list; the
// list empties when the order is cancelled.
func TestWorker_KeepsTheShoppingListCurrent(t *testing.T) {
	d := dbtest.New(t)
	ctx := t.Context()
	o := placed(t, d, wib(5, 10, 0))
	flour := withRecipe(t, d, o)
	_, err := d.Pool().Exec(ctx, "update orders set status = 'confirmed', payment_status = 'dp_paid' where id = $1", o.ID)
	noErr(t, err)
	emit(t, d, "order.confirmed", o, wib(5, 10, 5))
	publisher := start(t, d, wib(5, 10, 6))

	_, err = publisher.PublishBatch(ctx)
	noErr(t, err)

	var needed, packs int64
	eventually(t, "the flour on the shopping list", func() bool {
		err := d.Pool().QueryRow(ctx, `select r.qty_needed, r.packs_to_buy from batch_requirements r
			join production_batches b on b.id = r.batch_id
			where b.batch_date = '2026-10-08' and r.ingredient_id = $1`, flour).Scan(&needed, &packs)
		return err == nil
	})
	if needed != 100 || packs != 1 {
		t.Errorf("flour = %d g in %d packs, want 100 g (2 donuts × 50 g) in 1 pack of 1 kg", needed, packs)
	}

	_, err = d.Pool().Exec(ctx, "update orders set status = 'cancelled', payment_status = 'forfeited' where id = $1", o.ID)
	noErr(t, err)
	emit(t, d, "order.cancelled", o, wib(7, 20, 0))
	_, err = publisher.PublishBatch(ctx)
	noErr(t, err)
	eventually(t, "the shopping list emptied", func() bool {
		var n int
		err := d.Pool().QueryRow(ctx, `select count(*) from batch_requirements r join production_batches b on b.id = r.batch_id
			where b.batch_date = '2026-10-08'`).Scan(&n)
		return err == nil && n == 0
	})
}

// Stock that arrives lowers the shopping list: inventory.stock_changed
// recomputes the upcoming batches with what may be counted.
func TestWorker_StockLowersTheShoppingList(t *testing.T) {
	d := dbtest.New(t)
	ctx := t.Context()
	o := placed(t, d, wib(5, 10, 0))
	flour := withRecipe(t, d, o)
	_, err := d.Pool().Exec(ctx, "update orders set status = 'confirmed', payment_status = 'dp_paid' where id = $1", o.ID)
	noErr(t, err)
	emit(t, d, "order.confirmed", o, wib(5, 10, 5))
	publisher := start(t, d, wib(5, 10, 6))
	_, err = publisher.PublishBatch(ctx)
	noErr(t, err)
	line := func() (usable, toBuy int64, err error) {
		err = d.Pool().QueryRow(ctx, `select r.qty_usable_stock, r.qty_to_buy from batch_requirements r
			join production_batches b on b.id = r.batch_id where b.batch_date = '2026-10-08' and r.ingredient_id = $1`, flour).Scan(&usable, &toBuy)
		return usable, toBuy, err
	}
	eventually(t, "the flour on the shopping list", func() bool { _, toBuy, err := line(); return err == nil && toBuy == 100 })

	repo := inventorypg.NewRepository(d)
	lot := inventory.Lot{ID: uuid.New(), IngredientID: flour, ReceivedAt: wib(5, 10, 7), Status: inventory.LotAvailable, Source: inventory.SourceManual}
	noErr(t, repo.InsertLot(ctx, dbtest.DefaultTenantID, lot, uuid.New(), wib(5, 10, 7)))
	noErr(t, repo.InsertMovement(ctx, dbtest.DefaultTenantID, inventory.Movement{
		LotID: lot.ID, IngredientID: flour, Kind: inventory.MoveReceive, Qty: 60, ActorID: uuid.New(), At: wib(5, 10, 7),
	}))
	noErr(t, outbox.Append(ctx, d.Pool(), outbox.Event{
		TenantID: dbtest.DefaultTenantID, Aggregate: "ingredient", Type: "inventory.stock_changed", At: wib(5, 10, 7),
		Payload: map[string]any{"ingredient_id": flour},
	}))
	_, err = publisher.PublishBatch(ctx)
	noErr(t, err)

	eventually(t, "the stock counted", func() bool {
		usable, toBuy, err := line()
		return err == nil && usable == 60 && toBuy == 40
	})
}

// Past its shopping cutoff a batch locks on the worker's sweep; the first
// order moved into production puts it in production.
func TestWorker_BatchLocksAndGoesIntoProduction(t *testing.T) {
	d := dbtest.New(t)
	ctx := t.Context()
	o := placed(t, d, wib(5, 10, 0)) // cutoff Wednesday 19.30
	_, err := d.Pool().Exec(ctx, "update orders set status = 'confirmed', payment_status = 'dp_paid' where id = $1", o.ID)
	noErr(t, err)
	_, err = d.Pool().Exec(ctx, "insert into production_batches (tenant_id, batch_date, created_at, updated_at) values ($1, '2026-10-08', now(), now())", dbtest.DefaultTenantID)
	noErr(t, err)
	status := func() string {
		var s string
		if err := d.Pool().QueryRow(ctx, "select status from production_batches where batch_date = '2026-10-08'").Scan(&s); err != nil {
			return ""
		}
		return s
	}

	publisher := start(t, d, wib(7, 19, 30)) // the sweep runs at start

	eventually(t, "the batch locked", func() bool { return status() == "locked" })
	noErr(t, outbox.Append(ctx, d.Pool(), outbox.Event{
		TenantID: dbtest.DefaultTenantID, Aggregate: "order", Type: "order.status_changed", At: wib(8, 5, 0),
		Payload: map[string]any{"order_id": o.ID, "code": o.Code, "production_date": "2026-10-08", "from": "confirmed", "to": "in_production"},
	}))
	_, err = publisher.PublishBatch(ctx)
	noErr(t, err)
	eventually(t, "the batch in production", func() bool { return status() == "in_production" })
}
