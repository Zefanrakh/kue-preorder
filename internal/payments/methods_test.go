package payments_test

import (
	"math/big"
	"testing"

	"pgregory.net/rapid"

	"github.com/Zefanrakh/kue-preorder/internal/payments"
)

func TestFee_DefaultRules(t *testing.T) {
	rules := payments.DefaultFeeRules()
	tests := []struct {
		method payments.Method
		amount int64
		want   int64
	}{
		// Rp4.000 + 11% VAT.
		{payments.MethodBankTransfer, 100_000, 4440},
		{payments.MethodBankTransfer, 10_000, 4440},
		// Rp5.000 + 11% VAT.
		{payments.MethodMinimarket, 100_000, 5550},
		// 2% of what is paid, fee included: 102.041 × 2% = 2.040,82, rounded
		// up to 2.041, leaves exactly 100.000.
		{payments.MethodEWallet, 100_000, 2041},
		// QRIS never costs the customer anything, whatever it costs the shop.
		{payments.MethodQRIS, 100_000, 0},
		{payments.MethodQRIS, 5_000_000, 0},
	}
	for _, tt := range tests {
		got, err := rules[tt.method].Fee(tt.amount)
		if err != nil || got != tt.want {
			t.Errorf("Fee(%s, %d) = %d, %v; want %d", tt.method, tt.amount, got, err, tt.want)
		}
	}
}

func TestFee_QRISIgnoresAnyRule(t *testing.T) {
	r := payments.FeeRule{Method: payments.MethodQRIS, FixedIDR: 4000, RateBPS: 70}
	if got, err := r.Fee(100_000); err != nil || got != 0 {
		t.Errorf("Fee() = %d, %v; want 0: Bank Indonesia forbids a QRIS surcharge", got, err)
	}
}

func TestFee_RejectsBadInput(t *testing.T) {
	ok := payments.DefaultFeeRules()[payments.MethodBankTransfer]
	for name, tt := range map[string]struct {
		rule   payments.FeeRule
		amount int64
	}{
		"zero amount":    {ok, 0},
		"absurd amount":  {ok, payments.MaxOrderTotalIDR + 1},
		"unknown method": {payments.FeeRule{Method: "cash"}, 1000},
		"negative fixed": {payments.FeeRule{Method: payments.MethodBankTransfer, FixedIDR: -1}, 1000},
		"rate above 50%": {payments.FeeRule{Method: payments.MethodEWallet, RateBPS: 5001}, 1000},
	} {
		if got, err := tt.rule.Fee(tt.amount); err == nil {
			t.Errorf("%s: Fee() = %d, want an error", name, got)
		}
	}
}

// kept is what the provider leaves the shop of gross, at its worst rounding.
func kept(r payments.FeeRule, gross int64) int64 {
	vat := int64(10_000 + payments.VATBPS)
	fixed := (r.FixedIDR*vat + 9_999) / 10_000
	num, den := big.NewInt(int64(r.RateBPS)), big.NewInt(10_000)
	if !r.VATIncluded {
		num.Mul(num, big.NewInt(vat))
		den.Mul(den, big.NewInt(10_000))
	}
	cut := new(big.Int).Mul(big.NewInt(gross), num)
	cut.Add(cut, new(big.Int).Sub(den, big.NewInt(1)))
	cut.Quo(cut, den)
	return gross - fixed - cut.Int64()
}

// The shop always keeps the whole amount, and the fee is the smallest that
// does it: one rupiah less and the shop would be short.
func TestProperty_FeeCoversTheProviderExactly(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		r := payments.FeeRule{
			Method:      rapid.SampledFrom([]payments.Method{payments.MethodBankTransfer, payments.MethodEWallet, payments.MethodMinimarket}).Draw(t, "method"),
			FixedIDR:    rapid.Int64Range(0, 100_000).Draw(t, "fixed"),
			RateBPS:     rapid.Int32Range(0, 5_000).Draw(t, "rate"),
			VATIncluded: rapid.Bool().Draw(t, "vat included"),
		}
		amount := rapid.Int64Range(1, payments.MaxOrderTotalIDR).Draw(t, "amount")

		fee, err := r.Fee(amount)

		if err != nil {
			t.Fatalf("Fee() error = %v", err)
		}
		if fee < 0 || kept(r, amount+fee) < amount {
			t.Fatalf("fee %d leaves the shop %d of %d", fee, kept(r, amount+fee), amount)
		}
		if fee > 0 && kept(r, amount+fee-1) >= amount {
			t.Fatalf("fee %d is more than needed: %d would do", fee, fee-1)
		}
	})
}

func TestPolicy_DPAllowed(t *testing.T) {
	p := payments.DefaultPolicy()
	if p.DPAllowed(149_999) || !p.DPAllowed(150_000) {
		t.Error("want a DP from Rp150.000 exactly, not below")
	}
	p.DPMinTotalIDR = 0
	if !p.DPAllowed(1) {
		t.Error("a threshold of 0 allows a DP on any total")
	}
}
