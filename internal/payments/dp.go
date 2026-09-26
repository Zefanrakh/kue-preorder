package payments

import (
	"errors"
	"fmt"
	"time"

	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
)

// MaxOrderTotalIDR bounds an order total. It keeps every product of totals
// and percentages far from int64 overflow; a real order is many orders of
// magnitude smaller, so anything above it is a bug upstream.
const MaxOrderTotalIDR int64 = 1 << 40 // over a trillion rupiah

// Policy is a tenant's payment policy (§9.8, §14).
type Policy struct {
	// DPMinPercent is the smallest DP as a percentage of the total, 1 to 100.
	// There is always a DP: the shop never bakes on credit.
	DPMinPercent int32
	// DPCoversIngredientCost raises the DP to the estimated ingredient cost,
	// so a customer who disappears has paid for what was bought.
	DPCoversIngredientCost bool
	// BalanceDueHoursBefore is how long before production starts the
	// balance must be paid, 0 to 168.
	BalanceDueHoursBefore int32
	// DPInvoiceValidMinutes is how long a DP invoice stays open, 30 to
	// 10080; never past the shopping cutoff (§15).
	DPInvoiceValidMinutes int32
	// UpdatedAt is zero while the defaults are in use.
	UpdatedAt time.Time
}

// DefaultPolicy is the policy of a tenant that has not changed it (§27): a
// DP of half the total that also covers the ingredients, the balance 12
// hours before production, and 3 hours to pay the DP.
func DefaultPolicy() Policy {
	return Policy{DPMinPercent: 50, DPCoversIngredientCost: true, BalanceDueHoursBefore: 12, DPInvoiceValidMinutes: 180}
}

// DPInvoiceValidFor is how long a DP invoice stays open.
func (p Policy) DPInvoiceValidFor() time.Duration {
	return time.Duration(p.DPInvoiceValidMinutes) * time.Minute
}

// ErrInvalidAmount means an amount is negative or absurdly large.
var ErrInvalidAmount = errors.New("invalid amount")

// Validate checks the policy, with messages for the owner editing it.
func (p Policy) Validate() error {
	f := apperr.Fields{}
	f.Check(p.DPMinPercent >= 1 && p.DPMinPercent <= 100, "dp_min_percent", "DP minimal 1 sampai 100 persen dari total.")
	f.Check(p.BalanceDueHoursBefore >= 0 && p.BalanceDueHoursBefore <= 168, "balance_due_hours_before", "Tenggat pelunasan 0 sampai 168 jam sebelum produksi.")
	f.Check(p.DPInvoiceValidMinutes >= 30 && p.DPInvoiceValidMinutes <= 10080, "dp_invoice_valid_minutes", "Masa berlaku tagihan DP 30 menit sampai 7 hari.")
	return f.Err()
}

// DPRequired is the down payment locked at checkout (§14):
//
//	min(total, max(ceil(total × percent / 100), ingredient cost))
//
// The ingredient cost counts only when the policy says so. The DP never
// exceeds the total, even for a cake sold below its ingredient cost.
func DPRequired(p Policy, totalIDR, ingredientCostIDR int64) (int64, error) {
	if p.DPMinPercent < 1 || p.DPMinPercent > 100 {
		return 0, fmt.Errorf("dp_min_percent must be 1 to 100, got %d", p.DPMinPercent)
	}
	if totalIDR <= 0 || totalIDR > MaxOrderTotalIDR {
		return 0, fmt.Errorf("%w: total %d", ErrInvalidAmount, totalIDR)
	}
	if ingredientCostIDR < 0 || ingredientCostIDR > MaxOrderTotalIDR {
		return 0, fmt.Errorf("%w: ingredient cost %d", ErrInvalidAmount, ingredientCostIDR)
	}
	dp := ceilDiv(totalIDR*int64(p.DPMinPercent), 100)
	if p.DPCoversIngredientCost {
		dp = max(dp, ingredientCostIDR)
	}
	return min(dp, totalIDR), nil
}

// ceilDiv divides non-negative a by positive b, rounding up.
func ceilDiv(a, b int64) int64 {
	return (a + b - 1) / b
}
