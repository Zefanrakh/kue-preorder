//go:build integration

package main

import (
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	ordersv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/orders/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/orders/v1/ordersv1connect"
	schedulingv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/scheduling/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/scheduling/v1/schedulingv1connect"
	validationv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/validation/v1"
	"github.com/Zefanrakh/kue-preorder/internal/orders"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
)

func (w *wired) customer(t *testing.T, phone string) ordersv1connect.CustomerOrderServiceClient {
	t.Helper()
	return ordersv1connect.NewCustomerOrderServiceClient(w.http, w.url, w.bearer(t, uuid.New(), phone))
}

func (w *wired) placeRequest(day int32, key string) *ordersv1.PlaceOrderRequest {
	return &ordersv1.PlaceOrderRequest{
		Items:    []*ordersv1.CartItem{{VariantId: w.donut.ID.String(), Quantity: 3}},
		PickupAt: timestamppb.New(wib(int(day), 9, 0)), TermsVersion: orders.TermsVersion,
		CustomerName: "Sari", CustomerEmail: "sari@contoh.com", Notes: "Tulisan: Selamat ulang tahun Budi",
		IdempotencyKey: key,
	}
}

func precondition(t *testing.T, err error) string {
	t.Helper()
	var cerr *connect.Error
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeFailedPrecondition {
		t.Fatalf("error = %v, want FailedPrecondition", err)
	}
	for _, d := range cerr.Details() {
		if v, _ := d.Value(); v != nil {
			if p, ok := v.(*validationv1.Precondition); ok {
				return p.GetReason()
			}
		}
	}
	t.Fatalf("error %v has no Precondition detail", err)
	return ""
}

func TestPlaceOrder(t *testing.T) {
	w := newWired(t)
	sari := w.customer(t, "6281234567890")
	ctx := t.Context()
	key := uuid.NewString()

	res, err := sari.PlaceOrder(ctx, connect.NewRequest(w.placeRequest(8, key)))
	noErr(t, err)
	o := res.Msg.GetOrder()
	if len(o.GetCode()) != 6 || o.GetStatus() != ordersv1.OrderStatus_ORDER_STATUS_AWAITING_DP || o.GetPaymentStatus() != ordersv1.PaymentStatus_PAYMENT_STATUS_UNPAID ||
		o.GetTotalIdr() != 24000 || o.GetDpRequiredIdr() != 12000 || o.GetNotes() != "Tulisan: Selamat ulang tahun Budi" || o.GetCustomerName() != "Sari" {
		t.Errorf("PlaceOrder() = %v", o)
	}
	if ps := o.GetPayments(); len(ps) != 1 || ps[0].GetKind() != ordersv1.PaymentKind_PAYMENT_KIND_DP || ps[0].GetAmountIdr() != 12000 ||
		ps[0].GetState() != ordersv1.PaymentState_PAYMENT_STATE_PENDING || !strings.HasPrefix(ps[0].GetCheckoutUrl(), "https://pay.dev.invalid/") {
		t.Errorf("payments = %v, want a pending DP of 12,000 with a link", ps)
	}

	// The phone came from the sign-in, stored in E.164 on the order.
	var phone string
	noErr(t, w.d.Pool().QueryRow(ctx, "select customer_phone from orders where code = $1", o.GetCode()).Scan(&phone))
	if phone != "+6281234567890" {
		t.Errorf("customer_phone = %q", phone)
	}

	again, err := sari.PlaceOrder(ctx, connect.NewRequest(w.placeRequest(8, key)))
	noErr(t, err)
	if again.Msg.GetOrder().GetId() != o.GetId() {
		t.Errorf("same idempotency key placed %s, want the first order %s", again.Msg.GetOrder().GetCode(), o.GetCode())
	}

	mine, err := sari.GetMyOrder(ctx, connect.NewRequest(&ordersv1.GetMyOrderRequest{Code: strings.ToLower(o.GetCode())}))
	if err != nil || mine.Msg.GetOrder().GetId() != o.GetId() {
		t.Errorf("GetMyOrder() = %v, %v", mine, err)
	}
	someoneElse := w.customer(t, "6289876543210")
	if _, err := someoneElse.GetMyOrder(ctx, connect.NewRequest(&ordersv1.GetMyOrderRequest{Code: o.GetCode()})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("another customer's GetMyOrder() code = %v, want NotFound", connect.CodeOf(err))
	}
}

func TestPlaceOrder_AtMostTwoAwaitingDP(t *testing.T) {
	w := newWired(t)
	sari := w.customer(t, "6281234567890")
	ctx := t.Context()
	for _, day := range []int32{8, 9} {
		_, err := sari.PlaceOrder(ctx, connect.NewRequest(w.placeRequest(day, uuid.NewString())))
		noErr(t, err)
	}

	_, err := sari.PlaceOrder(ctx, connect.NewRequest(w.placeRequest(10, uuid.NewString())))

	if reason := precondition(t, err); reason != "too_many_unpaid" {
		t.Errorf("reason = %q, want too_many_unpaid", reason)
	}
	list, err := sari.ListMyOrders(ctx, connect.NewRequest(&ordersv1.ListMyOrdersRequest{}))
	if err != nil || len(list.Msg.GetOrders()) != 2 || list.Msg.GetOrders()[0].GetFirstItem() != "Donut Coklat" {
		t.Errorf("ListMyOrders() = %v, %v; want the two orders", list, err)
	}
}

func TestPlaceOrder_NeedsAVerifiedPhone(t *testing.T) {
	w := newWired(t)

	emailOnly := w.customer(t, "")
	_, err := emailOnly.PlaceOrder(t.Context(), connect.NewRequest(w.placeRequest(8, uuid.NewString())))
	if reason := precondition(t, err); reason != "phone_required" {
		t.Errorf("reason = %q, want phone_required", reason)
	}

	anonymous := ordersv1connect.NewCustomerOrderServiceClient(w.http, w.url)
	if _, err := anonymous.PlaceOrder(t.Context(), connect.NewRequest(w.placeRequest(8, uuid.NewString()))); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("anonymous PlaceOrder() code = %v, want Unauthenticated", connect.CodeOf(err))
	}
}

// Closing a day that has orders puts it on hold: the CMS shows how many are
// left to move.
func TestClosedDayWithOrdersIsOnHold(t *testing.T) {
	w := newWired(t)
	ctx := t.Context()
	_, err := w.customer(t, "6281234567890").PlaceOrder(ctx, connect.NewRequest(w.placeRequest(8, uuid.NewString())))
	noErr(t, err)
	owner := uuid.New()
	_, err = w.d.Pool().Exec(ctx, "insert into staff_roles (tenant_id, auth_user_id, role, created_at) values ($1, $2, 'owner', now())", dbtest.DefaultTenantID, owner)
	noErr(t, err)
	cms := schedulingv1connect.NewScheduleAdminServiceClient(w.http, w.url, w.bearer(t, owner, ""))

	_, err = cms.AddClosedDate(ctx, connect.NewRequest(&schedulingv1.AddClosedDateRequest{Date: "2026-10-08", Reason: "Ibu sakit"}))
	noErr(t, err)
	res, err := cms.ListClosedDates(ctx, connect.NewRequest(&schedulingv1.ListClosedDatesRequest{FromDate: "2026-10-01", ToDate: "2026-10-31"}))
	noErr(t, err)

	byDate := map[string]int32{}
	for _, d := range res.Msg.GetClosedDates() {
		byDate[d.GetDate()] = d.GetActiveOrders()
	}
	if byDate["2026-10-08"] != 1 || byDate["2026-10-07"] != 0 {
		t.Errorf("closed dates = %v, want the 8th on hold with 1 order and the 7th a plain day off", byDate)
	}
}
