//go:build integration

package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func insertTenant(ctx context.Context, tx pgx.Tx, name string) error {
	_, err := tx.Exec(ctx, "insert into tenants (name) values ($1)", name)
	return err
}

func tenantExists(t *testing.T, d *db.DB, name string) bool {
	t.Helper()
	var exists bool
	if err := d.Pool().QueryRow(t.Context(), "select exists (select 1 from tenants where name = $1)", name).Scan(&exists); err != nil {
		t.Fatalf("check tenant: %v", err)
	}
	return exists
}

func TestPing_ReachesDatabase(t *testing.T) {
	d := dbtest.New(t)

	if err := d.Ping(t.Context()); err != nil {
		t.Errorf("Ping() error = %v", err)
	}
}

func TestInTx_CommitsOnNil(t *testing.T) {
	d := dbtest.New(t)

	err := d.InTx(t.Context(), func(tx pgx.Tx) error {
		return insertTenant(t.Context(), tx, "committed")
	})

	if err != nil {
		t.Fatalf("InTx() error = %v", err)
	}
	if !tenantExists(t, d, "committed") {
		t.Error("row missing after commit")
	}
}

func TestInTx_RollsBackOnError(t *testing.T) {
	d := dbtest.New(t)
	errBoom := errors.New("boom")

	err := d.InTx(t.Context(), func(tx pgx.Tx) error {
		if err := insertTenant(t.Context(), tx, "rolled back"); err != nil {
			return err
		}
		return errBoom
	})

	if !errors.Is(err, errBoom) {
		t.Fatalf("InTx() error = %v, want %v", err, errBoom)
	}
	if tenantExists(t, d, "rolled back") {
		t.Error("row persisted although fn returned an error")
	}
}

func TestInTx_RollsBackOnPanic(t *testing.T) {
	d := dbtest.New(t)

	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic was swallowed; InTx must re-panic after rollback")
			}
		}()
		_ = d.InTx(t.Context(), func(tx pgx.Tx) error {
			if err := insertTenant(t.Context(), tx, "panicked"); err != nil {
				return err
			}
			panic("boom")
		})
	}()

	if tenantExists(t, d, "panicked") {
		t.Error("row persisted although fn panicked")
	}
}
