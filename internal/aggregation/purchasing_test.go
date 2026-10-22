package aggregation_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/aggregation"
	"github.com/Zefanrakh/kue-preorder/internal/orders"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
)

// §19's lines: needed → ordered → received, and back to needed when an
// order is cancelled. Nothing counts twice: once flour arrives it is stock,
// not an order.
func TestPurchasing(t *testing.T) {
	b := newBakery()
	b.kitchen.items[day(7)] = []orders.CommittedItem{{VariantID: chocolate, Quantity: 6}, {VariantID: cheese, Quantity: 6}}
	b.recompute(t, day(7))
	need := lineOf(b.linesOf(day(7)), flour).Needed
	buying := aggregation.NewPurchasing(b.engine)
	record := func(changes ...aggregation.Procured) {
		t.Helper()
		if err := buying.Record(t.Context(), tenant, day(7), changes); err != nil {
			t.Fatal(err)
		}
	}

	record(aggregation.Procured{IngredientID: flour, Ordered: 1000})
	if l := lineOf(b.linesOf(day(7)), flour); l.Status != aggregation.LineOrdered || l.Ordered != 1000 || l.ToBuy != 0 {
		t.Errorf("ordered: %+v, want nothing more to buy", l)
	}

	b.shelf[flour] = 900 // 900 g of the 1,000 arrived
	record(aggregation.Procured{IngredientID: flour, Ordered: -1000, Received: 900})
	l := lineOf(b.linesOf(day(7)), flour)
	if l.Status != aggregation.LineReceived || l.Ordered != 0 || l.Received != 900 || l.UsableStock != 900 || l.ToBuy != need-900 {
		t.Errorf("received short: %+v, want the 900 g counted once, as stock, and %d g to buy", l, need-900)
	}

	record(aggregation.Procured{IngredientID: egg, Ordered: 8})
	record(aggregation.Procured{IngredientID: egg, Ordered: -8}) // cancelled
	if e := lineOf(b.linesOf(day(7)), egg); e.Status != aggregation.LineNeeded || e.Ordered != 0 || e.ToBuy != e.Needed {
		t.Errorf("cancelled: %+v, want needed again", e)
	}

	batch, lines, err := buying.LockShoppingList(t.Context(), tenant, day(7))
	if err != nil || batch.Date != day(7) || len(lines) != 4 {
		t.Errorf("LockShoppingList() = %+v, %d lines, %v", batch, len(lines), err)
	}
	if err := buying.Record(t.Context(), tenant, day(7), []aggregation.Procured{{IngredientID: uuid.New(), Ordered: 1}}); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("an ingredient not on the list: error = %v, want ErrNotFound", err)
	}
	if _, _, err := buying.LockShoppingList(t.Context(), tenant, day(9)); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("a day without a batch: error = %v, want ErrNotFound", err)
	}
}
