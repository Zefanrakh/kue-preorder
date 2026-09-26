//go:build integration

package postgres_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/orders"
	"github.com/Zefanrakh/kue-preorder/internal/orders/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func wib(day, hour, minute int) time.Time {
	return time.Date(2026, time.October, day, hour, minute, 0, 0, clock.Jakarta)
}

func date(day int) clock.Date { return clock.Date{Year: 2026, Month: time.October, Day: day} }

// insertOrder stores a minimal order for production on day with its own
// shopping cutoff. Placing orders arrives in M2.4; until then the test
// writes the row.
func insertOrder(t *testing.T, d *db.DB, tenant uuid.UUID, status string, day int, cutoff time.Time) {
	t.Helper()
	ctx := t.Context()
	var customer, channel uuid.UUID
	if err := d.Pool().QueryRow(ctx, "insert into customers (tenant_id, name, phone) values ($1, 'Budi', '+628123456789') returning id", tenant).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	err := d.Pool().QueryRow(ctx, "select id from channels where tenant_id = $1 and key = 'web'", tenant).Scan(&channel)
	if err != nil {
		if err := d.Pool().QueryRow(ctx, "insert into channels (tenant_id, key, name) values ($1, 'web', 'Website') returning id", tenant).Scan(&channel); err != nil {
			t.Fatal(err)
		}
	}
	payment := map[string]string{"awaiting_dp": "unpaid", "expired": "unpaid", "confirmed": "dp_paid", "cancelled": "forfeited"}[status]
	pickup := wib(day, 9, 0)
	_, err = d.Pool().Exec(ctx, `insert into orders (tenant_id, customer_id, channel_id, status, payment_status, fulfillment_type,
		pickup_at, production_start_at, production_date, shopping_cutoff_at, dp_due_at, balance_due_at,
		subtotal_idr, total_idr, dp_required_idr, full_payment_required, terms_version, terms_accepted_at,
		idempotency_key, created_at, updated_at)
		values ($1, $2, $3, $4, $5, 'pickup', $6, $7, $8, $9, $9, $9, 8000, 8000, 4000, false, 'v1', now(), $10, now(), now())`,
		tenant, customer, channel, status, payment, pickup, pickup.Add(-90*time.Minute), date(day).String(), cutoff, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
}

func TestBatchCutoffs(t *testing.T) {
	d := dbtest.New(t)
	tenant := dbtest.DefaultTenantID
	insertOrder(t, d, tenant, "confirmed", 7, wib(6, 19, 30))
	insertOrder(t, d, tenant, "confirmed", 7, wib(6, 17, 0)) // earlier: the batch's cutoff
	insertOrder(t, d, tenant, "awaiting_dp", 7, wib(6, 8, 0))
	insertOrder(t, d, tenant, "cancelled", 7, wib(6, 7, 0))
	insertOrder(t, d, tenant, "confirmed", 8, wib(7, 19, 30))
	insertOrder(t, d, tenant, "confirmed", 20, wib(19, 19, 30)) // outside the range
	other := dbtest.CreateTenant(t, d, "Toko Lain")
	insertOrder(t, d, other, "confirmed", 7, wib(6, 6, 0))
	committed := []orders.Status{orders.Confirmed, orders.InProduction, orders.Ready, orders.OutForDelivery, orders.Completed}

	got, err := postgres.NewRepository(d).BatchCutoffs(t.Context(), tenant, date(6), date(10), committed)

	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[date(7)].Equal(wib(6, 17, 0)) || !got[date(8)].Equal(wib(7, 19, 30)) {
		t.Errorf("BatchCutoffs() = %v, want the 7th at Tuesday 17.00 (committed orders only) and the 8th", got)
	}
}
