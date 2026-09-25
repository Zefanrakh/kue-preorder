// Package recipe is the pure recipe engine (docs/architecture.md §10). A Model
// turns units of one component (dough, filling, topping) into the amount of
// one ingredient. The package has no database or HTTP and imports nothing
// from internal/, so it can be tested exhaustively on its own.
//
// Every model shares one contract, enforced by evaluate rather than per model:
//   - u must be finite and >= 0; it may be fractional (1.5 pans);
//   - Resolve(0) == 0: no units, no ingredient, even when an affine model has
//     a fixed part;
//   - every result is finite and >= 0.
//
// More units never need less of an ingredient. Affine, power, and piecewise
// models hold this by construction; formulas are checked by Validate before a
// recipe is saved.
package recipe

import (
	"errors"
	"fmt"
	"math"
)

// ModelType names how a component's ingredient amount scales with its units.
type ModelType string

// Model types, stored in component_ingredients.model_type.
const (
	Affine    ModelType = "affine"    // a + b*u
	Power     ModelType = "power"     // a * u^b
	Piecewise ModelType = "piecewise" // linear interpolation between measured points
	Formula   ModelType = "formula"   // a free expression of u
)

// Model computes the amount of one ingredient for u units of a component,
// before the waste factor.
type Model interface {
	Resolve(u float64) (float64, error)
	Type() ModelType
}

var (
	// ErrInvalidParams means stored parameters do not describe a valid model.
	ErrInvalidParams = errors.New("invalid recipe parameters")
	// ErrInvalidUnits means u is negative, NaN, or infinite.
	ErrInvalidUnits = errors.New("component units must be a finite number >= 0")
	// ErrInvalidResult means a model produced a negative, NaN, or infinite amount.
	ErrInvalidResult = errors.New("recipe amount must be a finite number >= 0")
	// ErrDecreasing means more units produced a smaller amount.
	ErrDecreasing = errors.New("recipe amount decreases as units grow")
)

// evaluate applies the shared contract around f, which only sees u > 0.
func evaluate(u float64, f func(u float64) (float64, error)) (float64, error) {
	if !isFinite(u) || u < 0 {
		return 0, fmt.Errorf("%w: got %v", ErrInvalidUnits, u)
	}
	if u == 0 {
		return 0, nil
	}
	v, err := f(u)
	if err != nil {
		return 0, err
	}
	if !isFinite(v) || v < 0 {
		return 0, fmt.Errorf("%w: got %v at u=%v", ErrInvalidResult, v, u)
	}
	return v, nil
}

func isFinite(x float64) bool {
	return !math.IsNaN(x) && !math.IsInf(x, 0)
}

// nonNegative checks a model parameter.
func nonNegative(name string, x float64) error {
	if !isFinite(x) || x < 0 {
		return fmt.Errorf("%w: %s must be a finite number >= 0, got %v", ErrInvalidParams, name, x)
	}
	return nil
}
