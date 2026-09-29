//go:build integration

package postgres_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
	"github.com/Zefanrakh/kue-preorder/internal/procurement"
	"github.com/Zefanrakh/kue-preorder/internal/procurement/postgres"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

var (
	now      = time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	thursday = clock.Date{Year: 2026, Month: time.October, Day: 8}
)

// seed writes a batch, two ingredients, and a supplier straight to their
// tables: they belong to other modules.
func seed(t *testing.T, d *db.DB) (batch, flour, cocoa, supplier uuid.UUID) {
	t.Helper()
	ctx := t.Context()
	tenant := dbtest.DefaultTenantID
	for _, q := range []struct {
		sql string
		id  *uuid.UUID
	}{
		{"insert into production_batches (tenant_id, batch_date, created_at, updated_at) values ($1, '2026-10-08', now(), now()) returning id", &batch},
		{"insert into ingredients (tenant_id, name, base_unit) values ($1, 'Tepung', 'g') returning id", &flour},
		{"insert into ingredients (tenant_id, name, base_unit) values ($1, 'Coklat', 'g') returning id", &cocoa},
		{"insert into suppliers (tenant_id, name) values ($1, 'Toko Sinar') returning id", &supplier},
	} {
		if err := d.Pool().QueryRow(ctx, q.sql, tenant).Scan(q.id); err != nil {
			t.Fatal(err)
		}
	}
	return batch, flour, cocoa, supplier
}

func TestOrders(t *testing.T) {
	d := dbtest.New(t)
	repo := postgres.NewRepository(d)
	ctx := t.Context()
	tenant := dbtest.DefaultTenantID
	batch, flour, cocoa, supplier := seed(t, d)
	price := int64(14000)
	o := procurement.Order{
		ID: uuid.New(), BatchID: batch, BatchDate: thursday, SupplierID: &supplier, Adapter: "manual", Status: procurement.OrderOrdered,
		Note: "Antar pagi", CreatedBy: uuid.New(),
		Items: []procurement.Item{
			{ID: uuid.New(), IngredientID: flour, Qty: 2000, Pack: &procurement.Pack{Count: 2, Size: 1000, Unit: "kg", PriceIDR: &price}, Status: procurement.ItemOrdered},
			{ID: uuid.New(), IngredientID: cocoa, Qty: 53, Status: procurement.ItemOrdered},
		},
	}
	if err := repo.InsertOrder(ctx, tenant, o, now); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetOrder(ctx, tenant, o.ID)
	if err != nil || got.BatchDate != thursday || *got.SupplierID != supplier || got.Note != "Antar pagi" || len(got.Items) != 2 {
		t.Fatalf("GetOrder() = %+v, %v", got, err)
	}
	for _, it := range got.Items {
		switch it.IngredientID {
		case flour:
			if it.Pack == nil || it.Pack.Count != 2 || it.Pack.Size != 1000 || *it.Pack.PriceIDR != 14000 || it.Qty != 2000 {
				t.Errorf("flour = %+v (pack %+v)", it, it.Pack)
			}
		case cocoa:
			if it.Pack != nil || it.Qty != 53 {
				t.Errorf("cocoa = %+v, want 53 g without a pack", it)
			}
		}
	}

	if ok, err := repo.SetItemReceived(ctx, tenant, o.Items[0].ID, 1800, now); err != nil || !ok {
		t.Fatalf("SetItemReceived() = %t, %v", ok, err)
	}
	if ok, _ := repo.SetItemReceived(ctx, tenant, o.Items[0].ID, 1800, now); ok {
		t.Error("an item was received twice")
	}
	if ok, _ := repo.SetItemCancelled(ctx, tenant, o.Items[0].ID); ok {
		t.Error("a received item was cancelled")
	}
	if ok, err := repo.SetItemCancelled(ctx, tenant, o.Items[1].ID); err != nil || !ok {
		t.Errorf("SetItemCancelled() = %t, %v", ok, err)
	}
	if err := repo.SetOrderStatus(ctx, tenant, o.ID, procurement.OrderReceived, now); err != nil {
		t.Fatal(err)
	}
	list, err := repo.ListOrders(ctx, tenant, thursday)
	if err != nil || len(list) != 1 || list[0].Status != procurement.OrderReceived {
		t.Fatalf("ListOrders() = %+v, %v", list, err)
	}
	for _, it := range list[0].Items {
		if it.IngredientID == flour && (it.Status != procurement.ItemReceived || *it.QtyReceived != 1800 || it.ReceivedAt == nil) {
			t.Errorf("flour = %+v, want received 1,800", it)
		}
	}
	if _, err := repo.GetOrder(ctx, dbtest.CreateTenant(t, d, "Toko Lain"), o.ID); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("another tenant's GetOrder() error = %v, want ErrNotFound", err)
	}
}
