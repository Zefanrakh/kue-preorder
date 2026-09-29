//go:build integration

package main

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	aggregationv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/aggregation/v1"
	inventoryv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/inventory/v1"
	ordersv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/orders/v1"
	procurementv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/procurement/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/procurement/v1/procurementv1connect"
)

func (w *wired) procurement(t *testing.T, role string) procurementv1connect.ProcurementServiceClient {
	t.Helper()
	_, user := w.staff(t, role)
	return procurementv1connect.NewProcurementServiceClient(w.http, w.url, w.bearer(t, user, ""))
}

// From the shopping list to the stock: flour is ordered from Toko Sinar,
// 800 g of the 1 kg arrive, the stock holds them once, the 200 g short is to
// buy again, and an order for it cancelled gives it back to the list.
func TestProcurement_ShoppingListToStock(t *testing.T) {
	w := newWired(t)
	ctx := t.Context()
	w.withRecipe(t)
	placed, _ := w.placed(t, 8) // 20 donuts: 1,000 g of flour
	owner, _ := w.staff(t, "owner")
	_, err := owner.RecordManualPayment(ctx, connect.NewRequest(&ordersv1.RecordManualPaymentRequest{
		Code: placed.GetCode(), AmountIdr: 80000, Reference: "BCA 1", Note: "DP",
	}))
	noErr(t, err)
	flour := w.flourLine(t)
	supplier := flour.GetPack().GetSupplierId()
	buying := w.procurement(t, "kitchen")

	created, err := buying.CreateOrder(ctx, connect.NewRequest(&procurementv1.CreateOrderRequest{Date: "2026-10-08", SupplierId: supplier}))

	noErr(t, err)
	o := created.Msg.GetOrder()
	if o.GetSupplierName() != "Toko Sinar" || o.GetStatus() != procurementv1.OrderStatus_ORDER_STATUS_ORDERED || len(o.GetItems()) != 1 ||
		o.GetItems()[0].GetQty() != 1000 || o.GetItems()[0].GetPack().GetCount() != 1 || o.GetCostIdr() != 14000 {
		t.Fatalf("order = %v, want 1 kg of flour from Toko Sinar at Rp14.000", o)
	}
	if l := w.flourLine(t); l.GetStatus() != aggregationv1.LineStatus_LINE_STATUS_ORDERED || l.GetOrdered() != 1000 || l.GetToBuy() != 0 {
		t.Errorf("after ordering: %v, want ordered and nothing to buy", l)
	}
	if _, err := buying.CreateOrder(ctx, connect.NewRequest(&procurementv1.CreateOrderRequest{Date: "2026-10-08", SupplierId: supplier})); precondition(t, err) != "nothing_to_order" {
		t.Error("the same flour was ordered twice")
	}

	received, err := buying.ReceiveOrder(ctx, connect.NewRequest(&procurementv1.ReceiveOrderRequest{
		OrderId: o.GetId(), Items: []*procurementv1.ReceivedItem{{ItemId: o.GetItems()[0].GetId(), Qty: 800}},
	}))
	noErr(t, err)
	if r := received.Msg.GetOrder(); r.GetStatus() != procurementv1.OrderStatus_ORDER_STATUS_RECEIVED || r.GetItems()[0].GetQtyReceived() != 800 {
		t.Errorf("after receiving: %v, want the order closed with 800 g received", r)
	}
	if l := w.flourLine(t); l.GetStatus() != aggregationv1.LineStatus_LINE_STATUS_RECEIVED || l.GetOrdered() != 0 ||
		l.GetUsableStock() != 800 || l.GetToBuy() != 200 {
		t.Errorf("after 800 g arrived: %v, want them counted once, as stock, and 200 g to buy", l)
	}
	stock, err := w.inventory(t, "kitchen").ListStock(ctx, connect.NewRequest(&inventoryv1.ListStockRequest{}))
	noErr(t, err)
	if lots := stock.Msg.GetIngredients()[0].GetLots(); len(lots) != 1 || lots[0].GetBalance() != 800 || lots[0].GetSource() != inventoryv1.LotSource_LOT_SOURCE_PROCUREMENT {
		t.Errorf("stock = %v, want one procurement lot of 800 g", lots)
	}

	rest, err := buying.CreateOrder(ctx, connect.NewRequest(&procurementv1.CreateOrderRequest{Date: "2026-10-08", SupplierId: supplier, Note: "Kurang 200 g"}))
	noErr(t, err)
	if l := w.flourLine(t); l.GetStatus() != aggregationv1.LineStatus_LINE_STATUS_ORDERED || l.GetToBuy() != 0 {
		t.Errorf("after ordering the rest: %v", l)
	}
	cancelled, err := buying.CancelOrder(ctx, connect.NewRequest(&procurementv1.CancelOrderRequest{OrderId: rest.Msg.GetOrder().GetId(), Reason: "Beli di pasar saja"}))
	noErr(t, err)
	if c := cancelled.Msg.GetOrder(); c.GetStatus() != procurementv1.OrderStatus_ORDER_STATUS_CANCELLED {
		t.Errorf("cancelled order = %v", c)
	}
	if l := w.flourLine(t); l.GetStatus() != aggregationv1.LineStatus_LINE_STATUS_RECEIVED || l.GetToBuy() != 200 || l.GetReceived() != 800 {
		t.Errorf("after cancelling the rest: %v, want 200 g to buy again", l)
	}

	list, err := buying.ListOrders(ctx, connect.NewRequest(&procurementv1.ListOrdersRequest{Date: "2026-10-08"}))
	noErr(t, err)
	if len(list.Msg.GetOrders()) != 2 {
		t.Errorf("ListOrders() = %d orders, want 2", len(list.Msg.GetOrders()))
	}
	var audits int
	noErr(t, w.d.Pool().QueryRow(ctx, "select count(*) from audit_log where action like 'procurement.%'").Scan(&audits))
	if audits != 4 {
		t.Errorf("%d procurement audit entries, want 4: two orders, a delivery, a cancel", audits)
	}
	customer := procurementv1connect.NewProcurementServiceClient(w.http, w.url, w.bearer(t, uuid.New(), "6281234567890"))
	if _, err := customer.ListOrders(ctx, connect.NewRequest(&procurementv1.ListOrdersRequest{Date: "2026-10-08"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("customer ListOrders() code = %v, want PermissionDenied", connect.CodeOf(err))
	}
}
