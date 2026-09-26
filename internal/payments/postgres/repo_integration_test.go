//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/payments"
	"github.com/Zefanrakh/kue-preorder/internal/payments/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

var now = time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC)

func TestPolicy(t *testing.T) {
	d := dbtest.New(t)
	repo := postgres.NewRepository(d)
	ctx := t.Context()

	if _, err := repo.Policy(ctx, dbtest.DefaultTenantID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("Policy() before any change error = %v, want ErrNotFound", err)
	}
	if p, err := payments.NewReader(repo).Policy(ctx, dbtest.DefaultTenantID); err != nil || p != payments.DefaultPolicy() {
		t.Errorf("Reader.Policy() = %+v, %v; want the defaults", p, err)
	}

	_, err := d.Pool().Exec(ctx, `insert into payment_policies
		(tenant_id, dp_min_percent, dp_covers_ingredient_cost, balance_due_hours_before, dp_invoice_valid_minutes, updated_at)
		values ($1, 30, false, 24, 60, now())`, dbtest.DefaultTenantID)
	if err != nil {
		t.Fatal(err)
	}
	p, err := payments.NewReader(repo).Policy(ctx, dbtest.DefaultTenantID)
	if err != nil || p.DPMinPercent != 30 || p.DPCoversIngredientCost || p.BalanceDueHoursBefore != 24 || p.DPInvoiceValidMinutes != 60 || p.UpdatedAt.IsZero() {
		t.Errorf("Policy() = %+v, %v; want the stored row", p, err)
	}

	other := dbtest.CreateTenant(t, d, "Toko Lain")
	if p, err := payments.NewReader(repo).Policy(ctx, other); err != nil || p != payments.DefaultPolicy() {
		t.Errorf("another tenant's Policy() = %+v, %v; want the defaults", p, err)
	}
}

// newOrder writes a bare order straight to the table; placing orders is the
// orders module's job and this package may not import it.
func newOrder(t *testing.T, d *db.DB) uuid.UUID {
	t.Helper()
	ctx := t.Context()
	var customer, channel, order uuid.UUID
	if err := d.Pool().QueryRow(ctx, "insert into customers (tenant_id, name, phone) values ($1, 'Sari', '+6281234567890') returning id", dbtest.DefaultTenantID).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if err := d.Pool().QueryRow(ctx, "select id from channels where tenant_id = $1 and key = 'web'", dbtest.DefaultTenantID).Scan(&channel); err != nil {
		t.Fatal(err)
	}
	err := d.Pool().QueryRow(ctx, `insert into orders (tenant_id, customer_id, channel_id, code, status, payment_status, fulfillment_type,
		pickup_at, production_start_at, production_date, shopping_cutoff_at, dp_due_at, balance_due_at,
		subtotal_idr, total_idr, dp_required_idr, full_payment_required, terms_version, terms_accepted_at,
		idempotency_key, customer_name, customer_phone, created_at, updated_at)
		values ($1, $2, $3, 'K7M3QX', 'awaiting_dp', 'unpaid', 'pickup', now() + interval '2 days', now() + interval '2 days',
		current_date + 2, now() + interval '1 day', now() + interval '3 hours', now() + interval '1 day',
		16000, 16000, 8000, false, 'dp-draft-1', now(), gen_random_uuid(), 'Sari', '+6281234567890', now(), now())
		returning id`, dbtest.DefaultTenantID, customer, channel).Scan(&order)
	if err != nil {
		t.Fatal(err)
	}
	return order
}

func TestLedger(t *testing.T) {
	d := dbtest.New(t)
	ledger := payments.NewLedger(postgres.NewRepository(d))
	ctx := t.Context()
	order := newOrder(t, d)
	expires := now.Add(3 * time.Hour)
	dp := payments.Payment{ID: uuid.New(), OrderID: order, Kind: payments.KindDP, Provider: "dev", AmountIDR: 8000, ExpiresAt: &expires}

	if err := ledger.AddPending(ctx, dbtest.DefaultTenantID, dp, now); err != nil {
		t.Fatal(err)
	}
	if err := ledger.AttachInvoice(ctx, dbtest.DefaultTenantID, dp.ID, payments.Invoice{ExternalID: "inv-1", URL: "https://pay.test/1"}, now); err != nil {
		t.Fatal(err)
	}
	got, err := ledger.OrderPayments(ctx, dbtest.DefaultTenantID, order)
	if err != nil || len(got) != 1 {
		t.Fatalf("OrderPayments() = %+v, %v", got, err)
	}
	p := got[0]
	if p.State != payments.StatePending || p.AmountIDR != 8000 || p.FeeIDR != 0 || p.CheckoutURL != "https://pay.test/1" || p.ExternalID != "inv-1" || !p.ExpiresAt.Equal(expires) {
		t.Errorf("payment = %+v, want a pending DP with its invoice", p)
	}

	// A payment that is no longer pending takes no invoice.
	if _, err := d.Pool().Exec(ctx, "update payments set status = 'expired' where id = $1", dp.ID); err != nil {
		t.Fatal(err)
	}
	if err := ledger.AttachInvoice(ctx, dbtest.DefaultTenantID, dp.ID, payments.Invoice{ExternalID: "inv-2", URL: "x"}, now); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("AttachInvoice(expired) error = %v, want ErrNotFound", err)
	}
}

func TestLedger_RejectsBadPayments(t *testing.T) {
	ledger := payments.NewLedger(postgres.NewRepository(dbtest.New(t)))
	for name, p := range map[string]payments.Payment{
		"zero":        {ID: uuid.New(), OrderID: uuid.New(), Kind: payments.KindDP, Provider: "dev"},
		"refund":      {ID: uuid.New(), OrderID: uuid.New(), Kind: payments.KindRefund, Provider: "dev", AmountIDR: 100},
		"no order":    {ID: uuid.New(), Kind: payments.KindDP, Provider: "dev", AmountIDR: 100},
		"no provider": {ID: uuid.New(), OrderID: uuid.New(), Kind: payments.KindDP, AmountIDR: 100},
	} {
		if err := ledger.AddPending(t.Context(), dbtest.DefaultTenantID, p, now); err == nil {
			t.Errorf("AddPending(%s) succeeded, want an error", name)
		}
	}
}

// Inside a Tx the ledger writes in the caller's transaction: a failure
// later in the unit of work takes the payment back too.
func TestLedger_JoinsTheCallersTransaction(t *testing.T) {
	d := dbtest.New(t)
	ledger := payments.NewLedger(postgres.NewRepository(d))
	order := newOrder(t, d)
	errBoom := errors.New("boom")

	err := d.Tx(t.Context(), func(ctx context.Context) error {
		if err := ledger.AddPending(ctx, dbtest.DefaultTenantID, payments.Payment{ID: uuid.New(), OrderID: order, Kind: payments.KindDP, Provider: "dev", AmountIDR: 8000}, now); err != nil {
			return err
		}
		return errBoom
	})

	if !errors.Is(err, errBoom) {
		t.Fatalf("Tx() error = %v", err)
	}
	if got, _ := ledger.OrderPayments(t.Context(), dbtest.DefaultTenantID, order); len(got) != 0 {
		t.Errorf("OrderPayments() = %+v, want none after the rollback", got)
	}
}
