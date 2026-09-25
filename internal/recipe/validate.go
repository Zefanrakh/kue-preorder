package recipe

import (
	"fmt"
	"math"
)

// The range §10 checks before a recipe is saved: u = 0.5..100.
const (
	validateStep  = 0.5
	validateSteps = 200 // 0.5, 1.0, ..., 100
)

// Validate checks model over u = 0.5..100 in steps of 0.5: every amount must
// be finite and >= 0, and never smaller than the one before. The CMS runs it
// before saving a recipe line, so a bad formula is caught at input rather
// than when a batch is computed. Affine, power, and piecewise models pass by
// construction.
//
// A check at sample points can miss a dip that starts and ends between two
// samples; at runtime such a formula still yields finite amounts >= 0.
func Validate(m Model) error {
	prev := 0.0 // Resolve(0)
	for i := 1; i <= validateSteps; i++ {
		u := float64(i) * validateStep
		v, err := m.Resolve(u)
		if err != nil {
			return fmt.Errorf("at u=%v: %w", u, err)
		}
		if v < prev-slack(prev) {
			return fmt.Errorf("%w: %v at u=%v, then %v at u=%v", ErrDecreasing, prev, u-validateStep, v, u)
		}
		prev = v
	}
	return nil
}

// slack absorbs float rounding noise when comparing amounts near x.
func slack(x float64) float64 {
	return 1e-9 * math.Max(1, math.Abs(x))
}
