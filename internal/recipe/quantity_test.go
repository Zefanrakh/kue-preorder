package recipe_test

import (
	"errors"
	"math"
	"testing"

	"github.com/Zefanrakh/kue-preorder/internal/recipe"
)

func TestQuantity(t *testing.T) {
	tests := []struct {
		name   string
		model  recipe.Model
		u      float64
		waste  float64
		round  recipe.Rounding
		wanted int64
	}{
		{"grams", recipe.AffineModel{A: 10, B: 90}, 2, 1, recipe.RoundNearest, 190},
		{"waste factor applied before rounding", recipe.AffineModel{A: 10, B: 90}, 2, 1.05, recipe.RoundNearest, 200}, // 199.5
		{"eggs round up: a dough cannot use 0.4 egg", recipe.AffineModel{B: 0.4}, 6, 1, recipe.RoundUp, 3},            // 2.4
		{"eggs to nearest would under-buy", recipe.AffineModel{B: 0.4}, 6, 1, recipe.RoundNearest, 2},
		{"float noise: 3.0000000000000004 eggs are 3", recipe.AffineModel{B: 0.1}, 30, 1, recipe.RoundUp, 3},
		{"no units, nothing to buy", recipe.AffineModel{A: 10, B: 90}, 0, 1.2, recipe.RoundUp, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := recipe.Quantity(tt.model, tt.u, tt.waste, tt.round)
			if err != nil || got != tt.wanted {
				t.Errorf("Quantity() = %d, %v; want %d", got, err, tt.wanted)
			}
		})
	}
}

func TestQuantity_RejectsBadInput(t *testing.T) {
	m := recipe.AffineModel{A: 10, B: 90}
	tests := []struct {
		name    string
		model   recipe.Model
		u       float64
		waste   float64
		round   recipe.Rounding
		wantErr error
	}{
		{"waste factor below 1", m, 1, 0.9, recipe.RoundNearest, recipe.ErrInvalidWasteFactor},
		{"waste factor NaN", m, 1, math.NaN(), recipe.RoundNearest, recipe.ErrInvalidWasteFactor},
		{"waste factor infinite", m, 1, math.Inf(1), recipe.RoundNearest, recipe.ErrInvalidWasteFactor},
		{"negative units", m, -1, 1, recipe.RoundNearest, recipe.ErrInvalidUnits},
		{"amount too large to hold exactly", recipe.AffineModel{B: 1e17}, 1, 1, recipe.RoundNearest, recipe.ErrInvalidResult},
		{"amount overflows to infinity", recipe.AffineModel{B: 1e300}, 1e10, 1, recipe.RoundNearest, recipe.ErrInvalidResult},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := recipe.Quantity(tt.model, tt.u, tt.waste, tt.round); !errors.Is(err, tt.wantErr) {
				t.Errorf("Quantity() error = %v, want %v", err, tt.wantErr)
			}
		})
	}

	if _, err := recipe.Quantity(m, 1, 1, recipe.Rounding(7)); err == nil {
		t.Error("Quantity() with an unknown rounding: error = nil")
	}
}
