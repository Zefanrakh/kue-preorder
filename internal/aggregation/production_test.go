package aggregation_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/aggregation"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/orders"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

func wib(d, hour, minute int) time.Time {
	return time.Date(2026, time.October, d, hour, minute, 0, 0, clock.Jakarta)
}

func reason(err error) string {
	var p *apperr.PreconditionError
	if errors.As(err, &p) {
		return p.Reason
	}
	return ""
}

// A batch locks at its shopping cutoff, not a minute sooner, and only once;
// a batch without orders has no cutoff and stays open.
func TestEngine_LockDue(t *testing.T) {
	b := newBakery()
	for _, d := range []int{7, 8, 9} {
		b.kitchen.items[day(d)] = []orders.CommittedItem{{VariantID: chocolate, Quantity: 1}}
		b.recompute(t, day(d))
	}
	b.kitchen.cutoffs[day(7)] = wib(6, 19, 30)
	b.kitchen.cutoffs[day(8)] = wib(7, 19, 30)
	// The 9th's orders were all cancelled: no cutoff.

	b.clock.Set(wib(6, 19, 29))
	if n, err := b.engine.LockDue(t.Context(), tenant); err != nil || n != 0 {
		t.Fatalf("a minute early: LockDue() = %d, %v; want nothing locked", n, err)
	}
	b.clock.Set(wib(6, 19, 30))
	if n, err := b.engine.LockDue(t.Context(), tenant); err != nil || n != 1 {
		t.Fatalf("at the cutoff: LockDue() = %d, %v; want the 7th locked", n, err)
	}
	if n, _ := b.engine.LockDue(t.Context(), tenant); n != 0 {
		t.Errorf("second sweep locked %d, want 0", n)
	}
	b.clock.Set(wib(9, 12, 0))
	if n, _ := b.engine.LockDue(t.Context(), tenant); n != 1 {
		t.Errorf("later: locked %d, want only the 8th", n)
	}
	for d, want := range map[int]aggregation.BatchStatus{7: aggregation.BatchLocked, 8: aggregation.BatchLocked, 9: aggregation.BatchOpen} {
		if got := b.repo.batches[day(d)].Status; got != want {
			t.Errorf("batch of the %dth = %s, want %s", d, got, want)
		}
	}
}

// The first order in production puts its batch in production; a done batch
// stays done.
func TestEngine_MarkInProduction(t *testing.T) {
	b := newBakery()
	b.kitchen.items[day(7)] = []orders.CommittedItem{{VariantID: chocolate, Quantity: 1}}
	b.recompute(t, day(7))

	for range 2 {
		if err := b.engine.MarkInProduction(t.Context(), tenant, day(7)); err != nil {
			t.Fatal(err)
		}
	}
	if s := b.repo.batches[day(7)].Status; s != aggregation.BatchInProduction {
		t.Errorf("status = %s, want in_production", s)
	}
	b.repo.batches[day(7)].Status = aggregation.BatchDone
	if err := b.engine.MarkInProduction(t.Context(), tenant, day(7)); err != nil || b.repo.batches[day(7)].Status != aggregation.BatchDone {
		t.Errorf("a done batch became %s, %v", b.repo.batches[day(7)].Status, err)
	}
}

// "Produksi selesai" takes what the recipes need out of the stock, marks
// the batch done, and reports what the ledger lacked.
func TestService_Complete(t *testing.T) {
	b := newBakery(identity.RoleKitchen)
	b.kitchen.items[day(7)] = []orders.CommittedItem{{VariantID: chocolate, Quantity: 6}, {VariantID: cheese, Quantity: 6}}
	b.recompute(t, day(7))
	needed := map[string]int64{}
	for _, l := range b.linesOf(day(7)) {
		needed[l.IngredientID.String()] = l.Needed
	}
	b.pantry.available = map[uuid.UUID]int64{egg: 5}
	b.clock.Set(wib(7, 14, 0))

	c, err := b.service.Complete(t.Context(), day(7))

	if err != nil {
		t.Fatal(err)
	}
	if c.Status != aggregation.BatchDone || b.repo.batches[day(7)].Status != aggregation.BatchDone {
		t.Errorf("status = %s, want done", c.Status)
	}
	if len(b.pantry.uses) != 4 {
		t.Fatalf("consumed %+v, want the 4 ingredients", b.pantry.uses)
	}
	for _, u := range b.pantry.uses {
		if u.Qty != needed[u.IngredientID.String()] {
			t.Errorf("consumed %d of %s, want what the recipes need, %d", u.Qty, u.IngredientID, needed[u.IngredientID.String()])
		}
	}
	var eggs aggregation.UsedView
	for _, u := range c.Used {
		if u.IngredientID == egg {
			eggs = u
		}
	}
	if eggs.IngredientName != "Telur" || eggs.Consumed != 5 || eggs.Missing != 3 {
		t.Errorf("eggs = %+v, want 5 taken and 3 missing from the ledger", eggs)
	}
	if a := b.repo.audits; len(a) != 1 || a[0].Action != "aggregation.batch.completed" || a[0].Reason != "Produksi selesai" {
		t.Errorf("audits = %+v", a)
	}
	if e := b.repo.events; len(e) != 1 || e[0].Type != "batch.completed" {
		t.Errorf("events = %+v", e)
	}

	if _, err := b.service.Complete(t.Context(), day(7)); reason(err) != "batch_done" || b.pantry.calls != 1 {
		t.Errorf("second Complete() error = %v after %d consumptions, want batch_done and one", err, b.pantry.calls)
	}
}

func TestService_CompleteRefuses(t *testing.T) {
	b := newBakery(identity.RoleKitchen)
	b.kitchen.items[day(7)] = []orders.CommittedItem{{VariantID: chocolate, Quantity: 6}}
	b.recompute(t, day(7))

	b.clock.Set(wib(6, 23, 59))
	if _, err := b.service.Complete(t.Context(), day(7)); reason(err) != "not_production_day" {
		t.Errorf("the day before: error = %v, want not_production_day", err)
	}
	b.clock.Set(wib(8, 12, 0))
	if _, err := b.service.Complete(t.Context(), day(8)); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("a day without a batch: error = %v, want ErrNotFound", err)
	}
	if b.pantry.calls != 0 {
		t.Error("a refusal consumed stock")
	}

	customer := newBakery()
	customer.kitchen.items[day(7)] = []orders.CommittedItem{{VariantID: chocolate, Quantity: 6}}
	customer.recompute(t, day(7))
	customer.clock.Set(wib(7, 12, 0))
	if _, err := customer.service.Complete(t.Context(), day(7)); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("customer Complete() error = %v, want ErrForbidden", err)
	}
}
