//go:build integration

package main

import (
	"testing"
	"time"

	"connectrpc.com/connect"

	aggregationv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/aggregation/v1"
	inventoryv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/inventory/v1"
	ordersv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/orders/v1"
)

// Thursday's 20 donuts use 1 kg of flour from 1.3 kg in stock, oldest
// delivery first; Friday's batch then counts on what is left, and no gram
// counts twice at any point.
func TestProduction_FinishedBatchUsesTheStock(t *testing.T) {
	w := newWired(t)
	ctx := t.Context()
	_, flour := w.withRecipe(t)
	owner, _ := w.staff(t, "owner")
	for _, day := range []int32{8, 9} {
		placed, _ := w.placed(t, day) // 20 donuts each: 1,000 g of flour
		_, err := owner.RecordManualPayment(ctx, connect.NewRequest(&ordersv1.RecordManualPaymentRequest{
			Code: placed.GetCode(), AmountIdr: 80000, Reference: "BCA 1", Note: "DP",
		}))
		noErr(t, err)
	}
	pantry := w.inventory(t, "kitchen")
	for _, qty := range []int64{300, 1000} {
		_, err := pantry.ReceiveStock(ctx, connect.NewRequest(&inventoryv1.ReceiveStockRequest{IngredientId: flour.String(), Qty: qty}))
		noErr(t, err)
		w.clock.Advance(time.Minute) // deliveries keep their order; Postgres keeps microseconds
	}
	batches := w.batches(t, "kitchen")
	recompute := func(date string) *aggregationv1.ShoppingLine {
		t.Helper()
		res, err := batches.RecomputeBatch(ctx, connect.NewRequest(&aggregationv1.RecomputeBatchRequest{Date: date}))
		noErr(t, err)
		return res.Msg.GetBatch().GetLines()[0]
	}
	if th, fr := recompute("2026-10-08"), recompute("2026-10-09"); th.GetUsableStock() != 1000 || fr.GetUsableStock() != 300 || fr.GetToBuy() != 700 {
		t.Fatalf("before production: Thursday %v, Friday %v; want 1,000 and the 300 left", th, fr)
	}
	if _, err := batches.CompleteBatch(ctx, connect.NewRequest(&aggregationv1.CompleteBatchRequest{Date: "2026-10-08"})); precondition(t, err) != "not_production_day" {
		t.Fatal("a batch was completed before its day")
	}

	w.clock.Set(wib(8, 14, 0))
	kitchen := w.batches(t, "kitchen") // a token for the new day
	res, err := kitchen.CompleteBatch(ctx, connect.NewRequest(&aggregationv1.CompleteBatchRequest{Date: "2026-10-08"}))

	noErr(t, err)
	if b := res.Msg.GetBatch().GetBatch(); b.GetStatus() != aggregationv1.BatchStatus_BATCH_STATUS_DONE {
		t.Errorf("batch = %v, want done", b)
	}
	if u := res.Msg.GetUses(); len(u) != 1 || u[0].GetIngredientName() != "Tepung" || u[0].GetConsumed() != 1000 || u[0].GetMissing() != 0 {
		t.Errorf("uses = %v, want 1,000 g of flour taken, nothing missing", u)
	}
	stock, err := w.inventory(t, "kitchen").ListStock(ctx, connect.NewRequest(&inventoryv1.ListStockRequest{}))
	noErr(t, err)
	st := stock.Msg.GetIngredients()[0]
	if st.GetTotal() != 300 || len(st.GetLots()) != 1 || st.GetLots()[0].GetBalance() != 300 {
		t.Errorf("flour = %v, want the older 300 g gone first and 300 g of the newer delivery left", st)
	}
	friday, err := kitchen.RecomputeBatch(ctx, connect.NewRequest(&aggregationv1.RecomputeBatchRequest{Date: "2026-10-09"}))
	noErr(t, err)
	if l := friday.Msg.GetBatch().GetLines()[0]; l.GetUsableStock() != 300 || l.GetToBuy() != 700 {
		t.Errorf("Friday after Thursday's production: %v, want the 300 g left", l)
	}
	if _, err := kitchen.CompleteBatch(ctx, connect.NewRequest(&aggregationv1.CompleteBatchRequest{Date: "2026-10-08"})); precondition(t, err) != "batch_done" {
		t.Error("a batch was completed twice")
	}
	var consumed int64
	noErr(t, w.d.Pool().QueryRow(ctx, "select coalesce(sum(qty), 0) from stock_movements where kind = 'consume' and batch_id is not null").Scan(&consumed))
	if consumed != -1000 {
		t.Errorf("consumed %d in the ledger, want -1,000 for the batch", consumed)
	}
}
