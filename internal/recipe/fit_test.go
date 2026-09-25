package recipe_test

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/Zefanrakh/kue-preorder/internal/recipe"
)

// The measured points of docs/architecture.md §10.
var docPoints = []recipe.Point{{U: 1, Amount: 100}, {U: 2, Amount: 190}, {U: 4, Amount: 360}}

func TestFitAffine_DocumentedPoints(t *testing.T) {
	m, report, err := recipe.FitAffine(docPoints)

	if err != nil {
		t.Fatalf("FitAffine() error = %v", err)
	}
	// Worked by hand: slope 3630/42 = 605/7, intercept 650/3 - 605/3 = 15.
	if !near(m.A, 15) || !near(m.B, 605.0/7) {
		t.Errorf("FitAffine() = %+v, want a=15, b=605/7", m)
	}
	if report.R2 <= 0.99 || report.R2 > 1 {
		t.Errorf("R2 = %v, want close to 1", report.R2)
	}
	if len(report.Residuals) != len(docPoints) {
		t.Errorf("residuals = %v, want one per point", report.Residuals)
	}
}

func TestFitPower_DocumentedPoints(t *testing.T) {
	m, report, err := recipe.FitPower(docPoints)

	if err != nil {
		t.Fatalf("FitPower() error = %v", err)
	}
	// §10 lists {"a": 100, "b": 0.926} for the same recipe.
	if math.Abs(m.A-100) > 1 || math.Abs(m.B-0.926) > 0.01 {
		t.Errorf("FitPower() = %+v, want about a=100, b=0.926", m)
	}
	if report.R2 < 0.999 {
		t.Errorf("R2 = %v, want > 0.999 for points this close to a power law", report.R2)
	}
	if report.MaxRelError > 0.01 {
		t.Errorf("MaxRelError = %v, want < 1%%", report.MaxRelError)
	}
}

func TestFitPiecewise_PassesThroughPointsAndAveragesRepeats(t *testing.T) {
	points := []recipe.Point{{U: 2, Amount: 190}, {U: 1, Amount: 100}, {U: 1, Amount: 110}}

	m, report, err := recipe.FitPiecewise(points)

	if err != nil {
		t.Fatalf("FitPiecewise() error = %v", err)
	}
	want := []recipe.Point{{U: 1, Amount: 105}, {U: 2, Amount: 190}}
	if got := m.Points(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Points() = %v, want sorted with repeats averaged: %v", got, want)
	}
	if !near(report.MaxAbsError, 5) {
		t.Errorf("MaxAbsError = %v, want 5 (each repeat is 5 from their mean)", report.MaxAbsError)
	}
}

func TestFitAffine_NegativeInterceptFitsThroughOrigin(t *testing.T) {
	// The best line is -80 + 90u; a recipe cannot have a negative fixed part.
	points := []recipe.Point{{U: 1, Amount: 10}, {U: 2, Amount: 100}, {U: 3, Amount: 190}}

	m, _, err := recipe.FitAffine(points)

	if err != nil {
		t.Fatalf("FitAffine() error = %v", err)
	}
	if m.A != 0 || !near(m.B, 780.0/14) {
		t.Errorf("FitAffine() = %+v, want a=0, b=780/14", m)
	}
}

func TestFitAffine_SingleUnitCountFitsThroughOrigin(t *testing.T) {
	m, _, err := recipe.FitAffine([]recipe.Point{{U: 2, Amount: 180}, {U: 2, Amount: 190}})

	if err != nil {
		t.Fatalf("FitAffine() error = %v", err)
	}
	if m.A != 0 || !near(m.B, 92.5) {
		t.Errorf("FitAffine() = %+v, want a=0, b=92.5 (mean 185 per 2 units)", m)
	}
}

func TestFitReport_EqualAmounts(t *testing.T) {
	_, exact, err := recipe.FitAffine([]recipe.Point{{U: 1, Amount: 50}, {U: 2, Amount: 50}})
	if err != nil {
		t.Fatal(err)
	}
	if exact.R2 != 1 {
		t.Errorf("R2 of an exact fit to equal amounts = %v, want 1", exact.R2)
	}

	_, off, err := recipe.FitPower([]recipe.Point{{U: 1, Amount: 50}, {U: 1, Amount: 50}, {U: 4, Amount: 50}, {U: 4, Amount: 50}})
	if err != nil {
		t.Fatal(err)
	}
	if off.R2 != 1 || off.MaxRelError > 1e-9 {
		t.Errorf("flat data: R2 = %v, MaxRelError = %v; want 1 and 0", off.R2, off.MaxRelError)
	}
}

func TestFit_RejectsUnusablePoints(t *testing.T) {
	falling := []recipe.Point{{U: 1, Amount: 100}, {U: 2, Amount: 50}}
	tests := []struct {
		name    string
		fit     func([]recipe.Point) error
		points  []recipe.Point
		wantMsg string
	}{
		{"affine, no points", fitAffine, nil, "no measured points"},
		{"affine, too many points", fitAffine, rising(recipe.MaxPoints + 1), "at most 100"},
		{"power, too many points", fitPower, rising(recipe.MaxPoints + 1), "at most 100"},
		{"piecewise, too many points", fitPiecewise, rising(recipe.MaxPoints + 1), "at most 100"},
		{"affine, zero units", fitAffine, []recipe.Point{{U: 0, Amount: 10}}, "point 1"},
		{"affine, negative amount", fitAffine, []recipe.Point{{U: 1, Amount: -1}}, "point 1"},
		{"affine, NaN", fitAffine, []recipe.Point{{U: math.NaN(), Amount: 1}}, "point 1"},
		{"affine, falling amounts", fitAffine, falling, "fall"},
		{"power, zero amount", fitPower, []recipe.Point{{U: 1, Amount: 0}, {U: 2, Amount: 10}}, "every amount > 0"},
		{"power, one unit count", fitPower, []recipe.Point{{U: 2, Amount: 10}, {U: 2, Amount: 11}}, "two different unit counts"},
		{"power, falling amounts", fitPower, falling, "fall"},
		{"power, bad point", fitPower, []recipe.Point{{U: -1, Amount: 10}}, "point 1"},
		// Found by the property test: a 500x jump between 12.5 and 13 units
		// fits b of about 158, which overflows well before u = 100.
		{"power, curve too steep to use", fitPower, []recipe.Point{{U: 12.5, Amount: 1}, {U: 13, Amount: 501}}, "unusable"},
		// Absurd magnitudes (a typo with a dozen extra zeros) must fail cleanly.
		{"affine, line overflows before u=100", fitAffine, []recipe.Point{{U: 1, Amount: 1e306}, {U: 2, Amount: 1e307}}, "unusable"},
		{"affine, amounts too large to average", fitAffine, []recipe.Point{{U: 1, Amount: 1.7e308}, {U: 2, Amount: 1.7e308}}, "finite"},
		{"power, intercept overflows", fitPower, []recipe.Point{{U: 0.5, Amount: 1e308}, {U: 0.6, Amount: 1.5e308}}, "finite"},
		{"piecewise, slope overflows", fitPiecewise, []recipe.Point{{U: 0.5, Amount: 0}, {U: 0.6, Amount: 1.7e308}}, "unusable"},
		{"piecewise, falling amounts", fitPiecewise, falling, "u=2"},
		{"piecewise, bad point", fitPiecewise, []recipe.Point{{U: 1, Amount: math.Inf(1)}}, "point 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.fit(tt.points)
			if !errors.Is(err, recipe.ErrFit) || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error = %v, want ErrFit mentioning %q", err, tt.wantMsg)
			}
		})
	}
}

// rising returns n points on the line 10u, one per unit.
func rising(n int) []recipe.Point {
	points := make([]recipe.Point, n)
	for i := range points {
		points[i] = recipe.Point{U: float64(i + 1), Amount: float64(10 * (i + 1))}
	}
	return points
}

func fitAffine(p []recipe.Point) error    { _, _, err := recipe.FitAffine(p); return err }
func fitPower(p []recipe.Point) error     { _, _, err := recipe.FitPower(p); return err }
func fitPiecewise(p []recipe.Point) error { _, _, err := recipe.FitPiecewise(p); return err }

func TestFitAll(t *testing.T) {
	results := recipe.FitAll([]recipe.Point{{U: 1, Amount: 0}, {U: 2, Amount: 10}})

	if len(results) != 3 {
		t.Fatalf("FitAll() returned %d results, want 3", len(results))
	}
	byType := map[recipe.ModelType]recipe.FitResult{}
	for _, r := range results {
		byType[r.Type] = r
	}
	if r := byType[recipe.Affine]; r.Err != nil || r.Model == nil {
		t.Errorf("affine = %+v, want fitted", r)
	}
	if r := byType[recipe.Power]; !errors.Is(r.Err, recipe.ErrFit) || r.Model != nil {
		t.Errorf("power = %+v, want ErrFit (an amount is 0) and no model", r)
	}
	if r := byType[recipe.Piecewise]; r.Err != nil || r.Model.Type() != recipe.Piecewise {
		t.Errorf("piecewise = %+v, want fitted", r)
	}
}

func TestParams_RoundTripsThroughBuild(t *testing.T) {
	models := []recipe.Model{
		recipe.AffineModel{A: 10, B: 90},
		recipe.PowerModel{A: 100, B: 0.926},
		mustBuild(t, recipe.Piecewise, pointsJSON),
		mustBuild(t, recipe.Formula, `{"expr":"max(50, 40*u)"}`),
	}
	for _, m := range models {
		t.Run(string(m.Type()), func(t *testing.T) {
			params, err := recipe.Params(m)
			if err != nil {
				t.Fatalf("Params() error = %v", err)
			}
			back := mustBuild(t, m.Type(), string(params))
			for _, u := range []float64{0, 0.5, 1, 2.5, 7} {
				want, _ := m.Resolve(u)
				got, _ := back.Resolve(u)
				if got != want {
					t.Errorf("after the round trip Resolve(%v) = %v, want %v (params %s)", u, got, want, params)
				}
			}
		})
	}
}

type otherModel struct{ recipe.AffineModel }

func TestParams_RejectsUnknownModel(t *testing.T) {
	if _, err := recipe.Params(otherModel{}); !errors.Is(err, recipe.ErrInvalidParams) {
		t.Errorf("Params(unknown) error = %v, want ErrInvalidParams", err)
	}
}
