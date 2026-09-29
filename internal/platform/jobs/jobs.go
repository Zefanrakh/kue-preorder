// Package jobs runs background jobs on River (docs/architecture.md §21) in
// the worker: the client, how failures alert, tracing and correlation ids
// per job, and the bridge from outbox events to the jobs they start. Modules
// keep their job logic in their own services; their River workers stay thin.
package jobs

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/log"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
)

// Schema holds River's tables (migration 00009).
const Schema = "river"

// defaultMaxWorkers bounds the jobs one worker process runs at once.
const defaultMaxWorkers = 10

// Config is what NewClient needs.
type Config struct {
	Workers        *river.Workers
	Periodic       []*river.PeriodicJob
	Logger         *slog.Logger
	TracerProvider trace.TracerProvider
	// MaxWorkers defaults to 10.
	MaxWorkers int
}

// NewClient returns a River client over d's pool with the project's error
// handling and observability. Start it to work jobs; unstarted, it only
// inserts them.
func NewClient(d *db.DB, c Config) (*river.Client[pgx.Tx], error) {
	if c.MaxWorkers == 0 {
		c.MaxWorkers = defaultMaxWorkers
	}
	client, err := river.NewClient(riverpgxv5.New(d.Pool()), &river.Config{
		Schema:       Schema,
		Queues:       map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: c.MaxWorkers}},
		Workers:      c.Workers,
		PeriodicJobs: c.Periodic,
		Logger:       c.Logger,
		ErrorHandler: &errorHandler{logger: c.Logger},
		Middleware:   []rivertype.Middleware{&observe{tracer: c.TracerProvider.Tracer("kue-preorder/jobs")}},
	})
	if err != nil {
		return nil, fmt.Errorf("create job client: %w", err)
	}
	return client, nil
}

// errorHandler logs a job's failure: WARN while River will try again, ERROR
// (an alert, §22) once it will not. River itself logs failures at INFO.
type errorHandler struct {
	logger *slog.Logger
}

func (h *errorHandler) HandleError(ctx context.Context, job *rivertype.JobRow, err error) *river.ErrorHandlerResult {
	attrs := []any{slog.String("kind", job.Kind), slog.Int64("job_id", job.ID), slog.Int("attempt", job.Attempt), slog.Any("error", err)}
	if job.Attempt >= job.MaxAttempts {
		h.logger.ErrorContext(ctx, "job failed for good", attrs...)
	} else {
		h.logger.WarnContext(ctx, "job failed; retrying", attrs...)
	}
	return nil
}

func (h *errorHandler) HandlePanic(ctx context.Context, job *rivertype.JobRow, panicVal any, stack string) *river.ErrorHandlerResult {
	h.logger.ErrorContext(ctx, "job panicked",
		slog.String("kind", job.Kind), slog.Int64("job_id", job.ID), slog.Int("attempt", job.Attempt),
		slog.Any("panic", panicVal), slog.String("stack", stack))
	return nil
}

// observe gives every job a correlation id, "job-<id>", for its logs, and a
// span, "job <kind>", for its queries and calls.
type observe struct {
	river.MiddlewareDefaults
	tracer trace.Tracer
}

func (o *observe) Work(ctx context.Context, job *rivertype.JobRow, doInner func(context.Context) error) error {
	ctx = log.WithCorrelationID(ctx, fmt.Sprintf("job-%d", job.ID))
	ctx, span := o.tracer.Start(ctx, "job "+job.Kind, trace.WithSpanKind(trace.SpanKindConsumer), trace.WithAttributes(
		attribute.String("job.kind", job.Kind), attribute.Int64("job.id", job.ID), attribute.Int("job.attempt", job.Attempt),
	))
	defer span.End()
	err := doInner(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "job failed")
	}
	return err
}

// Subscriber turns an outbox event into the job it starts.
type Subscriber func(e outbox.Stored) (river.JobArgs, error)

// Dispatcher returns the outbox.Dispatch that inserts the jobs an event's
// subscribers start, in the publisher's transaction. An event nobody
// subscribes to is only marked published. A subscriber that cannot read its
// event is a bug no retry fixes: it is logged at ERROR and the event goes on
// without that job, so one bad event cannot hold up the outbox.
func Dispatcher(client *river.Client[pgx.Tx], subs map[string][]Subscriber, logger *slog.Logger) outbox.Dispatch {
	return func(ctx context.Context, tx pgx.Tx, e outbox.Stored) error {
		var params []river.InsertManyParams
		for _, sub := range subs[e.Type] {
			args, err := sub(e)
			if err != nil {
				logger.ErrorContext(ctx, "outbox event has no job", slog.String("type", e.Type), slog.Int64("seq", e.Seq), slog.Any("error", err))
				continue
			}
			params = append(params, river.InsertManyParams{Args: args})
		}
		if len(params) == 0 {
			return nil
		}
		if _, err := client.InsertManyTx(ctx, tx, params); err != nil {
			return fmt.Errorf("insert jobs for %s: %w", e.Type, err)
		}
		return nil
	}
}

// Merge joins the subscriptions of several modules: an event two modules
// subscribe to starts the jobs of both.
func Merge(all ...map[string][]Subscriber) map[string][]Subscriber {
	out := map[string][]Subscriber{}
	for _, subs := range all {
		for typ, s := range subs {
			out[typ] = append(out[typ], s...)
		}
	}
	return out
}
