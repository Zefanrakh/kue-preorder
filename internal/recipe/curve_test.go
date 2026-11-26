package recipe_test

import (
	"math"
	"testing"

	"github.com/Zefanrakh/kue-preorder/internal/recipe"
)

func TestCurve_SamplesValidateRangeFromZero(t *testing.T) {
	curve := recipe.Curve(recipe.AffineModel{A: 10, B: 90}, 12)

	if len(curve) != 201 {
		t.Fatalf("Curve() has %d samples, want 201 (u = 0, then 200 steps)", len(curve))
	}
	// Resolve(0) == 0 holds for the chart too, even with a fixed part a.
	for i, want := range map[int]recipe.Point{0: {U: 0, Amount: 0}, 1: {U: 0.5, Amount: 55}, 2: {U: 1, Amount: 100}, 200: {U: 100, Amount: 9010}} {
		if curve[i] != want {
			t.Errorf("Curve()[%d] = %+v, want %+v", i, curve[i], want)
		}
	}
}

func TestCurve_ReachesTheFurthestPoint(t *testing.T) {
	tests := []struct {
		name    string
		upTo    float64
		wantEnd float64
	}{
		{"no points", 0, 100},
		{"within Validate's range", 100, 100},
		{"just beyond it", 100.5, 200},
		{"on a multiple of 100", 200, 200},
		{"well beyond it", 250, 300},
		{"not a number", math.NaN(), 100},
		{"infinite", math.Inf(1), 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			curve := recipe.Curve(recipe.AffineModel{A: 0, B: 2}, tt.upTo)

			if len(curve) != 201 {
				t.Fatalf("Curve() has %d samples, want 201", len(curve))
			}
			if last := curve[200]; last.U != tt.wantEnd || last.Amount != 2*tt.wantEnd {
				t.Errorf("last sample = %+v, want u = %v", last, tt.wantEnd)
			}
			step := tt.wantEnd / 200
			for i, p := range curve {
				if p.U != float64(i)*step || math.Mod(p.U, 0.5) != 0 {
					t.Fatalf("sample %d at u = %v, want %v, a multiple of 0.5", i, p.U, float64(i)*step)
				}
			}
		})
	}
}

// failsAbove resolves u as u, and fails above limit.
type failsAbove struct{ limit float64 }

func (f failsAbove) Resolve(u float64) (float64, error) {
	if u > f.limit {
		return 0, recipe.ErrInvalidResult
	}
	return u, nil
}

func (failsAbove) Type() recipe.ModelType { return recipe.Formula }

func TestCurve_StopsBeforeTheModelFails(t *testing.T) {
	curve := recipe.Curve(failsAbove{limit: 150}, 200)

	// The steps are 1 apart up to 200; u = 150 is the last one that resolves.
	if len(curve) != 151 || curve[150].U != 150 {
		t.Fatalf("Curve() = %d samples ending at %+v, want 151 ending at u = 150", len(curve), curve[len(curve)-1])
	}
}
