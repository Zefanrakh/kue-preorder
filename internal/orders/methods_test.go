package orders_test

import (
	"errors"
	"testing"

	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/orders"
	"github.com/Zefanrakh/kue-preorder/internal/payments"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
)

func option(t *testing.T, q orders.Quote, m payments.Method) orders.PaymentOption {
	t.Helper()
	for _, o := range q.PaymentOptions {
		if o.Method == m {
			return o
		}
	}
	t.Fatalf("no %s option in %+v", m, q.PaymentOptions)
	return orders.PaymentOption{}
}

func TestQuote_PaymentOptions(t *testing.T) {
	s := newShop()

	q := s.quote(t, wib(7, 9, 0), line(s.chocolate, 6), line(s.cheese, 6)) // 99.000, DP 49.500

	if len(q.PaymentOptions) != 4 || q.PaymentOptions[0].Method != payments.MethodQRIS {
		t.Fatalf("options = %+v, want all four methods, QRIS first", q.PaymentOptions)
	}
	if o := option(t, q, payments.MethodQRIS); o.FullFeeIDR != 0 || *o.DPFeeIDR != 0 {
		t.Errorf("QRIS = %+v, want no fee", o)
	}
	if o := option(t, q, payments.MethodBankTransfer); o.FullFeeIDR != 4440 || *o.DPFeeIDR != 4440 {
		t.Errorf("bank transfer = %+v, want Rp4.440 on both", o)
	}
	// 2% grossed up: 49.500 → 1.011, 99.000 → 2.021.
	if o := option(t, q, payments.MethodEWallet); *o.DPFeeIDR != 1011 || o.FullFeeIDR != 2021 {
		t.Errorf("e-wallet = %+v, want 1.011 on the DP and 2.021 in full", o)
	}
}

func TestQuote_DisabledMethodsAreNotOffered(t *testing.T) {
	s := newShop()
	rule := s.policies.rules[payments.MethodMinimarket]
	rule.Enabled = false
	s.policies.rules[payments.MethodMinimarket] = rule

	q := s.quote(t, wib(7, 9, 0), line(s.chocolate, 1))

	for _, o := range q.PaymentOptions {
		if o.Method == payments.MethodMinimarket {
			t.Errorf("options = %+v, want no minimarket", q.PaymentOptions)
		}
	}
}

// Below the threshold a DP would mean two payments and two fees: the order
// is paid in full at once.
func TestQuote_SmallOrdersPayInFull(t *testing.T) {
	s := newShop()
	s.policies.policy.DPMinTotalIDR = 150_000
	pricey := s.add(catalog.VariantSummary{ProductName: "Lapis", Name: "Legit", PriceIDR: 149_999, ProductionMinutes: 90, MinNoticeHours: 12, Active: true, ProductActive: true})
	priceier := s.add(catalog.VariantSummary{ProductName: "Lapis", Name: "Legit besar", PriceIDR: 150_000, ProductionMinutes: 90, MinNoticeHours: 12, Active: true, ProductActive: true})

	small := s.quote(t, wib(8, 9, 0), line(pricey, 1))
	big := s.quote(t, wib(8, 9, 0), line(priceier, 1))

	if !small.FullPaymentRequired || small.FullPaymentReason != orders.FullPaymentSmallOrder || small.DPRequiredIDR != 149_999 || option(t, small, payments.MethodBankTransfer).DPFeeIDR != nil {
		t.Errorf("Rp149.999 = %+v, want full payment for a small order and no DP fees", small)
	}
	if big.FullPaymentRequired || big.FullPaymentReason != "" || big.DPRequiredIDR != 75_000 {
		t.Errorf("Rp150.000 = %+v, want a DP of 75.000", big)
	}
}

func TestQuote_FullPaymentReasonSchedule(t *testing.T) {
	s := newShop()
	s.clock.Set(wib(6, 17, 0)) // shopping for the 7th closes at 19.30

	q := s.quote(t, wib(7, 9, 0), line(s.chocolate, 30))

	if !q.FullPaymentRequired || q.FullPaymentReason != orders.FullPaymentSchedule {
		t.Errorf("quote = %+v, want full payment because of the schedule", q)
	}
}

func TestQuote_MinimumOrder(t *testing.T) {
	s := newShop()
	s.policies.policy.MinOrderIDR = 30_000

	_, err := s.checkout.Quote(t.Context(), orders.QuoteRequest{Items: items(line(s.chocolate, 3)), PickupAt: wib(7, 9, 0)}) // 24.000

	var v *apperr.ValidationError
	if !errors.As(err, &v) || v.Fields["items"] != "Minimal pesanan Rp30.000." {
		t.Errorf("Quote() error = %v, want the minimum order", err)
	}
	if q := s.quote(t, wib(7, 9, 0), line(s.chocolate, 4)); q.TotalIDR != 32_000 { // 32.000 passes
		t.Errorf("quote = %+v", q)
	}
}

func TestPlace_ChargesTheMethodsFee(t *testing.T) {
	s := newShop()
	req := s.placeRequest(wib(7, 9, 0), line(s.chocolate, 6), line(s.cheese, 6))
	req.Method = payments.MethodBankTransfer

	o := s.place(t, req)

	p := o.Payments[0]
	if p.Method != payments.MethodBankTransfer || p.Kind != payments.KindDP || p.AmountIDR != 49_500 || p.FeeIDR != 4440 {
		t.Errorf("payment = %+v, want a bank transfer DP of 49.500 with Rp4.440 on top", p)
	}
	if r := s.provider.requests[0]; r.Method != payments.MethodBankTransfer || r.FeeIDR != 4440 || r.AmountIDR != 49_500 {
		t.Errorf("invoice request = %+v, want the method and its fee", r)
	}
}

func TestPlace_NeedsAnOfferedMethod(t *testing.T) {
	for name, m := range map[string]payments.Method{"none": "", "unknown": "cash"} {
		t.Run(name, func(t *testing.T) {
			s := newShop()
			req := s.placeRequest(wib(7, 9, 0), line(s.chocolate, 1))
			req.Method = m
			_, err := s.checkout.Place(t.Context(), req)
			var v *apperr.ValidationError
			if !errors.As(err, &v) || v.Fields["payment_method"] == "" || s.repo.inserts != 0 {
				t.Errorf("Place() error = %v, want a message on payment_method and nothing stored", err)
			}
		})
	}
}

func items(lines ...orders.ItemRequest) []orders.ItemRequest { return lines }
