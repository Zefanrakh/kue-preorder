//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Zefanrakh/kue-preorder/internal/inventory"
	"github.com/Zefanrakh/kue-preorder/internal/inventory/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

var now = time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)

// ingredient writes an ingredient straight to the table: the catalog is
// another module.
func ingredient(t *testing.T, d *db.DB, tenant uuid.UUID, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := d.Pool().QueryRow(t.Context(), `insert into ingredients (tenant_id, name, base_unit) values ($1, $2, 'g') returning id`, tenant, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// receive stores a lot of qty as the service does.
func receive(t *testing.T, repo *postgres.Repository, tenant, ing uuid.UUID, qty int64, received time.Time, expires *time.Time) inventory.Lot {
	t.Helper()
	l := inventory.Lot{ID: uuid.New(), IngredientID: ing, ReceivedAt: received, ExpiresAt: expires, Status: inventory.LotAvailable, Source: inventory.SourceManual}
	if err := repo.InsertLot(t.Context(), tenant, l, uuid.New(), received); err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertMovement(t.Context(), tenant, inventory.Movement{LotID: l.ID, IngredientID: ing, Kind: inventory.MoveReceive, Qty: qty, ActorID: uuid.New(), At: received}); err != nil {
		t.Fatal(err)
	}
	return l
}

func pgCode(err error) string {
	var e *pgconn.PgError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// Balances are the sum of the ledger, lots come oldest first with their
// latest ok check, and another tenant sees nothing.
func TestLots(t *testing.T) {
	d := dbtest.New(t)
	repo := postgres.NewRepository(d)
	ctx := t.Context()
	tenant := dbtest.DefaultTenantID
	flour, egg := ingredient(t, d, tenant, "Tepung"), ingredient(t, d, tenant, "Telur")
	older := receive(t, repo, tenant, flour, 300, now.Add(-48*time.Hour), nil)
	newer := receive(t, repo, tenant, flour, 1000, now, nil)
	eggs := receive(t, repo, tenant, egg, 12, now, nil)
	noErr(t, repo.InsertMovement(ctx, tenant, inventory.Movement{LotID: newer.ID, IngredientID: flour, Kind: inventory.MoveAdjust, Qty: -250, Reason: "opname", ActorID: uuid.New(), At: now}))
	for _, at := range []time.Time{now.Add(-time.Hour), now} {
		noErr(t, repo.InsertCheck(ctx, tenant, inventory.Check{LotID: eggs.ID, OK: true, CheckedBy: uuid.New(), At: at}))
	}
	noErr(t, repo.InsertCheck(ctx, tenant, inventory.Check{LotID: eggs.ID, Reason: inventory.DiscardSmell, CheckedBy: uuid.New(), At: now.Add(time.Hour)}))

	lots, err := repo.Lots(ctx, tenant, []uuid.UUID{flour}, []inventory.LotStatus{inventory.LotAvailable})
	if err != nil || len(lots) != 2 || lots[0].ID != older.ID || lots[0].Balance != 300 || lots[1].Balance != 750 {
		t.Errorf("Lots(flour) = %+v, %v; want 300 then 750, oldest first", lots, err)
	}
	all, err := repo.Lots(ctx, tenant, nil, []inventory.LotStatus{inventory.LotAvailable})
	if err != nil || len(all) != 3 {
		t.Errorf("Lots(all) = %d, %v; want 3", len(all), err)
	}
	got, err := repo.GetLot(ctx, tenant, eggs.ID)
	if err != nil || got.LastOKAt == nil || !got.LastOKAt.Equal(now) || got.Balance != 12 {
		t.Errorf("GetLot(eggs) = %+v, %v; want the latest ok check, not the later discard", got, err)
	}
	other := dbtest.CreateTenant(t, d, "Toko Lain")
	if lots, _ := repo.Lots(ctx, other, nil, []inventory.LotStatus{inventory.LotAvailable}); len(lots) != 0 {
		t.Errorf("another tenant sees %d lots", len(lots))
	}
}

// The ledger cannot be edited or erased, and a movement cannot claim
// another ingredient than its lot's.
func TestLedgerIsAppendOnly(t *testing.T) {
	d := dbtest.New(t)
	repo := postgres.NewRepository(d)
	ctx := t.Context()
	tenant := dbtest.DefaultTenantID
	flour, egg := ingredient(t, d, tenant, "Tepung"), ingredient(t, d, tenant, "Telur")
	l := receive(t, repo, tenant, flour, 1000, now, nil)

	for _, sql := range []string{"update stock_movements set qty = 1", "delete from stock_movements"} {
		if _, err := d.Pool().Exec(ctx, sql); pgCode(err) != "23001" {
			t.Errorf("%s: error = %v, want restrict_violation", sql, err)
		}
	}
	err := repo.InsertMovement(ctx, tenant, inventory.Movement{LotID: l.ID, IngredientID: egg, Kind: inventory.MoveReceive, Qty: 5, ActorID: uuid.New(), At: now})
	if pgCode(err) != "23503" {
		t.Errorf("a movement for the wrong ingredient: error = %v, want a foreign key violation", err)
	}
	for name, m := range map[string]inventory.Movement{
		"waste in":             {Kind: inventory.MoveWaste, Qty: 5, Reason: "bau"},
		"receive out":          {Kind: inventory.MoveReceive, Qty: -5},
		"adjust without why":   {Kind: inventory.MoveAdjust, Qty: -5},
		"waste without reason": {Kind: inventory.MoveWaste, Qty: -5},
	} {
		m.LotID, m.IngredientID, m.ActorID, m.At = l.ID, flour, uuid.New(), now
		if err := repo.InsertMovement(ctx, tenant, m); pgCode(err) != "23514" {
			t.Errorf("%s: error = %v, want a check violation", name, err)
		}
	}
}

func TestExpireLots(t *testing.T) {
	d := dbtest.New(t)
	repo := postgres.NewRepository(d)
	ctx := t.Context()
	tenant := dbtest.DefaultTenantID
	flour := ingredient(t, d, tenant, "Tepung")
	past, later := now.Add(-time.Minute), now.Add(time.Hour)
	stale := receive(t, repo, tenant, flour, 100, now.Add(-72*time.Hour), &past)
	receive(t, repo, tenant, flour, 100, now.Add(-72*time.Hour), &later)
	receive(t, repo, tenant, flour, 100, now.Add(-72*time.Hour), nil)

	n, err := repo.ExpireLots(ctx, tenant, now)

	if err != nil || n != 1 {
		t.Fatalf("ExpireLots() = %d, %v; want 1", n, err)
	}
	if l, _ := repo.GetLot(ctx, tenant, stale.ID); l.Status != inventory.LotExpired || l.Balance != 100 {
		t.Errorf("stale lot = %+v, want expired with its stock still in the ledger", l)
	}
	if n, _ := repo.ExpireLots(ctx, tenant, now); n != 0 {
		t.Errorf("second run = %d, want 0", n)
	}
}

// Changes to one ingredient take turns under its lock, inside the caller's
// transaction.
func TestLockIngredientJoinsTheTransaction(t *testing.T) {
	d := dbtest.New(t)
	repo := postgres.NewRepository(d)
	flour := ingredient(t, d, dbtest.DefaultTenantID, "Tepung")
	err := d.Tx(t.Context(), func(ctx context.Context) error {
		if err := repo.LockIngredient(ctx, flour); err != nil {
			return err
		}
		return repo.LockIngredient(ctx, flour) // re-entrant within one transaction
	})
	noErr(t, err)
}

func noErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
