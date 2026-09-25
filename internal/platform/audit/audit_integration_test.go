//go:build integration

package audit_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Zefanrakh/kue-preorder/internal/platform/audit"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

var at = time.Date(2026, 9, 25, 8, 30, 0, 0, time.UTC)

func priceChange(entityID uuid.UUID) audit.Entry {
	return audit.Entry{
		TenantID: dbtest.DefaultTenantID,
		ActorID:  uuid.New(),
		Action:   "catalog.variant.price_changed",
		Entity:   "product_variant",
		EntityID: entityID,
		Before:   map[string]int64{"price_idr": 8000},
		After:    map[string]int64{"price_idr": 9000},
		Reason:   "  harga coklat naik  ",
		At:       at,
	}
}

func count(t *testing.T, d *db.DB) int {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(t.Context(), "select count(*) from audit_log").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRecord_WritesTheEntry(t *testing.T) {
	d := dbtest.New(t)
	entity := uuid.New()

	err := d.InTx(t.Context(), func(tx pgx.Tx) error { return audit.Record(t.Context(), tx, priceChange(entity)) })
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}

	var (
		action, reason string
		before, after  []byte
		createdAt      time.Time
	)
	err = d.Pool().QueryRow(t.Context(),
		"select action, reason, before, after, created_at from audit_log where entity_id = $1", entity).
		Scan(&action, &reason, &before, &after, &createdAt)
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	var b, a map[string]int64
	if json.Unmarshal(before, &b) != nil || json.Unmarshal(after, &a) != nil || b["price_idr"] != 8000 || a["price_idr"] != 9000 {
		t.Errorf("before/after = %s/%s", before, after)
	}
	if action != "catalog.variant.price_changed" || reason != "harga coklat naik" || !createdAt.Equal(at) {
		t.Errorf("entry = %q %q %v; want the action, the trimmed reason, and the clock time", action, reason, createdAt)
	}
}

func TestRecord_RollsBackWithTheChange(t *testing.T) {
	d := dbtest.New(t)
	errChangeFailed := errors.New("change failed")

	err := d.InTx(t.Context(), func(tx pgx.Tx) error {
		if err := audit.Record(t.Context(), tx, priceChange(uuid.New())); err != nil {
			return err
		}
		return errChangeFailed
	})

	if !errors.Is(err, errChangeFailed) {
		t.Fatalf("InTx() error = %v", err)
	}
	if n := count(t, d); n != 0 {
		t.Errorf("audit entries = %d, want 0 after the change rolled back", n)
	}
}

func TestRecord_RejectsBadEntries(t *testing.T) {
	d := dbtest.New(t)
	noReason := priceChange(uuid.New())
	noReason.Reason = "   "
	badAction := priceChange(uuid.New())
	badAction.Action = "PriceChanged"

	for name, e := range map[string]audit.Entry{"no reason": noReason, "action not dotted lowercase": badAction} {
		t.Run(name, func(t *testing.T) {
			err := d.InTx(t.Context(), func(tx pgx.Tx) error { return audit.Record(t.Context(), tx, e) })
			if err == nil {
				t.Error("Record() error = nil")
			}
		})
	}
	if err := d.InTx(t.Context(), func(tx pgx.Tx) error { return audit.Record(t.Context(), tx, noReason) }); !errors.Is(err, audit.ErrReasonRequired) {
		t.Errorf("error = %v, want ErrReasonRequired", err)
	}
}

func TestAuditLog_IsAppendOnly(t *testing.T) {
	d := dbtest.New(t)
	entity := uuid.New()
	if err := d.InTx(t.Context(), func(tx pgx.Tx) error { return audit.Record(t.Context(), tx, priceChange(entity)) }); err != nil {
		t.Fatal(err)
	}

	for _, sql := range []string{
		"update audit_log set reason = 'rewritten' where entity_id = $1",
		"delete from audit_log where entity_id = $1",
	} {
		_, err := d.Pool().Exec(t.Context(), sql, entity)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23001" { // restrict_violation
			t.Errorf("%q: error = %v, want restrict_violation", sql, err)
		}
	}
	if n := count(t, d); n != 1 {
		t.Errorf("audit entries = %d, want the original 1", n)
	}
}
