// Package worker holds the inventory module's background jobs
// (docs/architecture.md §21).
package worker

import (
	"context"
	"log/slog"

	"github.com/riverqueue/river"

	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/inventory"
	"github.com/Zefanrakh/kue-preorder/internal/platform/jobs"
)

// ExpireLotsArgs asks for the lots past their expiry to be marked expired.
type ExpireLotsArgs struct{}

// Kind implements river.JobArgs.
func (ExpireLotsArgs) Kind() string { return "expire-lots" }

// Deps are what the workers work with.
type Deps struct {
	Stock   *inventory.Reader
	Tenants identity.TenantResolver
	Logger  *slog.Logger
}

// Register adds the inventory module's workers.
func Register(workers *river.Workers, d Deps) {
	river.AddWorker(workers, &expireWorker{deps: d})
}

// Periodic lists the daily jobs: lots are marked expired at 00.05 WIB, and
// at start, so a worker that was down catches up. Marking changes no
// shopping list: an expired lot never counted for a batch after its expiry
// anyway (§12); it now shows in the kitchen's list to throw away.
func Periodic() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(jobs.DailyAt(0, 5), func() (river.JobArgs, *river.InsertOpts) {
			return ExpireLotsArgs{}, &river.InsertOpts{MaxAttempts: 3}
		}, &river.PeriodicJobOpts{RunOnStart: true}),
	}
}

type expireWorker struct {
	river.WorkerDefaults[ExpireLotsArgs]
	deps Deps
}

func (w *expireWorker) Work(ctx context.Context, _ *river.Job[ExpireLotsArgs]) error {
	n, err := w.deps.Stock.ExpireLots(ctx, w.deps.Tenants.TenantID(ctx))
	if n > 0 {
		w.deps.Logger.InfoContext(ctx, "lots expired", slog.Int64("lots", n))
	}
	return err
}
