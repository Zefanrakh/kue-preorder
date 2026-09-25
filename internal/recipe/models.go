package recipe

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
)

// AffineModel is a + b*u: a fixed part (greasing a pan) plus a part per unit.
type AffineModel struct {
	A, B float64
}

// NewAffine returns a + b*u. Both parameters must be finite and >= 0.
func NewAffine(a, b float64) (AffineModel, error) {
	if err := errors.Join(nonNegative("a", a), nonNegative("b", b)); err != nil {
		return AffineModel{}, err
	}
	return AffineModel{A: a, B: b}, nil
}

// Type implements Model.
func (AffineModel) Type() ModelType { return Affine }

// Resolve implements Model.
func (m AffineModel) Resolve(u float64) (float64, error) {
	return evaluate(u, func(u float64) (float64, error) { return m.A + m.B*u, nil })
}

// PowerModel is a * u^b. With b < 1 a larger batch needs less per unit, the
// saving the recipe engine exists to capture.
type PowerModel struct {
	A, B float64
}

// NewPower returns a * u^b. Both parameters must be finite and >= 0.
func NewPower(a, b float64) (PowerModel, error) {
	if err := errors.Join(nonNegative("a", a), nonNegative("b", b)); err != nil {
		return PowerModel{}, err
	}
	return PowerModel{A: a, B: b}, nil
}

// Type implements Model.
func (PowerModel) Type() ModelType { return Power }

// Resolve implements Model.
func (m PowerModel) Resolve(u float64) (float64, error) {
	return evaluate(u, func(u float64) (float64, error) { return m.A * math.Pow(u, m.B), nil })
}

// Point is one measurement: Amount of the ingredient used for U units.
type Point struct {
	U, Amount float64
}

// PiecewiseModel interpolates linearly between measured points, starting from
// (0, 0). Above the last point it continues the last segment's slope, because
// a batch can be larger than anything measured.
type PiecewiseModel struct {
	points []Point // with the implicit (0, 0) first
}

// NewPiecewise returns the model through points. There must be at least one;
// U must be > 0 and strictly increasing, Amount >= 0 and never decreasing.
func NewPiecewise(points []Point) (PiecewiseModel, error) {
	if len(points) == 0 {
		return PiecewiseModel{}, fmt.Errorf("%w: piecewise needs at least one point", ErrInvalidParams)
	}
	prev := Point{}
	for i, p := range points {
		switch {
		case !isFinite(p.U) || !isFinite(p.Amount):
			return PiecewiseModel{}, fmt.Errorf("%w: point %d is not finite", ErrInvalidParams, i+1)
		case p.U <= prev.U:
			return PiecewiseModel{}, fmt.Errorf("%w: point %d: units must be > 0 and increase (got %v after %v)", ErrInvalidParams, i+1, p.U, prev.U)
		case p.Amount < prev.Amount:
			return PiecewiseModel{}, fmt.Errorf("%w: point %d: amount must not decrease (got %v after %v)", ErrInvalidParams, i+1, p.Amount, prev.Amount)
		}
		prev = p
	}
	return PiecewiseModel{points: append([]Point{{}}, points...)}, nil
}

// Type implements Model.
func (PiecewiseModel) Type() ModelType { return Piecewise }

// Points returns the measured points, without the implicit (0, 0).
func (m PiecewiseModel) Points() []Point {
	return slices.Clone(m.points[1:])
}

// Resolve implements Model.
func (m PiecewiseModel) Resolve(u float64) (float64, error) {
	return evaluate(u, func(u float64) (float64, error) {
		// The segment [points[i-1], points[i]] holding u; past the end, the last one.
		i := sort.Search(len(m.points), func(i int) bool { return m.points[i].U >= u })
		i = min(max(i, 1), len(m.points)-1)
		lo, hi := m.points[i-1], m.points[i]
		slope := (hi.Amount - lo.Amount) / (hi.U - lo.U)
		return lo.Amount + slope*(u-lo.U), nil
	})
}
