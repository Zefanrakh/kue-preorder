package aggregation

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/inventory"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/audit"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
)

// Consumer takes what a finished batch used out of the stock;
// inventory.Consumer implements it.
type Consumer interface {
	Consume(ctx context.Context, tenant, batchID uuid.UUID, uses []inventory.Use, actorID uuid.UUID, at time.Time) ([]inventory.Used, error)
}

// Errors of closing a day's production, in words the PWA shows as they are.
var (
	ErrBatchDone = &apperr.PreconditionError{
		Reason:  "batch_done",
		Message: "Produksi hari itu sudah ditandai selesai.",
	}
	ErrNotProductionDay = &apperr.PreconditionError{
		Reason:  "not_production_day",
		Message: "Produksi baru bisa ditandai selesai pada hari produksinya.",
	}
)

// LockDue locks the open batches whose shopping cutoff has passed (§15):
// their shopping list is final for the kitchen. Checkout already refuses
// orders for them. A batch without orders counting for production has no
// cutoff and stays open. It returns how many it locked.
func (e *Engine) LockDue(ctx context.Context, tenant uuid.UUID) (int, error) {
	dates, err := e.repo.OpenDates(ctx, tenant)
	if err != nil || len(dates) == 0 {
		return 0, err
	}
	cutoffs, err := e.orders.BatchCutoffs(ctx, tenant, dates[0], dates[len(dates)-1])
	if err != nil {
		return 0, err
	}
	now := e.clock.Now()
	n := 0
	for _, d := range dates {
		cutoff, ok := cutoffs[d]
		if !ok || cutoff.After(now) {
			continue
		}
		locked, err := e.repo.SetStatus(ctx, tenant, d, []BatchStatus{BatchOpen}, BatchLocked, now)
		if err != nil {
			return n, fmt.Errorf("lock batch %s: %w", d, err)
		}
		if locked {
			n++
		}
	}
	return n, nil
}

// MarkInProduction marks the batch of date in production, once its first
// order is (decided 2026-09-29: no separate button). A batch already in
// production or done stays as it is.
func (e *Engine) MarkInProduction(ctx context.Context, tenant uuid.UUID, date clock.Date) error {
	_, err := e.repo.SetStatus(ctx, tenant, date, []BatchStatus{BatchOpen, BatchLocked}, BatchInProduction, e.clock.Now())
	return err
}

// Completion is a day's production closed: the batch, and what it took out
// of the stock.
type Completion struct {
	View
	Used []UsedView
}

// UsedView is what the batch took of one ingredient, with its name.
type UsedView struct {
	inventory.Used
	IngredientName string
}

// completedReason is the audit reason of "Produksi selesai".
const completedReason = "Produksi selesai"

// Complete closes the production of date ("Produksi selesai", §12): what
// the batch's recipes need of each ingredient comes out of the stock,
// oldest lot first; the batch is done, so it counts on no stock any more
// and the later batches are computed again with what it left. The stock
// never goes below zero: what the ledger lacks is reported as missing, for
// a stock count to put right. It runs in one transaction with the batch
// locked, only on or after the production day, and only once; a mistake is
// put right with a stock count, not undone. Staff only.
func (s *Service) Complete(ctx context.Context, date clock.Date) (Completion, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return Completion{}, err
	}
	now := s.clock.Now()
	if clock.DateOf(now).Before(date) {
		return Completion{}, ErrNotProductionDay
	}
	var used []inventory.Used
	err = s.tx.Tx(ctx, func(ctx context.Context) error {
		b, err := s.repo.LockBatch(ctx, p.TenantID, date)
		if err != nil {
			return err
		}
		if b.Status == BatchDone {
			return ErrBatchDone
		}
		lines, err := s.repo.Lines(ctx, p.TenantID, b.ID)
		if err != nil {
			return err
		}
		var uses []inventory.Use
		for _, l := range lines {
			if l.Needed > 0 {
				uses = append(uses, inventory.Use{IngredientID: l.IngredientID, Qty: l.Needed})
			}
		}
		if used, err = s.stock.Consume(ctx, p.TenantID, b.ID, uses, p.AuthUserID, now); err != nil {
			return err
		}
		all := []BatchStatus{BatchOpen, BatchLocked, BatchInProduction}
		if _, err := s.repo.SetStatus(ctx, p.TenantID, date, all, BatchDone, now); err != nil {
			return err
		}
		var consumed, missing int64
		for _, u := range used {
			consumed += u.Consumed
			missing += u.Missing
		}
		if err := s.repo.Audit(ctx, audit.Entry{
			TenantID: p.TenantID, ActorID: p.AuthUserID, Action: "aggregation.batch.completed", Entity: "production_batch", EntityID: b.ID,
			Before: map[string]any{"status": b.Status},
			After:  map[string]any{"status": BatchDone, "ingredients": len(used), "consumed": consumed, "missing": missing},
			Reason: completedReason, At: now,
		}); err != nil {
			return err
		}
		return s.repo.Publish(ctx, outbox.Event{
			TenantID: p.TenantID, Aggregate: "batch", Type: "batch.completed", At: now,
			Payload: map[string]any{"batch_id": b.ID, "date": date.String()},
		})
	})
	if err != nil {
		return Completion{}, err
	}
	v, err := s.view(ctx, p.TenantID, date)
	if err != nil {
		return Completion{}, err
	}
	labels, err := s.catalog.Labels(ctx, p.TenantID)
	if err != nil {
		return Completion{}, err
	}
	c := Completion{View: v}
	for _, u := range used {
		c.Used = append(c.Used, UsedView{Used: u, IngredientName: labels.Ingredients[u.IngredientID].Name})
	}
	slices.SortFunc(c.Used, func(a, b UsedView) int { return cmp.Compare(a.IngredientName, b.IngredientName) })
	return c, nil
}
