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
	"github.com/Zefanrakh/kue-preorder/internal/platform/audit"
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
	return newOrderCoded(t, d, "K7M3QX")
}

func newOrderCoded(t *testing.T, d *db.DB, code string) uuid.UUID {
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
		values ($1, $2, $3, $4, 'awaiting_dp', 'unpaid', 'pickup', now() + interval '2 days', now() + interval '2 days',
		current_date + 2, now() + interval '1 day', now() + interval '3 hours', now() + interval '1 day',
		16000, 16000, 8000, false, 'dp-draft-1', now(), gen_random_uuid(), 'Sari', '+6281234567890', now(), now())
		returning id`, dbtest.DefaultTenantID, customer, channel, code).Scan(&order)
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
	dp := payments.Payment{ID: uuid.New(), OrderID: order, Kind: payments.KindDP, Provider: "dev", Method: payments.MethodQRIS, AmountIDR: 8000, ExpiresAt: &expires}

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
		if err := ledger.AddPending(ctx, dbtest.DefaultTenantID, payments.Payment{ID: uuid.New(), OrderID: order, Kind: payments.KindDP, Provider: "dev", Method: payments.MethodQRIS, AmountIDR: 8000}, now); err != nil {
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

// The SQL defaults of payment_policies are payments.DefaultPolicy's: a row
// written without the newer columns behaves like no row at all.
func TestPolicy_SQLDefaultsMatchGo(t *testing.T) {
	d := dbtest.New(t)
	_, err := d.Pool().Exec(t.Context(), `insert into payment_policies
		(tenant_id, dp_min_percent, dp_covers_ingredient_cost, balance_due_hours_before, dp_invoice_valid_minutes, updated_at)
		values ($1, 50, true, 12, 180, now())`, dbtest.DefaultTenantID)
	if err != nil {
		t.Fatal(err)
	}
	p, err := postgres.NewRepository(d).Policy(t.Context(), dbtest.DefaultTenantID)
	want := payments.DefaultPolicy()
	if err != nil || p.DPMinTotalIDR != want.DPMinTotalIDR || p.MinOrderIDR != want.MinOrderIDR {
		t.Errorf("Policy() = %+v, %v; want the DP threshold and minimum order of DefaultPolicy", p, err)
	}
}

func TestFeeRules(t *testing.T) {
	d := dbtest.New(t)
	reader := payments.NewReader(postgres.NewRepository(d))
	ctx := t.Context()

	rules, err := reader.FeeRules(ctx, dbtest.DefaultTenantID)
	if err != nil || len(rules) != 4 || rules[payments.MethodBankTransfer] != payments.DefaultFeeRules()[payments.MethodBankTransfer] {
		t.Fatalf("FeeRules() = %+v, %v; want the defaults", rules, err)
	}

	_, err = d.Pool().Exec(ctx, `insert into payment_method_fees (tenant_id, method, fixed_idr, rate_bps, vat_included, enabled, updated_at)
		values ($1, 'bank_transfer', 3500, 0, false, true, now()), ($1, 'minimarket', 5000, 0, false, false, now())`, dbtest.DefaultTenantID)
	if err != nil {
		t.Fatal(err)
	}
	rules, err = reader.FeeRules(ctx, dbtest.DefaultTenantID)
	if err != nil || rules[payments.MethodBankTransfer].FixedIDR != 3500 || rules[payments.MethodMinimarket].Enabled || !rules[payments.MethodQRIS].Enabled {
		t.Errorf("FeeRules() = %+v, %v; want the tenant's rows over the defaults", rules, err)
	}
}

func TestLedger_KeepsTheMethodAndRefusesAQRISFee(t *testing.T) {
	d := dbtest.New(t)
	repo := postgres.NewRepository(d)
	ledger := payments.NewLedger(repo)
	ctx := t.Context()
	order := newOrder(t, d)

	va := payments.Payment{ID: uuid.New(), OrderID: order, Kind: payments.KindDP, Provider: "dev", Method: payments.MethodBankTransfer, AmountIDR: 8000, FeeIDR: 4440}
	if err := ledger.AddPending(ctx, dbtest.DefaultTenantID, va, now); err != nil {
		t.Fatal(err)
	}
	got, err := ledger.OrderPayments(ctx, dbtest.DefaultTenantID, order)
	if err != nil || len(got) != 1 || got[0].Method != payments.MethodBankTransfer || got[0].FeeIDR != 4440 {
		t.Errorf("OrderPayments() = %+v, %v", got, err)
	}

	qris := payments.Payment{ID: uuid.New(), OrderID: order, Kind: payments.KindDP, Provider: "dev", Method: payments.MethodQRIS, AmountIDR: 8000, FeeIDR: 100}
	if err := ledger.AddPending(ctx, dbtest.DefaultTenantID, qris, now); err == nil {
		t.Error("the ledger took a QRIS payment with a fee")
	}
	// The database refuses it too, whoever writes.
	qris.State = payments.StatePending
	if err := repo.AddPayment(ctx, dbtest.DefaultTenantID, qris, now); err == nil {
		t.Error("the database took a QRIS payment with a fee")
	}
}

// Manual payments and refunds keep their proof, and ExpirePending closes
// only the order's pending invoices.
func TestLedger_ManualPaymentsAndExpiry(t *testing.T) {
	d := dbtest.New(t)
	ledger := payments.NewLedger(postgres.NewRepository(d))
	ctx := t.Context()
	order, other := newOrder(t, d), newOrderCoded(t, d, "P2Q3RS")
	tenant := dbtest.DefaultTenantID
	pending := func(order uuid.UUID) payments.Payment {
		return payments.Payment{ID: uuid.New(), OrderID: order, Kind: payments.KindDP, Provider: "dev", Method: payments.MethodBankTransfer, AmountIDR: 8000, FeeIDR: 4440}
	}
	mine, theirs := pending(order), pending(other)
	for _, p := range []payments.Payment{mine, theirs} {
		if err := ledger.AddPending(ctx, tenant, p, now); err != nil {
			t.Fatal(err)
		}
	}
	staff := uuid.New()
	dp := payments.Payment{ID: uuid.New(), OrderID: order, Kind: payments.KindDP, AmountIDR: 8000,
		Manual: &payments.ManualProof{Reference: "BCA 0412", Note: "Transfer langsung", RecordedBy: staff}}
	refund := payments.Payment{ID: uuid.New(), OrderID: order, Kind: payments.KindRefund, AmountIDR: -8000,
		Manual: &payments.ManualProof{Reference: "BCA balik", Note: "Oven rusak", RecordedBy: staff}}
	// Each step at its own time: the ledger lists payments by when they were made.
	paid, later := now.Add(time.Minute), now.Add(2*time.Minute)

	if err := ledger.AddManual(ctx, tenant, dp, paid); err != nil {
		t.Fatal(err)
	}
	if n, err := ledger.ExpirePending(ctx, tenant, order, paid); err != nil || n != 1 {
		t.Errorf("ExpirePending() = %d, %v; want the one pending invoice", n, err)
	}
	if err := ledger.AddManual(ctx, tenant, refund, later); err != nil {
		t.Fatal(err)
	}

	got, err := ledger.OrderPayments(ctx, tenant, order)
	if err != nil || len(got) != 3 {
		t.Fatalf("OrderPayments() = %+v, %v", got, err)
	}
	if got[0].State != payments.StateExpired || got[0].Manual != nil {
		t.Errorf("invoice = %+v, want expired", got[0])
	}
	if p := got[1]; p.State != payments.StatePaid || p.Provider != payments.ProviderManual || !p.PaidAt.Equal(paid) || p.Method != "" ||
		p.Manual == nil || *p.Manual != *dp.Manual {
		t.Errorf("manual DP = %+v (proof %+v), want paid with its proof", p, p.Manual)
	}
	if p := got[2]; p.Kind != payments.KindRefund || p.AmountIDR != -8000 || p.Manual.Reference != "BCA balik" {
		t.Errorf("refund = %+v", p)
	}
	if paid := payments.Paid(got); paid != 0 {
		t.Errorf("Paid() = %d, want 0 after the refund", paid)
	}
	if theirs, _ := ledger.OrderPayments(ctx, tenant, other); theirs[0].State != payments.StatePending {
		t.Errorf("another order's invoice = %s, want still pending", theirs[0].State)
	}
}

// The settings are saved over the defaults, replaced in place, and reset by
// removing the tenant's row; another tenant never sees them.
func TestSettingsWrites(t *testing.T) {
	d := dbtest.New(t)
	repo := postgres.NewRepository(d)
	reader := payments.NewReader(repo)
	ctx := t.Context()
	tenant := dbtest.DefaultTenantID
	policy := payments.DefaultPolicy()
	policy.DPMinTotalIDR, policy.DPMinPercent, policy.MinOrderIDR = 100_000, 40, 25_000
	va := payments.FeeRule{Method: payments.MethodBankTransfer, FixedIDR: 3500, RateBPS: 10, VATIncluded: true}

	for i, at := range []time.Time{now, now.Add(time.Hour)} {
		va.Enabled = i == 1
		if err := repo.SavePolicy(ctx, tenant, policy, at); err != nil {
			t.Fatal(err)
		}
		if err := repo.SaveFeeRule(ctx, tenant, va, at); err != nil {
			t.Fatal(err)
		}
	}

	got, err := reader.Policy(ctx, tenant)
	policy.UpdatedAt = now.Add(time.Hour)
	if err != nil || !got.UpdatedAt.Equal(policy.UpdatedAt) {
		t.Fatalf("Policy() = %+v, %v", got, err)
	}
	got.UpdatedAt = policy.UpdatedAt
	if got != policy {
		t.Errorf("Policy() = %+v, want %+v", got, policy)
	}
	rules, err := reader.FeeRules(ctx, tenant)
	if r := rules[payments.MethodBankTransfer]; err != nil || r.FixedIDR != 3500 || r.RateBPS != 10 || !r.VATIncluded || !r.Enabled ||
		!r.UpdatedAt.Equal(now.Add(time.Hour)) || !rules[payments.MethodQRIS].UpdatedAt.IsZero() {
		t.Errorf("FeeRules() = %+v, %v; want the saved rule, replaced in place", rules, err)
	}
	other := dbtest.CreateTenant(t, d, "Toko Lain")
	if p, _ := reader.Policy(ctx, other); p != payments.DefaultPolicy() {
		t.Errorf("another tenant's policy = %+v, want the defaults", p)
	}

	if had, err := repo.DeleteFeeRule(ctx, tenant, payments.MethodBankTransfer); err != nil || !had {
		t.Errorf("DeleteFeeRule() = %t, %v; want the row gone", had, err)
	}
	if had, err := repo.DeleteFeeRule(ctx, tenant, payments.MethodBankTransfer); err != nil || had {
		t.Errorf("DeleteFeeRule() again = %t, %v; want nothing to delete", had, err)
	}
	if rules, _ := reader.FeeRules(ctx, tenant); rules[payments.MethodBankTransfer] != payments.DefaultFeeRules()[payments.MethodBankTransfer] {
		t.Errorf("after a reset: %+v, want the default", rules[payments.MethodBankTransfer])
	}
}

// A settings change and its audit entry commit or roll back together, under
// the tenant's settings lock.
func TestSettingsWrites_JoinTheCallersTransaction(t *testing.T) {
	d := dbtest.New(t)
	repo := postgres.NewRepository(d)
	ctx := t.Context()
	tenant := dbtest.DefaultTenantID
	change := func(ctx context.Context) error {
		if err := repo.LockSettings(ctx, tenant); err != nil {
			return err
		}
		if err := repo.SaveFeeRule(ctx, tenant, payments.FeeRule{Method: payments.MethodEWallet, RateBPS: 150, VATIncluded: true, Enabled: true}, now); err != nil {
			return err
		}
		return repo.Audit(ctx, audit.Entry{TenantID: tenant, ActorID: uuid.New(), Action: "payments.method_fee.changed",
			Entity: "payment_method_fee", EntityID: tenant, Reason: "Tarif baru", At: now})
	}
	counts := func() (rules, audits int) {
		t.Helper()
		if err := d.Pool().QueryRow(ctx, `select (select count(*) from payment_method_fees where tenant_id = $1),
			(select count(*) from audit_log where tenant_id = $1 and entity = 'payment_method_fee')`, tenant).Scan(&rules, &audits); err != nil {
			t.Fatal(err)
		}
		return rules, audits
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
	if r, a := counts(); r != 0 || a != 0 {
		t.Errorf("after a rollback: %d rules, %d audits; want none", r, a)
	}
	if err := d.Tx(ctx, change); err != nil {
		t.Fatal(err)
	}
	if r, a := counts(); r != 1 || a != 1 {
		t.Errorf("after a commit: %d rules, %d audits; want one of each", r, a)
	}
}
