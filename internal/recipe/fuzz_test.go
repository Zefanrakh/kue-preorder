package recipe_test

import (
	"errors"
	"math"
	"testing"

	"github.com/Zefanrakh/kue-preorder/internal/recipe"
)

// FuzzFormula feeds arbitrary text to the formula sandbox. Whatever a user
// types, compiling and running must never panic, and a result that comes back
// without an error must honour the model contract. Run longer with:
//
//	go test -run '^$' -fuzz FuzzFormula -fuzztime 1m ./internal/recipe/
func FuzzFormula(f *testing.F) {
	for _, seed := range []string{
		"10 + 90*u", "100 * u ** 0.926", "max(50, 40*u)", "ceil(u) * 30",
		"u - 10", "1 / (u - 1)", "(1..10)[0] + u", "now()", `"x"`, "", "u +",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, src string) {
		m, err := recipe.NewFormula(src)
		if err != nil {
			if !errors.Is(err, recipe.ErrInvalidParams) {
				t.Fatalf("NewFormula(%q) error = %v, want ErrInvalidParams", src, err)
			}
			return
		}
		for _, u := range []float64{0, 0.5, 1, 2.5, 100} {
			v, err := m.Resolve(u)
			if err == nil && (math.IsNaN(v) || math.IsInf(v, 0) || v < 0) {
				t.Fatalf("%q at u=%v returned %v without an error", src, u, v)
			}
		}
		_ = recipe.Validate(m)
	})
}
