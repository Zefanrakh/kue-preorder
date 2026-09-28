//go:build integration

package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
)

// recorder keeps what it was dispatched, and fails on demand.
type recorder struct {
	got  []outbox.Stored
	fail error
}

func (r *recorder) dispatch(_ context.Context, _ pgx.Tx, e outbox.Stored) error {
	if r.fail != nil {
		return r.fail
	}
	r.got = append(r.got, e)
	return nil
}

func (r *recorder) components() []string {
	var out []string
	for _, e := range r.got {
		var p struct {
			Component string `json:"component_id"`
		}
		_ = json.Unmarshal(e.Payload, &p)
		out = append(out, p.Component)
	}
	return out
}

func appendAll(t *testing.T, d *db.DB, components ...uuid.UUID) {
	t.Helper()
	// One transaction and one instant: only seq tells them apart.
	err := d.InTx(t.Context(), func(tx pgx.Tx) error {
		for _, c := range components {
			if err := outbox.Append(t.Context(), tx, recipeChanged(c)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func unpublished(t *testing.T, d *db.DB) int {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(t.Context(), "select count(*) from outbox where published_at is null").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Events leave in the order they were written, even at one instant, and
// only once.
func TestPublisher_InOrderAndOnce(t *testing.T) {
	d := dbtest.New(t)
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	appendAll(t, d, ids...)
	r := &recorder{}
	p := outbox.NewPublisher(d, r.dispatch, clock.NewFake(at), slog.New(slog.DiscardHandler))

	n, err := p.PublishBatch(t.Context())

	if err != nil || n != 5 {
		t.Fatalf("PublishBatch() = %d, %v; want 5", n, err)
	}
	want := make([]string, len(ids))
	for i, id := range ids {
		want[i] = id.String()
	}
	if got := r.components(); !slices.Equal(got, want) {
		t.Errorf("dispatched %v, want the order written %v", got, want)
	}
	for i := 1; i < len(r.got); i++ {
		if r.got[i].Seq <= r.got[i-1].Seq {
			t.Errorf("seq %d after %d", r.got[i].Seq, r.got[i-1].Seq)
		}
	}
	if e := r.got[0]; e.TenantID != dbtest.DefaultTenantID || e.Type != "catalog.recipe_changed" || e.Aggregate != "component" || !e.CreatedAt.Equal(at) {
		t.Errorf("event = %+v", e)
	}
	if n, err := p.PublishBatch(t.Context()); err != nil || n != 0 || len(r.got) != 5 || unpublished(t, d) != 0 {
		t.Errorf("second PublishBatch() = %d, %v with %d dispatched; want nothing left", n, err, len(r.got))
	}
}

// A failed dispatch rolls the batch back: nothing is marked published, and
// the next try sends everything.
func TestPublisher_RetriesAFailedBatch(t *testing.T) {
	d := dbtest.New(t)
	appendAll(t, d, uuid.New(), uuid.New())
	r := &recorder{fail: errors.New("queue unreachable")}
	p := outbox.NewPublisher(d, r.dispatch, clock.NewFake(at), slog.New(slog.DiscardHandler))

	if _, err := p.PublishBatch(t.Context()); err == nil || unpublished(t, d) != 2 {
		t.Fatalf("PublishBatch() error = %v with %d unpublished; want an error and both kept", err, unpublished(t, d))
	}
	r.fail = nil
	if n, err := p.PublishBatch(t.Context()); err != nil || n != 2 || unpublished(t, d) != 0 {
		t.Errorf("retry = %d, %v; want both published", n, err)
	}
}

// While another publisher holds the lock, this one steps aside.
func TestPublisher_OneAtATime(t *testing.T) {
	d := dbtest.New(t)
	appendAll(t, d, uuid.New())
	r := &recorder{}
	p := outbox.NewPublisher(d, r.dispatch, clock.NewFake(at), slog.New(slog.DiscardHandler))
	ctx := t.Context()
	other, err := d.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Rollback(ctx) }()
	if _, err := other.Exec(ctx, "select pg_advisory_xact_lock(hashtextextended('outbox:publish', 0))"); err != nil {
		t.Fatal(err)
	}

	if n, err := p.PublishBatch(ctx); err != nil || n != 0 || len(r.got) != 0 {
		t.Errorf("PublishBatch() while locked = %d, %v; want 0 at once", n, err)
	}
	if err := other.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := p.PublishBatch(ctx); err != nil || n != 1 {
		t.Errorf("PublishBatch() after = %d, %v; want 1", n, err)
	}
}
