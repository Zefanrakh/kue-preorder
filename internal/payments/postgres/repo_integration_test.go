//go:build integration

package postgres_test

import (
	"errors"
	"testing"

	"github.com/Zefanrakh/kue-preorder/internal/payments"
	"github.com/Zefanrakh/kue-preorder/internal/payments/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

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
