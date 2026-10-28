//go:build integration

package main

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	aggregationv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/aggregation/v1"
	inventoryv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/inventory/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/inventory/v1/inventoryv1connect"
	ordersv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/orders/v1"
	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	catalogpg "github.com/Zefanrakh/kue-preorder/internal/catalog/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
)

func (w *wired) inventory(t *testing.T, role string) inventoryv1connect.InventoryServiceClient {
	t.Helper()
	_, user := w.staff(t, role)
	return inventoryv1connect.NewInventoryServiceClient(w.http, w.url, w.bearer(t, user, ""))
}

// flourLine returns the flour line of Thursday's batch, computed now.
func (w *wired) flourLine(t *testing.T) *aggregationv1.ShoppingLine {
	t.Helper()
	res, err := w.batches(t, "kitchen").RecomputeBatch(t.Context(), connect.NewRequest(&aggregationv1.RecomputeBatchRequest{Date: "2026-10-08"}))
	noErr(t, err)
	for _, l := range res.Msg.GetBatch().GetLines() {
		if l.GetIngredientName() == "Tepung" {
			return l
		}
	}
	t.Fatal("no flour on the shopping list")
	return nil
}

// Flour received lowers Thursday's shopping list; a stock count lowers what
// it may count on again.
func TestInventory_StockReachesTheShoppingList(t *testing.T) {
	w := newWired(t)
	ctx := t.Context()
	_, flour := w.withRecipe(t)
	placed, _ := w.placed(t, 8) // 20 donuts: 1,000 g of flour
	owner, _ := w.staff(t, "owner")
	_, err := owner.RecordManualPayment(ctx, connect.NewRequest(&ordersv1.RecordManualPaymentRequest{
		Code: placed.GetCode(), AmountIdr: 80000, Reference: "BCA 1", Note: "DP",
	}))
	noErr(t, err)
	if l := w.flourLine(t); l.GetToBuy() != 1000 || l.GetUsableStock() != 0 {
		t.Fatalf("before any stock: %v, want 1,000 g to buy", l)
	}
	kitchen := w.inventory(t, "kitchen")

	lot, err := kitchen.ReceiveStock(ctx, connect.NewRequest(&inventoryv1.ReceiveStockRequest{IdempotencyKey: uuid.NewString(), IngredientId: flour.String(), Qty: 600, Note: "Toko Sinar"}))

	noErr(t, err)
	if l := lot.Msg.GetLot(); l.GetBalance() != 600 || l.GetStatus() != inventoryv1.LotStatus_LOT_STATUS_AVAILABLE || l.GetSource() != inventoryv1.LotSource_LOT_SOURCE_MANUAL {
		t.Errorf("lot = %v", l)
	}
	if l := w.flourLine(t); l.GetUsableStock() != 600 || l.GetToBuy() != 400 || l.GetPacks() != 1 {
		t.Errorf("after 600 g in: %v, want 600 from stock and 400 g to buy in 1 pack", l)
	}

	counted, err := kitchen.CountStock(ctx, connect.NewRequest(&inventoryv1.CountStockRequest{IdempotencyKey: uuid.NewString(), IngredientId: flour.String(), ActualQty: 450, Reason: "Tumpah sedikit"}))
	noErr(t, err)
	if st := counted.Msg.GetIngredient(); st.GetTotal() != 450 || st.GetUsableToday() != 450 || len(st.GetLots()) != 1 {
		t.Errorf("after the count: %v, want 450 g", st)
	}
	if l := w.flourLine(t); l.GetUsableStock() != 450 || l.GetToBuy() != 550 {
		t.Errorf("after the count: %v, want 450 from stock and 550 to buy", l)
	}

	list, err := kitchen.ListStock(ctx, connect.NewRequest(&inventoryv1.ListStockRequest{}))
	noErr(t, err)
	if st := list.Msg.GetIngredients(); len(st) != 1 || st[0].GetName() != "Tepung" || st[0].GetTotal() != 450 {
		t.Errorf("ListStock() = %v", st)
	}
	var audits int
	noErr(t, w.d.Pool().QueryRow(ctx, "select count(*) from audit_log where action in ('inventory.stock.received', 'inventory.stock.counted')").Scan(&audits))
	if audits != 2 {
		t.Errorf("%d audit entries, want the delivery and the count", audits)
	}
}

// The offline queue may send a delivery twice at once (#45): one lot, one
// movement, one audit; both calls get that lot. The same key with another
// quantity is refused.
func TestInventory_RetriesAtOnceMakeOneLot(t *testing.T) {
	w := newWired(t)
	ctx := t.Context()
	_, flour := w.withRecipe(t)
	kitchen := w.inventory(t, "kitchen")
	req := &inventoryv1.ReceiveStockRequest{IdempotencyKey: uuid.NewString(), IngredientId: flour.String(), Qty: 600}

	const calls = 4
	lots := make(chan string, calls)
	errs := make(chan error, calls)
	var wg sync.WaitGroup
	for range calls {
		wg.Go(func() {
			res, err := kitchen.ReceiveStock(ctx, connect.NewRequest(req))
			if err != nil {
				errs <- err
				return
			}
			lots <- res.Msg.GetLot().GetId()
		})
	}
	wg.Wait()
	close(lots)
	close(errs)

	for err := range errs {
		t.Errorf("ReceiveStock() error = %v", err)
	}
	ids := map[string]bool{}
	for id := range lots {
		ids[id] = true
	}
	if len(ids) != 1 {
		t.Errorf("calls got lots %v, want one lot for all", ids)
	}
	var lotCount, movements, audits int
	noErr(t, w.d.Pool().QueryRow(ctx, `select (select count(*) from stock_lots), (select count(*) from stock_movements),
		(select count(*) from audit_log where action = 'inventory.stock.received')`).Scan(&lotCount, &movements, &audits))
	if lotCount != 1 || movements != 1 || audits != 1 {
		t.Errorf("%d lots, %d movements, %d audits; want one of each", lotCount, movements, audits)
	}

	req.Qty = 700
	if _, err := kitchen.ReceiveStock(ctx, connect.NewRequest(req)); precondition(t, err) != "idempotency_key_reused" {
		t.Errorf("same key, other qty: error = %v", err)
	}
	if _, err := kitchen.ReceiveStock(ctx, connect.NewRequest(&inventoryv1.ReceiveStockRequest{IngredientId: flour.String(), Qty: 1})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("no key: code = %v, want InvalidArgument", connect.CodeOf(err))
	}
}

// A count sent twice at once is applied once (#45).
func TestInventory_CountsAtOnceApplyOnce(t *testing.T) {
	w := newWired(t)
	ctx := t.Context()
	_, flour := w.withRecipe(t)
	kitchen := w.inventory(t, "kitchen")
	_, err := kitchen.ReceiveStock(ctx, connect.NewRequest(&inventoryv1.ReceiveStockRequest{IdempotencyKey: uuid.NewString(), IngredientId: flour.String(), Qty: 1000}))
	noErr(t, err)
	req := &inventoryv1.CountStockRequest{IdempotencyKey: uuid.NewString(), IngredientId: flour.String(), ActualQty: 400, Reason: "Opname"}

	const calls = 4
	errs := make(chan error, calls)
	var wg sync.WaitGroup
	for range calls {
		wg.Go(func() {
			res, err := kitchen.CountStock(ctx, connect.NewRequest(req))
			if err == nil && res.Msg.GetIngredient().GetTotal() != 400 {
				err = fmt.Errorf("total = %d, want 400", res.Msg.GetIngredient().GetTotal())
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("CountStock(): %v", err)
		}
	}
	var adjusts, audits int
	noErr(t, w.d.Pool().QueryRow(ctx, `select (select count(*) from stock_movements where kind = 'adjust'),
		(select count(*) from audit_log where action = 'inventory.stock.counted')`).Scan(&adjusts, &audits))
	if adjusts != 1 || audits != 1 {
		t.Errorf("%d adjust movements, %d audits; want one each", adjusts, audits)
	}
}

// Fresh eggs count at once; a day later they need a check; thrown away,
// nothing is left.
func TestInventory_CheckingPerishables(t *testing.T) {
	w := newWired(t)
	ctx := t.Context()
	shelf := int32(14)
	egg, err := catalogpg.NewRepository(w.d).CreateIngredient(ctx, dbtest.DefaultTenantID, catalog.IngredientInput{
		Name: "Telur", BaseUnit: catalog.Piece, Perishable: true, ShelfLifeDays: &shelf, LeftoverPolicy: catalog.LeftoverConfirm,
	}, wib(5, 9, 0))
	noErr(t, err)
	kitchen := w.inventory(t, "kitchen")
	lot, err := kitchen.ReceiveStock(ctx, connect.NewRequest(&inventoryv1.ReceiveStockRequest{IdempotencyKey: uuid.NewString(), IngredientId: egg.ID.String(), Qty: 30}))
	noErr(t, err)
	lotID := lot.Msg.GetLot().GetId()
	if lot.Msg.GetLot().GetExpiresAt() == nil {
		t.Error("eggs have no expiry despite a 14-day shelf life")
	}

	fresh, err := kitchen.ListLotsToCheck(ctx, connect.NewRequest(&inventoryv1.ListLotsToCheckRequest{}))
	noErr(t, err)
	if ls := fresh.Msg.GetLots(); len(ls) != 0 {
		t.Fatalf("ListLotsToCheck() = %v, want nothing: the eggs are fresh from the shop", ls)
	}

	w.clock.Advance(25 * time.Hour)
	kitchen = w.inventory(t, "kitchen") // a token for the new day
	toCheck, err := kitchen.ListLotsToCheck(ctx, connect.NewRequest(&inventoryv1.ListLotsToCheckRequest{}))
	noErr(t, err)
	if ls := toCheck.Msg.GetLots(); len(ls) != 1 || ls[0].GetLot().GetId() != lotID || ls[0].GetIngredientName() != "Telur" {
		t.Fatalf("a day later: ListLotsToCheck() = %v, want the eggs", ls)
	}
	if _, err := kitchen.CheckLot(ctx, connect.NewRequest(&inventoryv1.CheckLotRequest{LotId: lotID})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("CheckLot() without a result: code = %v, want InvalidArgument", connect.CodeOf(err))
	}
	checked, err := kitchen.CheckLot(ctx, connect.NewRequest(&inventoryv1.CheckLotRequest{LotId: lotID, Result: inventoryv1.CheckResult_CHECK_RESULT_OK}))
	noErr(t, err)
	if checked.Msg.GetLot().GetLastOkAt() == nil {
		t.Error("the check is not on the lot")
	}
	stock, err := kitchen.ListStock(ctx, connect.NewRequest(&inventoryv1.ListStockRequest{}))
	noErr(t, err)
	if st := stock.Msg.GetIngredients(); len(st) != 1 || st[0].GetUsableToday() != 30 {
		t.Errorf("checked eggs: %v, want 30 usable", st)
	}

	thrown, err := kitchen.CheckLot(ctx, connect.NewRequest(&inventoryv1.CheckLotRequest{
		LotId: lotID, Result: inventoryv1.CheckResult_CHECK_RESULT_DISCARD, Reason: inventoryv1.DiscardReason_DISCARD_REASON_MOLD,
	}))
	noErr(t, err)
	if l := thrown.Msg.GetLot(); l.GetBalance() != 0 || l.GetStatus() != inventoryv1.LotStatus_LOT_STATUS_DISCARDED {
		t.Errorf("thrown away: %v", l)
	}
	var waste int64
	noErr(t, w.d.Pool().QueryRow(ctx, "select qty from stock_movements where kind = 'waste' and reason = 'berjamur'").Scan(&waste))
	if waste != -30 {
		t.Errorf("waste = %d, want -30", waste)
	}
	if _, err := kitchen.CheckLot(ctx, connect.NewRequest(&inventoryv1.CheckLotRequest{LotId: lotID, Result: inventoryv1.CheckResult_CHECK_RESULT_OK})); precondition(t, err) != "lot_empty" {
		t.Error("a discarded lot was checked again")
	}

	customer := inventoryv1connect.NewInventoryServiceClient(w.http, w.url, w.bearer(t, uuid.New(), "6281234567890"))
	if _, err := customer.ListStock(ctx, connect.NewRequest(&inventoryv1.ListStockRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("customer ListStock() code = %v, want PermissionDenied", connect.CodeOf(err))
	}
}
