package recipe_test

import (
	"errors"
	"math"
	"testing"

	"github.com/Zefanrakh/kue-preorder/internal/recipe"
)

const pointsJSON = `{"points":[[1,100],[2,190],[4,360]]}`

func mustBuild(t *testing.T, typ recipe.ModelType, params string) recipe.Model {
	t.Helper()
	m, err := recipe.Build(typ, []byte(params))
	if err != nil {
		t.Fatalf("Build(%s, %s) error = %v", typ, params, err)
	}
	return m
}

func near(got, want float64) bool {
	return math.Abs(got-want) <= 1e-9*math.Max(1, math.Abs(want))
}

// The examples of docs/architecture.md §10: one recipe written four ways.
func TestResolve_DocumentedExamples(t *testing.T) {
	tests := []struct {
		name   string
		typ    recipe.ModelType
		params string
		u      float64
		want   float64
	}{
		{"affine, one unit", recipe.Affine, `{"a":10,"b":90}`, 1, 100},
		{"affine, two units", recipe.Affine, `{"a":10,"b":90}`, 2, 190},
		{"affine, fractional units", recipe.Affine, `{"a":10,"b":90}`, 1.5, 145},
		{"power, one unit", recipe.Power, `{"a":100,"b":0.926}`, 1, 100},
		{"power, two units", recipe.Power, `{"a":100,"b":0.926}`, 2, 100 * math.Pow(2, 0.926)},
		{"piecewise, below the first point from (0,0)", recipe.Piecewise, pointsJSON, 0.5, 50},
		{"piecewise, on a point", recipe.Piecewise, pointsJSON, 2, 190},
		{"piecewise, between points", recipe.Piecewise, pointsJSON, 3, 275},
		{"piecewise, beyond the last point keeps its slope", recipe.Piecewise, pointsJSON, 6, 530},
		{"formula", recipe.Formula, `{"expr":"10 + 90*u"}`, 2, 190},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := mustBuild(t, tt.typ, tt.params)

			got, err := m.Resolve(tt.u)

			if err != nil {
				t.Fatalf("Resolve(%v) error = %v", tt.u, err)
			}
			if !near(got, tt.want) {
				t.Errorf("Resolve(%v) = %v, want %v", tt.u, got, tt.want)
			}
			if m.Type() != tt.typ {
				t.Errorf("Type() = %q, want %q", m.Type(), tt.typ)
			}
		})
	}
}

func TestResolve_ZeroUnitsNeedNothing(t *testing.T) {
	for _, m := range []recipe.Model{
		mustBuild(t, recipe.Affine, `{"a":10,"b":90}`), // the fixed part too
		mustBuild(t, recipe.Power, `{"a":100,"b":0.926}`),
		mustBuild(t, recipe.Piecewise, pointsJSON),
		mustBuild(t, recipe.Formula, `{"expr":"10 + 90*u"}`),
	} {
		if got, err := m.Resolve(0); err != nil || got != 0 {
			t.Errorf("%s: Resolve(0) = %v, %v; want 0", m.Type(), got, err)
		}
	}
}

func TestResolve_RejectsInvalidUnits(t *testing.T) {
	m := mustBuild(t, recipe.Affine, `{"a":10,"b":90}`)
	for _, u := range []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := m.Resolve(u); !errors.Is(err, recipe.ErrInvalidUnits) {
			t.Errorf("Resolve(%v) error = %v, want ErrInvalidUnits", u, err)
		}
	}
}

// §11: 6 chocolate and 6 cheese donuts share one dough, resolved once for
// u = 12. With b < 1 that needs less than two separate doughs of 6.
func TestPower_SharedDoughNeedsLessThanSeparateDoughs(t *testing.T) {
	dough := mustBuild(t, recipe.Power, `{"a":100,"b":0.926}`)

	shared, err := dough.Resolve(12)
	if err != nil {
		t.Fatal(err)
	}
	separate, err := dough.Resolve(6)
	if err != nil {
		t.Fatal(err)
	}
	if shared >= 2*separate {
		t.Errorf("Resolve(12) = %v, want less than 2*Resolve(6) = %v", shared, 2*separate)
	}
}

func TestBuild_RejectsInvalidParams(t *testing.T) {
	tests := []struct {
		name   string
		typ    recipe.ModelType
		params string
	}{
		{"unknown model type", "linear", `{"a":1,"b":2}`},
		{"not JSON", recipe.Affine, `nope`},
		{"affine without b", recipe.Affine, `{"a":10}`},
		{"affine with negative a", recipe.Affine, `{"a":-1,"b":2}`},
		{"affine with an unknown field", recipe.Affine, `{"a":1,"b":2,"c":3}`},
		{"affine with trailing data", recipe.Affine, `{"a":1,"b":2} {}`},
		{"power with negative b", recipe.Power, `{"a":100,"b":-0.5}`},
		{"power without a", recipe.Power, `{"b":0.9}`},
		{"piecewise without points", recipe.Piecewise, `{}`},
		{"piecewise points not a list", recipe.Piecewise, `{"points":"1,100"}`},
		{"formula expr not a string", recipe.Formula, `{"expr":42}`},
		{"piecewise with no points", recipe.Piecewise, `{"points":[]}`},
		{"piecewise point with three values", recipe.Piecewise, `{"points":[[1,2,3]]}`},
		{"piecewise point at zero units", recipe.Piecewise, `{"points":[[0,10]]}`},
		{"piecewise units not increasing", recipe.Piecewise, `{"points":[[2,10],[2,20]]}`},
		{"piecewise amount decreasing", recipe.Piecewise, `{"points":[[1,100],[2,90]]}`},
		{"piecewise negative amount", recipe.Piecewise, `{"points":[[1,-5]]}`},
		{"formula without expr", recipe.Formula, `{}`},
		{"formula with an unknown field", recipe.Formula, `{"expr":"u","x":1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := recipe.Build(tt.typ, []byte(tt.params))
			if !errors.Is(err, recipe.ErrInvalidParams) {
				t.Errorf("Build() = %v, %v; want ErrInvalidParams", m, err)
			}
		})
	}
}

func TestNewPiecewise_RejectsNonFinitePoints(t *testing.T) {
	for _, p := range []recipe.Point{{U: math.NaN(), Amount: 1}, {U: 1, Amount: math.Inf(1)}} {
		if _, err := recipe.NewPiecewise([]recipe.Point{p}); !errors.Is(err, recipe.ErrInvalidParams) {
			t.Errorf("NewPiecewise(%v) error = %v, want ErrInvalidParams", p, err)
		}
	}
}

func TestNewPiecewise_BoundsThePointCount(t *testing.T) {
	if _, err := recipe.NewPiecewise(rising(recipe.MaxPoints)); err != nil {
		t.Errorf("NewPiecewise(%d points) error = %v, want nil", recipe.MaxPoints, err)
	}
	if _, err := recipe.NewPiecewise(rising(recipe.MaxPoints + 1)); !errors.Is(err, recipe.ErrInvalidParams) {
		t.Errorf("NewPiecewise(%d points) error = %v, want ErrInvalidParams", recipe.MaxPoints+1, err)
	}
}

func TestPiecewise_PointsReturnsACopyWithoutTheOrigin(t *testing.T) {
	m, err := recipe.NewPiecewise([]recipe.Point{{U: 1, Amount: 100}, {U: 2, Amount: 190}})
	if err != nil {
		t.Fatal(err)
	}

	pts := m.Points()
	pts[0].Amount = 999

	if got := m.Points(); len(got) != 2 || got[0] != (recipe.Point{U: 1, Amount: 100}) {
		t.Errorf("Points() = %v, want the two measured points, unchanged", got)
	}
}
