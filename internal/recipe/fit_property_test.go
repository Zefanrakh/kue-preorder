package recipe_test

import (
	"errors"
	"math"
	"testing"

	"pgregory.net/rapid"

	"github.com/Zefanrakh/kue-preorder/internal/recipe"
)

// drawUnits draws n >= 2 distinct, increasing unit counts in (0, ~200].
func drawUnits(t *rapid.T) []float64 {
	n := rapid.IntRange(2, 10).Draw(t, "n")
	units := make([]float64, n)
	u := 0.0
	for i := range units {
		u += rapid.Float64Range(0.1, 20).Draw(t, "du")
		units[i] = u
	}
	return units
}

// drawRisingPoints draws measurements whose amounts never fall.
func drawRisingPoints(t *rapid.T, minStep float64) []recipe.Point {
	units := drawUnits(t)
	points := make([]recipe.Point, len(units))
	amount := rapid.Float64Range(minStep, 500).Draw(t, "first")
	for i, u := range units {
		if i > 0 {
			amount += rapid.Float64Range(0, 500).Draw(t, "da")
		}
		points[i] = recipe.Point{U: u, Amount: amount}
	}
	return points
}

func closeTo(got, want, scale float64) bool {
	return math.Abs(got-want) <= 1e-6*math.Max(1, scale)
}

func TestProperty_FitAffineRecoversExactLines(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := rapid.Float64Range(0, 1000).Draw(t, "a")
		b := rapid.Float64Range(0.01, 1000).Draw(t, "b")
		units := drawUnits(t)
		points := make([]recipe.Point, len(units))
		for i, u := range units {
			points[i] = recipe.Point{U: u, Amount: a + b*u}
		}

		m, report, err := recipe.FitAffine(points)

		if err != nil {
			t.Fatalf("FitAffine() error = %v", err)
		}
		scale := a + b*units[len(units)-1]
		if !closeTo(m.A, a, scale) || !closeTo(m.B, b, scale) {
			t.Fatalf("FitAffine() = %+v, want a=%v b=%v", m, a, b)
		}
		if report.R2 < 1-1e-9 {
			t.Fatalf("R2 = %v for exact data", report.R2)
		}
	})
}

func TestProperty_FitPowerRecoversExactCurves(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := rapid.Float64Range(0.1, 1000).Draw(t, "a")
		b := rapid.Float64Range(0.01, 2).Draw(t, "b")
		units := drawUnits(t)
		points := make([]recipe.Point, len(units))
		for i, u := range units {
			points[i] = recipe.Point{U: u, Amount: a * math.Pow(u, b)}
		}

		m, _, err := recipe.FitPower(points)

		if err != nil {
			t.Fatalf("FitPower() error = %v", err)
		}
		if !closeTo(m.A, a, a) || !closeTo(m.B, b, 1) {
			t.Fatalf("FitPower() = %+v, want a=%v b=%v", m, a, b)
		}
	})
}

// A fit never returns a model that Validate would refuse to save. On
// measurements that never fall, affine and piecewise always fit; power may
// refuse (say, a jump so steep the curve overflows) but only with ErrFit.
func TestProperty_FitsOfRisingDataAreValidModels(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		points := drawRisingPoints(t, 0.01)
		for _, r := range recipe.FitAll(points) {
			if r.Err != nil {
				if r.Type == recipe.Power && errors.Is(r.Err, recipe.ErrFit) && r.Model == nil {
					continue
				}
				t.Fatalf("%s: fit error = %v on rising data %v", r.Type, r.Err, points)
			}
			if err := recipe.Validate(r.Model); err != nil {
				t.Fatalf("%s: fitted model fails Validate: %v", r.Type, err)
			}
			if r.Report.R2 > 1+1e-9 {
				t.Fatalf("%s: R2 = %v > 1", r.Type, r.Report.R2)
			}
		}
	})
}

func TestProperty_FitPiecewisePassesThroughEveryPoint(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		points := drawRisingPoints(t, 0)

		m, report, err := recipe.FitPiecewise(points)

		if err != nil {
			t.Fatalf("FitPiecewise() error = %v", err)
		}
		if report.MaxAbsError > tolerance(points[len(points)-1].Amount) {
			t.Fatalf("MaxAbsError = %v, want 0 through distinct points", report.MaxAbsError)
		}
		if got := len(m.Points()); got != len(points) {
			t.Fatalf("model has %d points, want %d", got, len(points))
		}
	})
}

func TestProperty_ParamsRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		m := drawModel(t)
		params, err := recipe.Params(m)
		if err != nil {
			t.Fatalf("Params() error = %v", err)
		}
		back, err := recipe.Build(m.Type(), params)
		if err != nil {
			t.Fatalf("Build(Params()) error = %v for %s", err, params)
		}
		u := rapid.Float64Range(0, 1e3).Draw(t, "u")
		if got, want := resolve(t, back, u), resolve(t, m, u); got != want {
			t.Fatalf("Resolve(%v) = %v after the round trip, want %v", u, got, want)
		}
	})
}
