package recipe

import (
	"fmt"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

const (
	// maxFormulaLen and maxFormulaNodes keep a user-written formula small
	// enough that compiling and running it stays cheap.
	maxFormulaLen   = 500
	maxFormulaNodes = 200
)

// formulaBuiltins are the only functions a formula may call. Everything else
// is off, notably now() and date(): a recipe must give the same amount
// whenever it runs.
var formulaBuiltins = []string{"abs", "ceil", "floor", "round", "max", "min"}

// FormulaModel evaluates a user-written expression of u with expr-lang/expr,
// compiled once. The sandbox exposes only the variable u, a few math
// functions, and the ** (or ^) power operator; expr's VM caps the memory a
// run may allocate.
type FormulaModel struct {
	expr    string
	program *vm.Program
}

// NewFormula compiles expression, e.g. "10 + 90*u".
func NewFormula(expression string) (FormulaModel, error) {
	e := strings.TrimSpace(expression)
	switch {
	case e == "":
		return FormulaModel{}, fmt.Errorf("%w: formula is empty", ErrInvalidParams)
	case len(e) > maxFormulaLen:
		return FormulaModel{}, fmt.Errorf("%w: formula is longer than %d characters", ErrInvalidParams, maxFormulaLen)
	}
	opts := []expr.Option{
		expr.Env(map[string]any{"u": 0.0}),
		expr.AsFloat64(),
		expr.MaxNodes(maxFormulaNodes),
		expr.DisableAllBuiltins(),
	}
	for _, name := range formulaBuiltins {
		opts = append(opts, expr.EnableBuiltin(name))
	}
	program, err := expr.Compile(e, opts...)
	if err != nil {
		return FormulaModel{}, fmt.Errorf("%w: formula %q: %w", ErrInvalidParams, e, err)
	}
	return FormulaModel{expr: e, program: program}, nil
}

// Type implements Model.
func (FormulaModel) Type() ModelType { return Formula }

// Expr returns the formula's source.
func (m FormulaModel) Expr() string { return m.expr }

// Resolve implements Model.
func (m FormulaModel) Resolve(u float64) (float64, error) {
	return evaluate(u, func(u float64) (float64, error) {
		out, err := expr.Run(m.program, map[string]any{"u": u})
		if err != nil {
			return 0, fmt.Errorf("%w: formula %q at u=%v: %w", ErrInvalidResult, m.expr, u, err)
		}
		v, ok := out.(float64)
		if !ok { // AsFloat64 rules this out; kept in case the library changes
			return 0, fmt.Errorf("%w: formula %q returned %T", ErrInvalidResult, m.expr, out)
		}
		return v, nil
	})
}
