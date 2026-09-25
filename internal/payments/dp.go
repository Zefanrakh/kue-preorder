package payments

import (
	"errors"
	"fmt"
)

// MaxOrderTotalIDR bounds an order total. It keeps every product of totals
// and percentages far from int64 overflow; a real order is many orders of
// magnitude smaller, so anything above it is a bug upstream.
const MaxOrderTotalIDR int64 = 1 << 40 // over a trillion rupiah

// Policy is a tenant's down payment policy (§9.8, §14).
type Policy struct {
	// DPMinPercent is the smallest DP as a percentage of the total, 1 to 100.
	// There is always a DP: the shop never bakes on credit.
	DPMinPercent int32
	// DPCoversIngredientCost raises the DP to the estimated ingredient cost,
	// so a customer who disappears has paid for what was bought.
	DPCoversIngredientCost bool
}

// ErrInvalidAmount means an amount is negative or absurdly large.
var ErrInvalidAmount = errors.New("invalid amount")

// Validate checks the policy.
func (p Policy) Validate() error {
	if p.DPMinPercent < 1 || p.DPMinPercent > 100 {
		return fmt.Errorf("dp_min_percent must be 1 to 100, got %d", p.DPMinPercent)
	}
	return nil
}

// DPRequired is the down payment locked at checkout (§14):
//
//	min(total, max(ceil(total × percent / 100), ingredient cost))
//
// The ingredient cost counts only when the policy says so. The DP never
// exceeds the total, even for a cake sold below its ingredient cost.
func DPRequired(p Policy, totalIDR, ingredientCostIDR int64) (int64, error) {
	if err := p.Validate(); err != nil {
		return 0, err
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
