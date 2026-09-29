//go:build integration

package jobs_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
	"github.com/Zefanrakh/kue-preorder/internal/platform/jobs"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

type pingArgs struct {
	Seq int64 `json:"seq"`
}

func (pingArgs) Kind() string { return "ping" }

type pingWorker struct {
	river.WorkerDefaults[pingArgs]
}

func (pingWorker) Work(context.Context, *river.Job[pingArgs]) error { return nil }

func emit(t *testing.T, d *db.DB, typ string) {
	t.Helper()
	err := outbox.Append(t.Context(), d.Pool(), outbox.Event{
		TenantID: dbtest.DefaultTenantID, Aggregate: "order", Type: typ, Payload: map[string]any{"code": "K7M3QX"},
		At: clock.Real{}.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func jobCount(t *testing.T, d *db.DB) int {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(t.Context(), "select count(*) from river.river_job where kind = 'ping'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Subscribed events start their jobs in the publisher's transaction; others,
// and those a subscriber passes on with nil, are only published; a
// subscriber that cannot read its event alerts and is skipped, without
// holding up the outbox.
func TestDispatcher(t *testing.T) {
	d := dbtest.New(t)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	workers := river.NewWorkers()
	river.AddWorker(workers, pingWorker{})
	client, err := jobs.NewClient(d, jobs.Config{Workers: workers, Logger: logger, TracerProvider: sdktrace.NewTracerProvider()})
	if err != nil {
		t.Fatal(err)
	}
	subs := map[string][]jobs.Subscriber{
		"order.confirmed": {func(e outbox.Stored) (river.JobArgs, error) { return pingArgs{Seq: e.Seq}, nil }},
		"order.cancelled": {func(outbox.Stored) (river.JobArgs, error) { return nil, errors.New("no order_id") }},
		// Cares about this event only sometimes: nil starts nothing.
		"order.placed": {func(outbox.Stored) (river.JobArgs, error) { return nil, nil }},
	}
	publisher := outbox.NewPublisher(d, jobs.Dispatcher(client, subs, logger), clock.Real{}, logger)
	emit(t, d, "order.confirmed")
	emit(t, d, "order.placed")
	emit(t, d, "order.cancelled")
	emit(t, d, "order.confirmed")

	n, err := publisher.PublishBatch(t.Context())

	if err != nil || n != 4 {
		t.Fatalf("PublishBatch() = %d, %v; want all four published", n, err)
	}
	if got := jobCount(t, d); got != 2 {
		t.Errorf("%d ping jobs, want one per order.confirmed", got)
	}
	if got := logs.String(); !strings.Contains(got, `"level":"ERROR"`) || !strings.Contains(got, "order.cancelled") {
		t.Errorf("logs = %s, want an alert for the unreadable event", got)
	}
}

// The jobs commit with the events marked published, or not at all.
func TestDispatcher_JobsRollBackWithTheBatch(t *testing.T) {
	d := dbtest.New(t)
	workers := river.NewWorkers()
	river.AddWorker(workers, pingWorker{})
	logger := slog.New(slog.DiscardHandler)
	client, err := jobs.NewClient(d, jobs.Config{Workers: workers, Logger: logger, TracerProvider: sdktrace.NewTracerProvider()})
	if err != nil {
		t.Fatal(err)
	}
	dispatch := jobs.Dispatcher(client, map[string][]jobs.Subscriber{
		"order.confirmed": {func(e outbox.Stored) (river.JobArgs, error) { return pingArgs{Seq: e.Seq}, nil }},
	}, logger)
	boom := errors.New("boom")
	calls := 0
	publisher := outbox.NewPublisher(d, func(ctx context.Context, tx pgx.Tx, e outbox.Stored) error {
		if err := dispatch(ctx, tx, e); err != nil {
			return err
		}
		if calls++; calls == 2 {
			return boom // the second event fails after the first one's job was inserted
		}
		return nil
	}, clock.Real{}, logger)
	emit(t, d, "order.confirmed")
	emit(t, d, "order.confirmed")

	if _, err := publisher.PublishBatch(t.Context()); !errors.Is(err, boom) {
		t.Fatalf("PublishBatch() error = %v, want boom", err)
	}
	if got := jobCount(t, d); got != 0 {
		t.Errorf("%d jobs after the rollback, want none", got)
	}
}
