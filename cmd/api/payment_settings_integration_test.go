//go:build integration

package main

import (
	"testing"

	"connectrpc.com/connect"

	ordersv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/orders/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/orders/v1/ordersv1connect"
	paymentsv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/payments/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/payments/v1/paymentsv1connect"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
)

func (w *wired) settings(t *testing.T, role string) paymentsv1connect.PaymentSettingsServiceClient {
	t.Helper()
	_, user := w.staff(t, role)
	return paymentsv1connect.NewPaymentSettingsServiceClient(w.http, w.url, w.bearer(t, user, ""))
}

func option(q *ordersv1.QuoteOrderResponse, m ordersv1.PaymentMethod) *ordersv1.PaymentOption {
	for _, o := range q.GetPaymentOptions() {
		if o.GetMethod() == m {
			return o
		}
	}
	return nil
}

// The owner's changes reach checkout at once, while an order already placed
// keeps what its checkout locked: its DP and its invoice's fee.
func TestPaymentSettings_ReachCheckout(t *testing.T) {
	w := newWired(t)
	ctx := t.Context()
	placed, sari := w.placed(t, 8) // 20 donuts, 160,000, a DP of 80,000 by bank transfer at Rp4.440
	owner, kitchen := w.settings(t, "owner"), w.settings(t, "kitchen")
	checkout := ordersv1connect.NewCheckoutServiceClient(w.http, w.url)
	quoteNow := func() *ordersv1.QuoteOrderResponse {
		t.Helper()
		res, err := quote(t, checkout, w.donut.ID.String(), 20, wib(8, 9, 0))
		noErr(t, err)
		return res.Msg
	}

	got, err := kitchen.GetPaymentSettings(ctx, connect.NewRequest(&paymentsv1.GetPaymentSettingsRequest{}))
	noErr(t, err)
	s := got.Msg.GetSettings()
	if s.GetPolicy().GetDpMinTotalIdr() != 150000 || s.GetPolicy().GetUpdatedAt() != nil || len(s.GetMethods()) != 4 ||
		s.GetMethods()[1].GetExamples()[0].GetFeeIdr() != 4440 {
		t.Errorf("kitchen GetPaymentSettings() = %v, want the defaults with their example fees", s)
	}
	policy := s.GetPolicy()
	policy.DpMinTotalIdr = 200000
	if _, err := kitchen.UpdatePaymentPolicy(ctx, connect.NewRequest(&paymentsv1.UpdatePaymentPolicyRequest{Policy: policy, Reason: "x"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("kitchen UpdatePaymentPolicy() code = %v, want PermissionDenied", connect.CodeOf(err))
	}
	if _, err := owner.UpdatePaymentPolicy(ctx, connect.NewRequest(&paymentsv1.UpdatePaymentPolicyRequest{Policy: policy})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("UpdatePaymentPolicy() without a reason: code = %v, want InvalidArgument", connect.CodeOf(err))
	}

	_, err = owner.UpdatePaymentPolicy(ctx, connect.NewRequest(&paymentsv1.UpdatePaymentPolicyRequest{Policy: policy, Reason: "DP hanya untuk pesanan besar"}))
	noErr(t, err)
	_, err = owner.UpdatePaymentMethod(ctx, connect.NewRequest(&paymentsv1.UpdatePaymentMethodRequest{
		Rule:   &paymentsv1.FeeRule{Method: ordersv1.PaymentMethod_PAYMENT_METHOD_BANK_TRANSFER, FixedIdr: 3500, Enabled: true},
		Reason: "Midtrans turunkan tarif VA",
	}))
	noErr(t, err)
	res, err := owner.UpdatePaymentMethod(ctx, connect.NewRequest(&paymentsv1.UpdatePaymentMethodRequest{
		Rule:   &paymentsv1.FeeRule{Method: ordersv1.PaymentMethod_PAYMENT_METHOD_EWALLET, RateBps: 200, VatIncluded: true},
		Reason: "E-wallet sedang gangguan",
	}))
	noErr(t, err)
	if va := res.Msg.GetSettings().GetMethods()[1]; va.GetRule().GetUpdatedAt() == nil || va.GetDefaultRule().GetFixedIdr() != 4000 || va.GetExamples()[0].GetFeeIdr() != 3885 {
		t.Errorf("bank transfer = %v, want Rp3.500 + PPN next to the default", va)
	}

	q := quoteNow()
	if q.GetFullPaymentReason() != ordersv1.FullPaymentReason_FULL_PAYMENT_REASON_SMALL_ORDER || q.GetDpRequiredIdr() != 160000 {
		t.Errorf("quote = %v, want full payment below the new Rp200.000 threshold", q)
	}
	if va := option(q, ordersv1.PaymentMethod_PAYMENT_METHOD_BANK_TRANSFER); va == nil || va.GetFullFeeIdr() != 3885 {
		t.Errorf("bank transfer option = %v, want Rp3.885", va)
	}
	if option(q, ordersv1.PaymentMethod_PAYMENT_METHOD_EWALLET) != nil {
		t.Errorf("options = %v, want e-wallet switched off", q.GetPaymentOptions())
	}

	mine, err := sari.GetMyOrder(ctx, connect.NewRequest(&ordersv1.GetMyOrderRequest{Code: placed.GetCode()}))
	noErr(t, err)
	if o := mine.Msg.GetOrder(); o.GetDpRequiredIdr() != 80000 || o.GetPayments()[0].GetFeeIdr() != 4440 {
		t.Errorf("the order placed before = DP %d, fee %d; want 80,000 and Rp4.440 as locked at checkout", o.GetDpRequiredIdr(), o.GetPayments()[0].GetFeeIdr())
	}

	_, err = owner.ResetPaymentMethod(ctx, connect.NewRequest(&paymentsv1.ResetPaymentMethodRequest{
		Method: ordersv1.PaymentMethod_PAYMENT_METHOD_EWALLET, Reason: "E-wallet normal lagi",
	}))
	noErr(t, err)
	if ew := option(quoteNow(), ordersv1.PaymentMethod_PAYMENT_METHOD_EWALLET); ew == nil || ew.GetFullFeeIdr() != 3266 {
		t.Errorf("e-wallet option after the reset = %v, want it back at 2%% (Rp3.266 on Rp160.000)", ew)
	}

	var audits int
	noErr(t, w.d.Pool().QueryRow(ctx, "select count(*) from audit_log where tenant_id = $1 and action like 'payments.%'", dbtest.DefaultTenantID).Scan(&audits))
	if audits != 4 {
		t.Errorf("%d audit entries, want 4: the policy, two methods, and the reset", audits)
	}
}
