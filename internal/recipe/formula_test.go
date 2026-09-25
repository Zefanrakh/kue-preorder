package recipe_test

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/Zefanrakh/kue-preorder/internal/recipe"
)

func TestNewFormula_Sandbox(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{"empty", "   "},
		{"variable other than u", "x * 2"},
		{"current time", "u + now().Year()"},
		{"date parsing", `date("2026-01-01").Year() + u`},
		{"collection builtin", "len([1, 2]) + u"},
		{"string result", `"abc"`},
		{"boolean result", "u > 1"},
		{"syntax error", "10 +* u"},
		{"longer than 500 characters", strings.Repeat("u + ", 130) + "u"},
		{"more than 200 nodes", "u" + strings.Repeat("+1", 150)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := recipe.NewFormula(tt.expr)
			if !errors.Is(err, recipe.ErrInvalidParams) {
				t.Errorf("NewFormula(%q) = %v, %v; want ErrInvalidParams", tt.expr, m, err)
			}
		})
	}
}

func TestNewFormula_AllowsMathHelpers(t *testing.T) {
	tests := []struct {
		expr string
		want float64 // at u = 2.5
	}{
		{"max(50, 40*u)", 100},
		{"ceil(u) * 30", 90},
		{"floor(u) * 30", 60},
		{"round(u) * 10", 30},
		{"min(u, 2) * 10", 20},
		{"abs(u) * 2", 5},
		{"100 * u ** 0.9", 100 * math.Pow(2.5, 0.9)},
		{"100 * u ^ 0.9", 100 * math.Pow(2.5, 0.9)},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			m, err := recipe.NewFormula(tt.expr)
			if err != nil {
				t.Fatalf("NewFormula() error = %v", err)
			}
			got, err := m.Resolve(2.5)
			if err != nil || !near(got, tt.want) {
				t.Errorf("Resolve(2.5) = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestNewFormula_KeepsTrimmedSource(t *testing.T) {
	m, err := recipe.NewFormula("  10 + 90*u \n")
	if err != nil {
		t.Fatal(err)
	}
	if m.Expr() != "10 + 90*u" {
		t.Errorf("Expr() = %q", m.Expr())
	}
}

func TestFormula_RejectsBadResultsAtRuntime(t *testing.T) {
	tests := []struct {
		name string
		expr string
		u    float64
	}{
		{"negative amount", "u - 10", 1},
		{"division by zero", "1 / (u - 1)", 1},
		{"not a number", "(u - 1) / (u - 1) * 0 / 0", 1},
		{"memory bomb stopped by the VM budget", "(1..100000000)[0] + u", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := recipe.NewFormula(tt.expr)
			if err != nil {
				return // rejected even earlier: also fine
			}
			if _, err := m.Resolve(tt.u); !errors.Is(err, recipe.ErrInvalidResult) {
				t.Errorf("Resolve(%v) error = %v, want ErrInvalidResult", tt.u, err)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr error
	}{
		{"linear", "10 + 90*u", nil},
		{"steps never go down", "ceil(u) * 30", nil},
		{"sublinear power", "100 * u ** 0.9", nil},
		{"dips then rises", "abs(u - 10)", recipe.ErrDecreasing},
		{"shrinks as units grow", "100 - u", recipe.ErrDecreasing},
		{"negative for small batches", "u - 10", recipe.ErrInvalidResult},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := recipe.NewFormula(tt.expr)
			if err != nil {
				t.Fatal(err)
			}
			if err := recipe.Validate(m); !errors.Is(err, tt.wantErr) {
				t.Errorf("Validate(%q) = %v, want %v", tt.expr, err, tt.wantErr)
			}
		})
	}
}
