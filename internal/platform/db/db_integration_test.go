//go:build integration

package db_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

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

func TestOpen_TracesQueriesWithoutParameterValues(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	d, err := db.Open(t.Context(), dbtest.NewEmptyURL(t), db.WithTracerProvider(tp))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(d.Close)
	// otelpgx only traces queries inside a recording span, as in production,
	// where every query runs under an RPC (or job) span.
	ctx, rpc := tp.Tracer("test").Start(t.Context(), "IdentityService/WhoAmI")

	if _, err := d.Pool().Exec(ctx, "select $1::text", "sari@example.com"); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	rpc.End()

	var query sdktrace.ReadOnlySpan
	for _, s := range recorder.Ended() {
		for _, a := range s.Attributes() {
			if a.Key == "db.query.text" && strings.Contains(a.Value.AsString(), "select $1::text") {
				query = s
			}
		}
	}
	if query == nil {
		t.Fatalf("no span with the query text among %d spans", len(recorder.Ended()))
	}
	if query.Parent().SpanID() != rpc.SpanContext().SpanID() {
		t.Error("query span is not a child of the RPC span")
	}
	for _, a := range query.Attributes() {
		if strings.Contains(a.Value.String(), "sari@example.com") {
			t.Errorf("span attribute %s records a parameter value", a.Key)
		}
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
