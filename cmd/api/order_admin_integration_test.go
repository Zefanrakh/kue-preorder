//go:build integration

package main

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	ordersv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/orders/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/orders/v1/ordersv1connect"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
)

// staff signs in a staff member with role and returns their CMS client.
func (w *wired) staff(t *testing.T, role string) (ordersv1connect.OrderAdminServiceClient, uuid.UUID) {
	t.Helper()
	user := uuid.New()
	_, err := w.d.Pool().Exec(t.Context(), "insert into staff_roles (tenant_id, auth_user_id, role, created_at) values ($1, $2, $3, now())",
		dbtest.DefaultTenantID, user, role)
	noErr(t, err)
	return ordersv1connect.NewOrderAdminServiceClient(w.http, w.url, w.bearer(t, user, "")), user
}

// placed places an order for Sari and returns it with her client.
func (w *wired) placed(t *testing.T, day int32) (*ordersv1.Order, ordersv1connect.CustomerOrderServiceClient) {
	t.Helper()
	sari := w.customer(t, "6281234567890")
	res, err := sari.PlaceOrder(t.Context(), connect.NewRequest(w.placeRequest(day, uuid.NewString())))
	noErr(t, err)
	return res.Msg.GetOrder(), sari
}

// A DP transferred straight to the shop, then the balance, then production
// to pickup: through every real repository, in one transaction per step.
func TestOrderAdmin_ManualPaymentsToPickup(t *testing.T) {
	w := newWired(t)
	ctx := t.Context()
	placed, sari := w.placed(t, 8) // 20 donuts, 160,000, a DP of 80,000
	owner, ownerID := w.staff(t, "owner")
	kitchen, _ := w.staff(t, "kitchen")
	pay := func(amount int64) (*connect.Response[ordersv1.RecordManualPaymentResponse], error) {
		w.clock.Advance(time.Minute) // the ledger lists payments by when they were made
		return owner.RecordManualPayment(ctx, connect.NewRequest(&ordersv1.RecordManualPaymentRequest{
			Code: placed.GetCode(), AmountIdr: amount, Reference: "BCA 0412", Note: "Transfer langsung ke rekening toko",
		}))
	}
	advance := func(to ordersv1.OrderStatus) (*connect.Response[ordersv1.AdvanceOrderResponse], error) {
		return kitchen.AdvanceOrder(ctx, connect.NewRequest(&ordersv1.AdvanceOrderRequest{Code: placed.GetCode(), Status: to}))
	}

	if _, err := kitchen.RecordManualPayment(ctx, connect.NewRequest(&ordersv1.RecordManualPaymentRequest{
		Code: placed.GetCode(), AmountIdr: 80000, Reference: "x", Note: "x",
	})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("kitchen RecordManualPayment() code = %v, want PermissionDenied: money is the owner's", connect.CodeOf(err))
	}
	if _, err := pay(160001); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("overpaying: code = %v, want InvalidArgument", connect.CodeOf(err))
	}

	res, err := pay(80000)
	noErr(t, err)
	o := res.Msg.GetOrder()
	if o.GetStatus() != ordersv1.OrderStatus_ORDER_STATUS_CONFIRMED || o.GetPaymentStatus() != ordersv1.PaymentStatus_PAYMENT_STATUS_DP_PAID ||
		o.GetCustomerPhone() != "+6281234567890" {
		t.Errorf("after the DP: %v", o)
	}
	ps := o.GetPayments()
	if len(ps) != 2 || ps[0].GetState() != ordersv1.PaymentState_PAYMENT_STATE_EXPIRED || ps[1].GetKind() != ordersv1.PaymentKind_PAYMENT_KIND_DP ||
		ps[1].GetManual().GetReference() != "BCA 0412" || ps[1].GetManual().GetRecordedBy() != ownerID.String() || ps[1].GetPaidAt() == nil {
		t.Errorf("payments = %v, want the invoice expired and the manual DP with its proof", ps)
	}

	// The customer sees the payment, not the shop's notes, and no new link.
	mine, err := sari.GetMyOrder(ctx, connect.NewRequest(&ordersv1.GetMyOrderRequest{Code: placed.GetCode()}))
	noErr(t, err)
	if mps := mine.Msg.GetOrder().GetPayments(); len(mps) != 2 || mps[1].GetManual() != nil || mps[0].GetState() != ordersv1.PaymentState_PAYMENT_STATE_EXPIRED {
		t.Errorf("customer's payments = %v, want the manual DP without its proof", mps)
	}

	_, err = advance(ordersv1.OrderStatus_ORDER_STATUS_IN_PRODUCTION)
	if reason := precondition(t, err); reason != "not_paid_in_full" {
		t.Errorf("production before the balance: reason = %q, want not_paid_in_full", reason)
	}

	_, err = pay(80000)
	noErr(t, err)
	for _, to := range []ordersv1.OrderStatus{ordersv1.OrderStatus_ORDER_STATUS_IN_PRODUCTION, ordersv1.OrderStatus_ORDER_STATUS_READY, ordersv1.OrderStatus_ORDER_STATUS_COMPLETED} {
		res, err := advance(to)
		noErr(t, err)
		if got := res.Msg.GetOrder(); got.GetStatus() != to || got.GetPaymentStatus() != ordersv1.PaymentStatus_PAYMENT_STATUS_PAID_IN_FULL {
			t.Errorf("AdvanceOrder(%s) = %s/%s", to, got.GetStatus(), got.GetPaymentStatus())
		}
	}

	var audits, events int
	noErr(t, w.d.Pool().QueryRow(ctx, `select
		(select count(*) from audit_log where entity_id = $1 and action = 'orders.payment.recorded_manually' and actor_id = $2),
		(select count(*) from outbox where payload->>'code' = $3 and event_type in ('order.payment_received', 'order.confirmed', 'order.status_changed'))`,
		uuid.MustParse(placed.GetId()), ownerID, placed.GetCode()).Scan(&audits, &events))
	if audits != 2 || events != 6 {
		t.Errorf("%d audit entries, %d events; want 2 (both payments) and 6 (2 payments, 1 confirmation, 3 steps)", audits, events)
	}

	list, err := kitchen.ListOrders(ctx, connect.NewRequest(&ordersv1.ListOrdersRequest{FromDate: "2026-10-08", ToDate: "2026-10-08", Query: "0812-3456-7890"}))
	noErr(t, err)
	if got := list.Msg.GetOrders(); len(got) != 1 || got[0].GetSummary().GetCode() != placed.GetCode() || got[0].GetCustomerPhone() != "+6281234567890" ||
		got[0].GetProductionDate() != "2026-10-08" {
		t.Errorf("ListOrders(phone) = %v", got)
	}
}

// A refund returns everything, on a negative ledger row, and cancels the
// order; money arriving afterwards is refused.
func TestOrderAdmin_Refund(t *testing.T) {
	w := newWired(t)
	ctx := t.Context()
	placed, _ := w.placed(t, 8)
	owner, _ := w.staff(t, "owner")
	w.clock.Advance(time.Minute)
	_, err := owner.RecordManualPayment(ctx, connect.NewRequest(&ordersv1.RecordManualPaymentRequest{
		Code: placed.GetCode(), AmountIdr: 80000, Reference: "BCA 1", Note: "DP",
	}))
	noErr(t, err)
	w.clock.Advance(time.Minute)

	res, err := owner.CancelOrder(ctx, connect.NewRequest(&ordersv1.CancelOrderRequest{
		Code: placed.GetCode(), Mode: ordersv1.CancelMode_CANCEL_MODE_REFUND, Reason: "Oven rusak", RefundReference: "BCA balik 77",
	}))

	noErr(t, err)
	o := res.Msg.GetOrder()
	if o.GetStatus() != ordersv1.OrderStatus_ORDER_STATUS_CANCELLED || o.GetPaymentStatus() != ordersv1.PaymentStatus_PAYMENT_STATUS_REFUNDED {
		t.Errorf("after the refund: %s/%s", o.GetStatus(), o.GetPaymentStatus())
	}
	var sum int64
	for _, p := range o.GetPayments() {
		if p.GetState() == ordersv1.PaymentState_PAYMENT_STATE_PAID {
			sum += p.GetAmountIdr()
		}
	}
	if last := o.GetPayments()[len(o.GetPayments())-1]; sum != 0 || last.GetKind() != ordersv1.PaymentKind_PAYMENT_KIND_REFUND || last.GetManual().GetReference() != "BCA balik 77" {
		t.Errorf("ledger sums to %d, last %v; want 0 after a refund row", sum, last)
	}

	_, err = owner.RecordManualPayment(ctx, connect.NewRequest(&ordersv1.RecordManualPaymentRequest{
		Code: placed.GetCode(), AmountIdr: 80000, Reference: "BCA 2", Note: "Pelunasan terlambat",
	}))
	if reason := precondition(t, err); reason != "payments_closed" {
		t.Errorf("money after the refund: reason = %q, want payments_closed", reason)
	}
	if _, err := owner.CancelOrder(ctx, connect.NewRequest(&ordersv1.CancelOrderRequest{Code: placed.GetCode(), Reason: "x"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("CancelOrder() without a mode: code = %v, want InvalidArgument", connect.CodeOf(err))
	}
}
