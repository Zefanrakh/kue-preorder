package recipe

import (
	"errors"
	"fmt"
	"math"
)

// Rounding says how a component's amount becomes whole units of the
// ingredient's base unit.
type Rounding int

const (
	// RoundNearest rounds half away from zero; for grams and millilitres.
	RoundNearest Rounding = iota
	// RoundUp rounds up; for pieces, since one dough cannot use part of an egg.
	RoundUp
)

// maxExactAmount is the largest amount a float64 still holds exactly (2^53).
const maxExactAmount = 1 << 53

// ErrInvalidWasteFactor means a waste factor below 1, NaN, or infinite.
var ErrInvalidWasteFactor = errors.New("waste factor must be a finite number >= 1")

// Quantity applies §10's precision rule to one component and one ingredient:
// resolve in float64, multiply by the waste factor, then round to whole units.
// Callers sum the results across components as int64 and round to pack size
// last.
func Quantity(m Model, u, wasteFactor float64, r Rounding) (int64, error) {
	if !isFinite(wasteFactor) || wasteFactor < 1 {
		return 0, fmt.Errorf("%w: got %v", ErrInvalidWasteFactor, wasteFactor)
	}
	amount, err := m.Resolve(u)
	if err != nil {
		return 0, err
	}
	x := amount * wasteFactor

	var rounded float64
	switch r {
	case RoundNearest:
		rounded = math.Round(x)
	case RoundUp:
		// Float arithmetic can turn exactly 3 eggs into 3.0000000000000004;
		// that is still 3, not 4.
		rounded = math.Ceil(x - slack(x))
	default:
		return 0, fmt.Errorf("unknown rounding %d", r)
	}
	if rounded > maxExactAmount {
		return 0, fmt.Errorf("%w: %v is too large", ErrInvalidResult, x)
	}
	return int64(rounded), nil
}
