package aggregation

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

// Procured is a change procurement made to one line: what was ordered and
// what arrived, as deltas. Ordering 2 kg is {Ordered: 2000}; receiving
// 1.8 kg of them is {Ordered: -2000, Received: 1800}: the order is closed,
// the flour is in the stock, and the 200 g short is to buy again.
type Procured struct {
	IngredientID uuid.UUID
	Ordered      int64
	Received     int64
}

// Purchasing is procurement's way into the shopping lists (M4, §19). Both
// methods work in the caller's transaction.
type Purchasing struct {
	engine *Engine
}

// NewPurchasing returns a Purchasing over engine.
func NewPurchasing(engine *Engine) *Purchasing {
	return &Purchasing{engine: engine}
}

// LockShoppingList returns the batch of date and its lines, holding the
// batch's row until the transaction ends, so two orders made at once take
// turns and never order the same line twice. apperr.ErrNotFound when no
// order was ever confirmed for date.
func (p *Purchasing) LockShoppingList(ctx context.Context, tenant uuid.UUID, date clock.Date) (Batch, []Line, error) {
	b, err := p.engine.repo.LockBatch(ctx, tenant, date)
	if err != nil {
		return Batch{}, nil, err
	}
	lines, err := p.engine.repo.Lines(ctx, tenant, b.ID)
	return b, lines, err
}

// Record applies procurement's changes to the batch of date and computes it
// again, so what to buy follows at once: an order lowers it, a delivery
// short of the order raises it again. The later batches follow the stock
// change inventory publishes.
func (p *Purchasing) Record(ctx context.Context, tenant uuid.UUID, date clock.Date, changes []Procured) error {
	b, err := p.engine.repo.LockBatch(ctx, tenant, date)
	if err != nil {
		return err
	}
	now := p.engine.clock.Now()
	for _, c := range changes {
		if err := p.engine.repo.AddProcured(ctx, tenant, b.ID, c.IngredientID, c.Ordered, c.Received, now); err != nil {
			return fmt.Errorf("record procurement of %s on %s: %w", c.IngredientID, date, err)
		}
	}
	return p.engine.Recompute(ctx, tenant, date)
}
