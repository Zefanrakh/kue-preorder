//go:build integration

package migrations_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

// Tables that are not owned by a tenant.
var globalTables = []string{
	"goose_db_version", // goose's bookkeeping
	"tenants",          // the tenants themselves
	"webhook_events",   // providers deliver events before the tenant is known (§9.1)
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func TestMigrations_UpDownUp(t *testing.T) {
	migrator, err := db.NewMigrator(dbtest.NewEmptyURL(t))
	if err != nil {
		t.Fatalf("NewMigrator() error = %v", err)
	}
	defer migrator.Close()
	ctx := t.Context()
	sources := migrator.ListSources()
	latest := sources[len(sources)-1].Version

	if _, err := migrator.Up(ctx); err != nil {
		t.Fatalf("first Up() error = %v", err)
	}
	down, err := migrator.DownTo(ctx, 0)
	if err != nil {
		t.Fatalf("DownTo(0) error = %v", err)
	}
	if len(down) != len(sources) {
		t.Errorf("DownTo(0) rolled back %d migrations, want %d", len(down), len(sources))
	}
	if _, err := migrator.Up(ctx); err != nil {
		t.Fatalf("second Up() error = %v", err)
	}
	if got, err := migrator.GetDBVersion(ctx); err != nil || got != latest {
		t.Errorf("GetDBVersion() = %d, %v; want %d", got, err, latest)
	}
}

func TestSchema_EveryTenantTableHasTenantID(t *testing.T) {
	d := dbtest.New(t)
	rows, err := d.Pool().Query(t.Context(), `
		select c.relname,
		       a.attname is not null,
		       coalesce(format_type(a.atttypid, a.atttypmod) = 'uuid', false),
		       coalesce(a.attnotnull, false),
		       exists (
		           select 1 from pg_constraint k
		           where k.conrelid = c.oid
		             and k.contype = 'f'
		             and k.confrelid = 'public.tenants'::regclass
		             and k.conkey = array[a.attnum]
		       )
		from pg_class c
		join pg_namespace n on n.oid = c.relnamespace
		left join pg_attribute a
		       on a.attrelid = c.oid and a.attname = 'tenant_id' and not a.attisdropped
		where n.nspname = 'public' and c.relkind in ('r', 'p')
		order by c.relname`)
	if err != nil {
		t.Fatalf("query catalog: %v", err)
	}
	defer rows.Close()

	checked := 0
	for rows.Next() {
		var (
			table                                   string
			hasColumn, isUUID, notNull, refsTenants bool
		)
		if err := rows.Scan(&table, &hasColumn, &isUUID, &notNull, &refsTenants); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if slices.Contains(globalTables, table) {
			continue
		}
		checked++
		switch {
		case !hasColumn:
			t.Errorf("%s has no tenant_id column", table)
		case !isUUID || !notNull:
			t.Errorf("%s.tenant_id must be uuid not null", table)
		case !refsTenants:
			t.Errorf("%s.tenant_id has no foreign key to tenants(id)", table)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if checked == 0 {
		t.Fatal("no tenant-owned tables found; the catalog query is wrong")
	}
}

func TestSchema_EveryTableHasRLSEnabled(t *testing.T) {
	d := dbtest.New(t)
	rows, err := d.Pool().Query(t.Context(), `
		select c.relname, c.relrowsecurity
		from pg_class c
		join pg_namespace n on n.oid = c.relnamespace
		where n.nspname = 'public' and c.relkind in ('r', 'p')
		order by c.relname`)
	if err != nil {
		t.Fatalf("query catalog: %v", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var (
			table string
			rls   bool
		)
		if err := rows.Scan(&table, &rls); err != nil {
			t.Fatalf("scan: %v", err)
		}
		tables = append(tables, table)
		if !rls {
			t.Errorf("%s has row level security disabled; the Supabase Data API would expose it", table)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if !slices.Contains(tables, "goose_db_version") {
		t.Errorf("tables = %v, want goose_db_version included", tables)
	}
}

func TestSeed_DefaultTenantExists(t *testing.T) {
	d := dbtest.New(t)

	var name string
	err := d.Pool().QueryRow(t.Context(), "select name from tenants where id = $1", dbtest.DefaultTenantID).Scan(&name)
	if err != nil {
		t.Fatalf("select default tenant: %v", err)
	}
	if name == "" {
		t.Error("default tenant has an empty name")
	}
}

func TestWebhookEvents_RejectsDuplicateProviderEvent(t *testing.T) {
	d := dbtest.New(t)
	insert := "insert into webhook_events (provider, event_id, received_at) values ($1, $2, $3)"
	at := time.Date(2026, 9, 24, 3, 0, 0, 0, time.UTC)

	if _, err := d.Pool().Exec(t.Context(), insert, "xendit", "evt_1", at); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := d.Pool().Exec(t.Context(), insert, "biteship", "evt_1", at); err != nil {
		t.Errorf("same event id from another provider: %v, want accepted", err)
	}

	_, err := d.Pool().Exec(t.Context(), insert, "xendit", "evt_1", at.Add(time.Minute))
	if code := pgCode(err); code != "23505" { // unique_violation
		t.Errorf("duplicate delivery error = %v (code %q), want unique_violation", err, code)
	}
}
