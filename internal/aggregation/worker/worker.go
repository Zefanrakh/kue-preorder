// Package worker holds the aggregation module's background jobs
// (docs/architecture.md §21): computing a batch again when its orders
// change, and every upcoming batch when a recipe changes.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/Zefanrakh/kue-preorder/internal/aggregation"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/jobs"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
)

// recomputeAttempts spreads the retries of a failing database over about
// an hour; a batch is computed again with its next order anyway.
const recomputeAttempts = 8

// RecomputeBatchArgs asks for a batch, and those after it, to be computed
// again.
type RecomputeBatchArgs struct {
	TenantID uuid.UUID `json:"tenant_id"`
	Date     string    `json:"date"` // such as "2026-10-07"
}

// Kind implements river.JobArgs.
func (RecomputeBatchArgs) Kind() string { return "recompute-batch" }

// InsertOpts implements river.JobArgsWithInsertOpts. Jobs of one batch are
// not made unique: River would drop one inserted while another runs, and
// that one might miss the latest order. A computation is idempotent, so an
// extra one costs only time.
func (RecomputeBatchArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: recomputeAttempts}
}

// RecomputeUpcomingArgs asks for every upcoming batch to be computed again.
type RecomputeUpcomingArgs struct {
	TenantID uuid.UUID `json:"tenant_id"`
}

// Kind implements river.JobArgs.
func (RecomputeUpcomingArgs) Kind() string { return "recompute-upcoming-batches" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (RecomputeUpcomingArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: recomputeAttempts}
}

// Deps are what the workers work with.
type Deps struct {
	Engine *aggregation.Engine
	Logger *slog.Logger
}

// Register adds the aggregation module's workers.
func Register(workers *river.Workers, d Deps) {
	river.AddWorker(workers, &recomputeWorker{deps: d})
	river.AddWorker(workers, &upcomingWorker{deps: d})
}

// Subscriptions are the outbox events that change a batch: an order that
// starts or stops counting for production (§13), any recipe change, and any
// stock change (§12).
func Subscriptions() map[string][]jobs.Subscriber {
	return map[string][]jobs.Subscriber{
		"order.confirmed":         {recomputeBatch},
		"order.cancelled":         {recomputeBatch},
		"catalog.recipe_changed":  {recomputeUpcoming},
		"inventory.stock_changed": {recomputeUpcoming},
	}
}

func recomputeBatch(e outbox.Stored) (river.JobArgs, error) {
	var p struct {
		ProductionDate string `json:"production_date"`
	}
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("read event %d: %w", e.Seq, err)
	}
	if _, err := clock.ParseDate(p.ProductionDate); err != nil {
		return nil, fmt.Errorf("event %d has no production_date: %w", e.Seq, err)
	}
	return RecomputeBatchArgs{TenantID: e.TenantID, Date: p.ProductionDate}, nil
}

func recomputeUpcoming(e outbox.Stored) (river.JobArgs, error) {
	return RecomputeUpcomingArgs{TenantID: e.TenantID}, nil
}

type recomputeWorker struct {
	river.WorkerDefaults[RecomputeBatchArgs]
	deps Deps
}

// Work computes the batch and every later one: stock is allocated by date
// (§11). A broken recipe is not retried: nothing heals
// until someone fixes the recipe, which computes the batch again itself. It
// alerts instead, and the batch shows the reason (§11).
func (w *recomputeWorker) Work(ctx context.Context, job *river.Job[RecomputeBatchArgs]) error {
	date, err := clock.ParseDate(job.Args.Date)
	if err != nil {
		return river.JobCancel(fmt.Errorf("bad date %q: %w", job.Args.Date, err))
	}
	_, err = w.deps.Engine.RecomputeFrom(ctx, job.Args.TenantID, date)
	return cancelIfRecipe(ctx, w.deps.Logger, err)
}

type upcomingWorker struct {
	river.WorkerDefaults[RecomputeUpcomingArgs]
	deps Deps
}

func (w *upcomingWorker) Work(ctx context.Context, job *river.Job[RecomputeUpcomingArgs]) error {
	n, err := w.deps.Engine.RecomputeUpcoming(ctx, job.Args.TenantID)
	if n > 0 {
		w.deps.Logger.InfoContext(ctx, "upcoming batches computed again", slog.Int("batches", n))
	}
	return cancelIfRecipe(ctx, w.deps.Logger, err)
}

// cancelIfRecipe turns a broken recipe into an alert and a job that is not
// retried; any other failure is retried. River's error handler does not see
// a cancelled job, so the alert is logged here.
func cancelIfRecipe(ctx context.Context, logger *slog.Logger, err error) error {
	var re *aggregation.RecipeError
	if err == nil || !errors.As(err, &re) {
		return err
	}
	logger.ErrorContext(ctx, "batch cannot be computed: broken recipe",
		slog.String("component_id", re.ComponentID.String()), slog.String("ingredient_id", re.IngredientID.String()), slog.Any("error", err))
	return river.JobCancel(err)
}
