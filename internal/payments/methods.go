package payments

import (
	"fmt"
	"math/big"

	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
)

// Method is how a customer pays one payment (§14). The customer picks it at
// checkout, sees its "Biaya admin" first, and the invoice offers only it.
type Method string

// Payment methods. Cards are left out: cakes do not need them, and they
// need an NPWP and cost the most.
const (
	MethodQRIS         Method = "qris"
	MethodBankTransfer Method = "bank_transfer" // virtual accounts of every bank
	MethodEWallet      Method = "ewallet"       // GoPay, ShopeePay
	MethodMinimarket   Method = "minimarket"    // Indomaret, Alfamart
)

// Methods lists every method, in the order checkout shows them: QRIS first,
// since it costs the customer nothing.
var Methods = []Method{MethodQRIS, MethodBankTransfer, MethodEWallet, MethodMinimarket}

// VATBPS is Indonesia's VAT (PPN) on provider fees, in basis points: 11%.
const VATBPS = 1100

const (
	bps      = 10_000
	maxRate  = 5_000 // 50%: anything above is surely a typo
	maxFixed = 1_000_000
)

// FeeRule is what the payment provider charges the shop for a method, and
// so the "Biaya admin" the customer pays on top (§27: fees are passed on,
// except for QRIS).
type FeeRule struct {
	Method Method
	// FixedIDR is charged per transaction; RateBPS on the amount paid, in
	// basis points (200 is 2%).
	FixedIDR int64
	RateBPS  int32
	// VATIncluded says the provider's price already holds VAT; otherwise
	// VAT is charged on top of it.
	VATIncluded bool
	// Enabled offers the method at checkout.
	Enabled bool
}

// DefaultFeeRules are Midtrans' prices as published (checked 27 September
// 2026, §27.2) for a tenant that has not set its own. QRIS costs the shop
// 0.7%, 0% up to Rp100.000 from 1 October 2026, and never the customer.
func DefaultFeeRules() map[Method]FeeRule {
	return map[Method]FeeRule{
		MethodQRIS:         {Method: MethodQRIS, RateBPS: 70, VATIncluded: true, Enabled: true},
		MethodBankTransfer: {Method: MethodBankTransfer, FixedIDR: 4000, Enabled: true},
		MethodEWallet:      {Method: MethodEWallet, RateBPS: 200, VATIncluded: true, Enabled: true},
		MethodMinimarket:   {Method: MethodMinimarket, FixedIDR: 5000, Enabled: true},
	}
}

// Validate checks a rule, with messages for the owner editing it.
func (r FeeRule) Validate() error {
	f := apperr.Fields{}
	f.Check(r.Method.valid(), "method", "Metode bayar tidak dikenal.")
	f.Check(r.FixedIDR >= 0 && r.FixedIDR <= maxFixed, "fixed_idr", "Biaya tetap 0 sampai Rp1.000.000.")
	f.Check(r.RateBPS >= 0 && r.RateBPS <= maxRate, "rate_bps", "Biaya persen 0 sampai 50%.")
	return f.Err()
}

func (m Method) valid() bool {
	for _, known := range Methods {
		if m == known {
			return true
		}
	}
	return false
}

// Fee is the "Biaya admin" for paying amountIDR with the rule's method: the
// smallest fee after which what the provider leaves the shop is still the
// whole amount. The provider takes its cut from everything paid, the fee
// included, so a percentage is grossed up: fee = gross - amount where
//
//	gross - ceil(fixed with VAT) - ceil(gross × rate with VAT) >= amount.
//
// Rounding up is the provider's worst case, so the shop never receives less.
// QRIS is always 0: Bank Indonesia forbids passing its cost to the customer
// under any name.
func (r FeeRule) Fee(amountIDR int64) (int64, error) {
	if r.Method == MethodQRIS {
		return 0, nil
	}
	if err := r.Validate(); err != nil {
		return 0, err
	}
	if amountIDR <= 0 || amountIDR > MaxOrderTotalIDR {
		return 0, fmt.Errorf("%w: fee on %d", ErrInvalidAmount, amountIDR)
	}
	vat := int64(bps + VATBPS)
	fixed := ceilDiv(r.FixedIDR*vat, bps)

	// The provider keeps gross × num / den of every gross.
	num := big.NewInt(int64(r.RateBPS))
	den := big.NewInt(bps)
	if !r.VATIncluded {
		num.Mul(num, big.NewInt(vat))
		den.Mul(den, big.NewInt(bps))
	}
	cut := func(gross int64) int64 { // ceil(gross × num / den)
		p := new(big.Int).Mul(big.NewInt(gross), num)
		p.Add(p, new(big.Int).Sub(den, big.NewInt(1)))
		return p.Quo(p, den).Int64()
	}

	// Start from the exact solution, (amount + fixed) × den / (den - num),
	// rounded up, then step until the rounded cut is covered.
	g := new(big.Int).Mul(big.NewInt(amountIDR+fixed), den)
	rest := new(big.Int).Sub(den, num)
	g.Add(g, new(big.Int).Sub(rest, big.NewInt(1)))
	gross := g.Quo(g, rest).Int64()
	for gross-fixed-cut(gross) < amountIDR {
		gross++
	}
	return gross - amountIDR, nil
}
