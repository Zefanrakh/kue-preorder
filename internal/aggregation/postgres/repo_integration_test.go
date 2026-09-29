//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/aggregation"
	"github.com/Zefanrakh/kue-preorder/internal/aggregation/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

var now = time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC)

func day(d int) clock.Date { return clock.Date{Year: 2026, Month: time.October, Day: d} }

// kitchen is a component, two ingredients, and a supplier, written straight
// to the tables: the catalog is another module.
type kitchen struct {
	dough, flour, egg, supplier uuid.UUID
}

func seed(t *testing.T, d *db.DB, tenant uuid.UUID) kitchen {
	t.Helper()
	ctx := t.Context()
	var k kitchen
	for _, q := range []struct {
		sql string
		id  *uuid.UUID
	}{
		{"insert into components (tenant_id, name, unit_label, created_at, updated_at) values ($1, 'Adonan donut', 'porsi', now(), now()) returning id", &k.dough},
		{"insert into ingredients (tenant_id, name, base_unit, is_perishable, leftover_policy, created_at, updated_at) values ($1, 'Tepung', 'g', false, 'auto', now(), now()) returning id", &k.flour},
		{"insert into ingredients (tenant_id, name, base_unit, is_perishable, leftover_policy, created_at, updated_at) values ($1, 'Telur', 'pcs', true, 'confirm', now(), now()) returning id", &k.egg},
		{"insert into suppliers (tenant_id, name, adapter_key, created_at, updated_at) values ($1, 'Toko Sinar', 'manual', now(), now()) returning id", &k.supplier},
	} {
		if err := d.Pool().QueryRow(ctx, q.sql, tenant).Scan(q.id); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return k
}

func price(p int64) *int64 { return &p }

// compute saves a result for date as the engine does: in one transaction
// with the batch locked.
func compute(t *testing.T, d *db.DB, repo *postgres.Repository, tenant uuid.UUID, date clock.Date, components []aggregation.ComponentTotal, lines []aggregation.Line) aggregation.Batch {
	t.Helper()
	var b aggregation.Batch
	err := d.Tx(t.Context(), func(ctx context.Context) error {
		if err := repo.EnsureBatch(ctx, tenant, date, now); err != nil {
			return err
		}
		var err error
		if b, err = repo.LockBatch(ctx, tenant, date); err != nil {
			return err
		}
		return repo.SaveResult(ctx, tenant, b.ID, components, lines, now)
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSaveAndRead(t *testing.T) {
	d := dbtest.New(t)
	repo := postgres.NewRepository(d)
	ctx := t.Context()
	tenant := dbtest.DefaultTenantID
	k := seed(t, d, tenant)
	pack := &catalog.DefaultPack{IngredientID: k.flour, SupplierID: k.supplier, Size: 1000, Unit: "kg", PriceIDR: price(14000)}

	b := compute(t, d, repo, tenant, day(7),
		[]aggregation.ComponentTotal{{ComponentID: k.dough, Units: 12.5}},
		[]aggregation.Line{
			{IngredientID: k.flour, Needed: 2500, UsableStock: 700, ToBuy: 1800, Pack: pack, Packs: 2, Status: aggregation.LineNeeded},
			{IngredientID: k.egg, Needed: 8, ToBuy: 8, Status: aggregation.LineNeeded},
		})

	got, err := repo.GetBatch(ctx, tenant, day(7))
	if err != nil || got.ID != b.ID || got.Status != aggregation.BatchOpen || got.ComputedAt == nil || !got.ComputedAt.Equal(now) || got.Error != "" {
		t.Errorf("GetBatch() = %+v, %v", got, err)
	}
	if cs, err := repo.Components(ctx, tenant, b.ID); err != nil || len(cs) != 1 || cs[0].Units != 12.5 {
		t.Errorf("Components() = %+v, %v", cs, err)
	}
	lines, err := repo.Lines(ctx, tenant, b.ID)
	if err != nil || len(lines) != 2 {
		t.Fatalf("Lines() = %+v, %v", lines, err)
	}
	for _, l := range lines {
		switch l.IngredientID {
		case k.flour:
			if l.Needed != 2500 || l.UsableStock != 700 || l.ToBuy != 1800 || l.Packs != 2 || l.Pack == nil || l.Pack.Size != 1000 ||
				l.Pack.Unit != "kg" || *l.Pack.PriceIDR != 14000 || l.Pack.SupplierID != k.supplier {
				t.Errorf("flour = %+v (pack %+v)", l, l.Pack)
			}
		case k.egg:
			if l.ToBuy != 8 || l.Pack != nil || l.Packs != 0 {
				t.Errorf("eggs = %+v, want 8 without a pack", l)
			}
		}
	}
	list, err := repo.ListBatches(ctx, tenant, day(1), day(31))
	if err != nil || len(list) != 1 || list[0].LinesToBuy != 2 || list[0].CostIDR != 28000 {
		t.Errorf("ListBatches() = %+v, %v; want 2 lines to buy costing 28,000", list, err)
	}
	if _, err := repo.GetBatch(ctx, dbtest.CreateTenant(t, d, "Toko Lain"), day(7)); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("another tenant's GetBatch() error = %v, want ErrNotFound", err)
	}
}

// A computation replaces what it computes and leaves what procurement set:
// an ordered line stays with its ordered quantity and status, and only the
// unordered lines no longer needed go.
func TestSaveResult_KeepsWhatProcurementSet(t *testing.T) {
	d := dbtest.New(t)
	repo := postgres.NewRepository(d)
	ctx := t.Context()
	tenant := dbtest.DefaultTenantID
	k := seed(t, d, tenant)
	b := compute(t, d, repo, tenant, day(7),
		[]aggregation.ComponentTotal{{ComponentID: k.dough, Units: 12}},
		[]aggregation.Line{{IngredientID: k.flour, Needed: 2000, ToBuy: 2000}, {IngredientID: k.egg, Needed: 8, ToBuy: 8}})
	if _, err := d.Pool().Exec(ctx, "update batch_requirements set status = 'ordered', qty_ordered = 2000 where ingredient_id = $1", k.flour); err != nil {
		t.Fatal(err)
	}

	// The next computation: no eggs at all any more, flour grew, and it
	// claims (wrongly) that nothing was ordered.
	compute(t, d, repo, tenant, day(7), nil,
		[]aggregation.Line{{IngredientID: k.flour, Needed: 2600, ToBuy: 600, Ordered: 0, Status: aggregation.LineNeeded}})

	lines, err := repo.Lines(ctx, tenant, b.ID)
	if err != nil || len(lines) != 1 {
		t.Fatalf("Lines() = %+v, %v; want only the flour", lines, err)
	}
	if f := lines[0]; f.Needed != 2600 || f.ToBuy != 600 || f.Ordered != 2000 || f.Status != aggregation.LineOrdered {
		t.Errorf("flour = %+v, want the new need with the 2,000 ordered and its status kept", f)
	}
	if cs, _ := repo.Components(ctx, tenant, b.ID); len(cs) != 0 {
		t.Errorf("components = %+v, want them replaced by none", cs)
	}
}

func TestMarkFailed(t *testing.T) {
	d := dbtest.New(t)
	repo := postgres.NewRepository(d)
	ctx := t.Context()
	tenant := dbtest.DefaultTenantID

	if err := repo.MarkFailed(ctx, tenant, day(8), "Resep Adonan donut untuk bahan Telur tidak bisa dihitung.", now); err != nil {
		t.Fatal(err)
	}
	b, err := repo.GetBatch(ctx, tenant, day(8))
	if err != nil || b.Error == "" || b.ComputedAt != nil {
		t.Fatalf("GetBatch() = %+v, %v; want a new batch carrying the error", b, err)
	}
	compute(t, d, repo, tenant, day(8), nil, nil)
	if b, _ := repo.GetBatch(ctx, tenant, day(8)); b.Error != "" || b.ComputedAt == nil {
		t.Errorf("after a success: %+v, want the error cleared", b)
	}
}

func TestUpcomingDates(t *testing.T) {
	d := dbtest.New(t)
	repo := postgres.NewRepository(d)
	ctx := t.Context()
	tenant := dbtest.DefaultTenantID
	for _, dd := range []int{4, 5, 7, 9} {
		compute(t, d, repo, tenant, day(dd), nil, nil)
	}
	if _, err := d.Pool().Exec(ctx, "update production_batches set status = 'done' where batch_date = '2026-10-09'"); err != nil {
		t.Fatal(err)
	}

	got, err := repo.UpcomingDates(ctx, tenant, day(5))

	if err != nil || len(got) != 2 || got[0] != day(5) || got[1] != day(7) {
		t.Errorf("UpcomingDates() = %v, %v; want the 5th and the 7th", got, err)
	}
}

// A batch sees only what the earlier batches that are not done count on.
func TestClaimedBefore(t *testing.T) {
	d := dbtest.New(t)
	repo := postgres.NewRepository(d)
	ctx := t.Context()
	tenant := dbtest.DefaultTenantID
	k := seed(t, d, tenant)
	claim := func(date int, flour int64) {
		compute(t, d, repo, tenant, day(date), nil, []aggregation.Line{{IngredientID: k.flour, Needed: flour, UsableStock: flour}})
	}
	claim(5, 300)
	claim(6, 200)
	claim(8, 900)
	if _, err := d.Pool().Exec(ctx, "update production_batches set status = 'done' where batch_date = '2026-10-05'"); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ClaimedBefore(ctx, tenant, day(8), []uuid.UUID{k.flour, k.egg})

	if err != nil || got[k.flour] != 200 || len(got) != 1 {
		t.Errorf("ClaimedBefore(8th) = %v, %v; want the 6th's 200 g only (the 5th is done, the 8th is not before)", got, err)
	}
}

// A batch moves on only from the statuses given; open batches list oldest
// first.
func TestSetStatus(t *testing.T) {
	d := dbtest.New(t)
	repo := postgres.NewRepository(d)
	ctx := t.Context()
	tenant := dbtest.DefaultTenantID
	compute(t, d, repo, tenant, day(8), nil, nil)
	compute(t, d, repo, tenant, day(7), nil, nil)

	if dates, err := repo.OpenDates(ctx, tenant); err != nil || len(dates) != 2 || dates[0] != day(7) {
		t.Fatalf("OpenDates() = %v, %v; want the 7th then the 8th", dates, err)
	}
	moved, err := repo.SetStatus(ctx, tenant, day(7), []aggregation.BatchStatus{aggregation.BatchOpen}, aggregation.BatchLocked, now)
	if err != nil || !moved {
		t.Fatalf("SetStatus(open → locked) = %t, %v", moved, err)
	}
	if moved, _ := repo.SetStatus(ctx, tenant, day(7), []aggregation.BatchStatus{aggregation.BatchOpen}, aggregation.BatchLocked, now); moved {
		t.Error("a locked batch was locked again")
	}
	if moved, _ := repo.SetStatus(ctx, tenant, day(9), []aggregation.BatchStatus{aggregation.BatchOpen}, aggregation.BatchLocked, now); moved {
		t.Error("a batch that does not exist moved")
	}
	if b, _ := repo.GetBatch(ctx, tenant, day(7)); b.Status != aggregation.BatchLocked {
		t.Errorf("status = %s, want locked", b.Status)
	}
	if dates, _ := repo.OpenDates(ctx, tenant); len(dates) != 1 || dates[0] != day(8) {
		t.Errorf("OpenDates() = %v, want the 8th only", dates)
	}
}
