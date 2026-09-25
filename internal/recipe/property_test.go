package recipe_test

import (
	"fmt"
	"math"
	"strconv"
	"testing"

	"pgregory.net/rapid"

	"github.com/Zefanrakh/kue-preorder/internal/recipe"
)

// Property tests for the invariants of docs/architecture.md §10, over
// randomly drawn valid models. rapid shrinks any failure to a minimal case.

func num(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) }

// drawTyped draws a coefficient in [0, maxValue] with at most 4 decimals, the
// way a person types one into a formula. (Arbitrary floats such as 3.7e-150
// print as hundreds of digits and hit the formula length limit, as they
// should; the other models are drawn from unrestricted floats.)
func drawTyped(t *rapid.T, label string, maxValue int) float64 {
	return float64(rapid.IntRange(0, maxValue*10_000).Draw(t, label)) / 10_000
}

func drawAffine(t *rapid.T) recipe.Model {
	m, err := recipe.NewAffine(rapid.Float64Range(0, 1000).Draw(t, "a"), rapid.Float64Range(0, 1000).Draw(t, "b"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func drawPower(t *rapid.T, minB, maxB float64) recipe.Model {
	m, err := recipe.NewPower(rapid.Float64Range(0, 1000).Draw(t, "a"), rapid.Float64Range(minB, maxB).Draw(t, "b"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func drawPiecewise(t *rapid.T) recipe.Model {
	n := rapid.IntRange(1, 8).Draw(t, "points")
	points := make([]recipe.Point, n)
	u, amount := 0.0, 0.0
	for i := range points {
		u += rapid.Float64Range(0.1, 20).Draw(t, "du")
		amount += rapid.Float64Range(0, 500).Draw(t, "da")
		points[i] = recipe.Point{U: u, Amount: amount}
	}
	m, err := recipe.NewPiecewise(points)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// drawFormula draws from formulas that never decrease for u > 0.
func drawFormula(t *rapid.T) recipe.Model {
	a, b := num(drawTyped(t, "a", 1000)), num(drawTyped(t, "b", 1000))
	p := num(drawTyped(t, "p", 2))
	expr := rapid.SampledFrom([]string{
		a + " + " + b + "*u",
		a + " * u ** " + p,
		"max(" + a + ", " + b + "*u)",
		a + " * ceil(u)",
		a + " + " + b + "*floor(u)",
		"min(" + a + "*u, " + b + " + " + a + "*u)",
	}).Draw(t, "expr")
	m, err := recipe.NewFormula(expr)
	if err != nil {
		t.Fatalf("NewFormula(%q) error = %v", expr, err)
	}
	return m
}

func drawModel(t *rapid.T) recipe.Model {
	switch rapid.SampledFrom([]recipe.ModelType{recipe.Affine, recipe.Power, recipe.Piecewise, recipe.Formula}).Draw(t, "type") {
	case recipe.Affine:
		return drawAffine(t)
	case recipe.Power:
		return drawPower(t, 0, 2)
	case recipe.Piecewise:
		return drawPiecewise(t)
	default:
		return drawFormula(t)
	}
}

func resolve(t *rapid.T, m recipe.Model, u float64) float64 {
	v, err := m.Resolve(u)
	if err != nil {
		t.Fatalf("%s: Resolve(%v) error = %v", m.Type(), u, err)
	}
	return v
}

func tolerance(x float64) float64 { return 1e-9 * math.Max(1, math.Abs(x)) }

func TestProperty_ZeroUnitsNeedNothing(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		if got := resolve(t, drawModel(t), 0); got != 0 {
			t.Fatalf("Resolve(0) = %v, want 0", got)
		}
	})
}

func TestProperty_AmountsAreFiniteAndNonNegative(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		m := drawModel(t)
		u := rapid.Float64Range(0, 1e4).Draw(t, "u")
		if v := resolve(t, m, u); math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			t.Fatalf("Resolve(%v) = %v, want finite and >= 0", u, v)
		}
	})
}

func TestProperty_MoreUnitsNeverNeedLess(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		m := drawModel(t)
		u1 := rapid.Float64Range(0, 1e4).Draw(t, "u1")
		u2 := u1 + rapid.Float64Range(0, 1e3).Draw(t, "du")
		v1, v2 := resolve(t, m, u1), resolve(t, m, u2)
		if v2 < v1-tolerance(v1) {
			t.Fatalf("Resolve(%v) = %v < Resolve(%v) = %v", u2, v2, u1, v1)
		}
	})
}

// §10: for power with b < 1, Resolve(k) <= k * Resolve(1). It holds for k >= 1;
// below 1 the inequality is reversed by definition.
func TestProperty_PowerBelowOneScalesSublinearly(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		m := drawPower(t, 0.01, 0.999)
		k := rapid.Float64Range(1, 1000).Draw(t, "k")
		one, many := resolve(t, m, 1), resolve(t, m, k)
		if many > k*one+tolerance(k*one) {
			t.Fatalf("Resolve(%v) = %v > %v * Resolve(1) = %v", k, many, k, k*one)
		}
	})
}

func TestProperty_ValidateAcceptsWellFormedModels(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		m := drawModel(t)
		if err := recipe.Validate(m); err != nil {
			t.Fatalf("Validate(%s) = %v", m.Type(), err)
		}
	})
}

func TestProperty_FormulaAgreesWithClosedForms(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := drawTyped(t, "a", 1000)
		b := drawTyped(t, "b", 2)
		u := rapid.Float64Range(0, 1e3).Draw(t, "u")
		for _, pair := range []struct {
			formula string
			closed  recipe.Model
		}{
			{fmt.Sprintf("%s + %s*u", num(a), num(b)), recipe.AffineModel{A: a, B: b}},
			{fmt.Sprintf("%s * u ** %s", num(a), num(b)), recipe.PowerModel{A: a, B: b}},
		} {
			f, err := recipe.NewFormula(pair.formula)
			if err != nil {
				t.Fatal(err)
			}
			got, want := resolve(t, f, u), resolve(t, pair.closed, u)
			if math.Abs(got-want) > tolerance(want) {
				t.Fatalf("%q at u=%v = %v, %s = %v", pair.formula, u, got, pair.closed.Type(), want)
			}
		}
	})
}

func TestProperty_QuantityRoundsWithinOneUnit(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		m := drawModel(t)
		u := rapid.Float64Range(0, 1e3).Draw(t, "u")
		waste := rapid.Float64Range(1, 1.5).Draw(t, "waste")
		x := resolve(t, m, u) * waste

		nearest, err := recipe.Quantity(m, u, waste, recipe.RoundNearest)
		if err != nil {
			t.Fatal(err)
		}
		up, err := recipe.Quantity(m, u, waste, recipe.RoundUp)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(float64(nearest)-x) > 0.5+tolerance(x) {
			t.Fatalf("RoundNearest(%v) = %d", x, nearest)
		}
		if float64(up) < x-tolerance(x) || float64(up) >= x+1 {
			t.Fatalf("RoundUp(%v) = %d", x, up)
		}
		if up < nearest {
			t.Fatalf("RoundUp = %d < RoundNearest = %d", up, nearest)
		}
	})
}

func TestProperty_QuantityNeverDecreasesWithUnits(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		m := drawModel(t)
		u1 := rapid.Float64Range(0, 1e3).Draw(t, "u1")
		u2 := u1 + rapid.Float64Range(0, 100).Draw(t, "du")
		r := rapid.SampledFrom([]recipe.Rounding{recipe.RoundNearest, recipe.RoundUp}).Draw(t, "rounding")
		q1, err1 := recipe.Quantity(m, u1, 1, r)
		q2, err2 := recipe.Quantity(m, u2, 1, r)
		if err1 != nil || err2 != nil {
			t.Fatal(err1, err2)
		}
		if q2 < q1 {
			t.Fatalf("Quantity(%v) = %d < Quantity(%v) = %d", u2, q2, u1, q1)
		}
	})
}
