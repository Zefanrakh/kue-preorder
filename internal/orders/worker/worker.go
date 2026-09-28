// Package worker holds the orders module's background jobs
// (docs/architecture.md §21): thin River workers over orders.Automation,
// when they run, and the outbox events that start them.
package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/orders"
	"github.com/Zefanrakh/kue-preorder/internal/platform/jobs"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
)

// SweepEvery is how often the worker looks for orders past a deadline.
const SweepEvery = time.Minute

// balanceInvoiceAttempts spreads retries of a failing provider over about
// seven hours; the balance is due hours to days after the DP.
const balanceInvoiceAttempts = 10

// ExpireUnpaidDPArgs asks for a sweep of orders whose DP did not arrive.
type ExpireUnpaidDPArgs struct{}

// Kind implements river.JobArgs.
func (ExpireUnpaidDPArgs) Kind() string { return "expire-unpaid-dp" }

// ForfeitUnpaidArgs asks for a sweep of orders whose balance did not arrive.
type ForfeitUnpaidArgs struct{}

// Kind implements river.JobArgs.
func (ForfeitUnpaidArgs) Kind() string { return "forfeit-unpaid" }

// BalanceInvoiceArgs asks for the balance invoice of one order.
type BalanceInvoiceArgs struct {
	TenantID uuid.UUID `json:"tenant_id"`
	OrderID  uuid.UUID `json:"order_id"`
}

// Kind implements river.JobArgs.
func (BalanceInvoiceArgs) Kind() string { return "create-balance-invoice" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (BalanceInvoiceArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: balanceInvoiceAttempts}
}

// Deps are what the workers work with.
type Deps struct {
	Automation *orders.Automation
	// Tenants picks the tenant a sweep works on.
	Tenants identity.TenantResolver
	Logger  *slog.Logger
}

// Register adds the orders module's workers.
func Register(workers *river.Workers, d Deps) {
	river.AddWorker(workers, &expireWorker{deps: d})
	river.AddWorker(workers, &forfeitWorker{deps: d})
	river.AddWorker(workers, &balanceInvoiceWorker{deps: d})
}

// Periodic lists the sweeps. Each runs once a minute and at start; a
// failed sweep is not retried, since the next one comes a minute later, and
// a sweep still waiting to run is not queued twice.
func Periodic() []*river.PeriodicJob {
	opts := &river.InsertOpts{MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByPeriod: SweepEvery}}
	every := river.PeriodicInterval(SweepEvery)
	return []*river.PeriodicJob{
		river.NewPeriodicJob(every, func() (river.JobArgs, *river.InsertOpts) { return ExpireUnpaidDPArgs{}, opts }, &river.PeriodicJobOpts{RunOnStart: true}),
		river.NewPeriodicJob(every, func() (river.JobArgs, *river.InsertOpts) { return ForfeitUnpaidArgs{}, opts }, &river.PeriodicJobOpts{RunOnStart: true}),
	}
}

// Subscriptions are the outbox events that start the orders module's jobs:
// a confirmed order, and money recorded for one, get their balance billed.
func Subscriptions() map[string][]jobs.Subscriber {
	return map[string][]jobs.Subscriber{
		"order.confirmed":        {balanceInvoice},
		"order.payment_received": {balanceInvoice},
	}
}

func balanceInvoice(e outbox.Stored) (river.JobArgs, error) {
	var p struct {
		OrderID uuid.UUID `json:"order_id"`
	}
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return nil, fmt.Errorf("read event %d: %w", e.Seq, err)
	}
	if p.OrderID == uuid.Nil {
		return nil, fmt.Errorf("event %d has no order_id", e.Seq)
	}
	return BalanceInvoiceArgs{TenantID: e.TenantID, OrderID: p.OrderID}, nil
}

type expireWorker struct {
	river.WorkerDefaults[ExpireUnpaidDPArgs]
	deps Deps
}

func (w *expireWorker) Work(ctx context.Context, _ *river.Job[ExpireUnpaidDPArgs]) error {
	n, err := w.deps.Automation.ExpireUnpaid(ctx, w.deps.Tenants.TenantID(ctx))
	if n > 0 {
		w.deps.Logger.InfoContext(ctx, "orders expired unpaid", slog.Int("orders", n))
	}
	return err
}

type forfeitWorker struct {
	river.WorkerDefaults[ForfeitUnpaidArgs]
	deps Deps
}

func (w *forfeitWorker) Work(ctx context.Context, _ *river.Job[ForfeitUnpaidArgs]) error {
	n, err := w.deps.Automation.ForfeitUnpaid(ctx, w.deps.Tenants.TenantID(ctx))
	if n > 0 {
		w.deps.Logger.InfoContext(ctx, "orders cancelled with the DP forfeited", slog.Int("orders", n))
	}
	return err
}

type balanceInvoiceWorker struct {
	river.WorkerDefaults[BalanceInvoiceArgs]
	deps Deps
}

func (w *balanceInvoiceWorker) Work(ctx context.Context, job *river.Job[BalanceInvoiceArgs]) error {
	return w.deps.Automation.EnsureBalanceInvoice(ctx, job.Args.TenantID, job.Args.OrderID)
}
