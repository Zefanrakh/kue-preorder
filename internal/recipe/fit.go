package recipe

import (
	"errors"
	"fmt"
	"math"
	"slices"
)

// ErrFit means no model of the requested type fits the measured points.
var ErrFit = errors.New("cannot fit the measured points")

// FitReport says how well a fitted model matches the measured points.
type FitReport struct {
	// R2 is the coefficient of determination of the amounts: 1 is a perfect
	// fit; it can be negative when the model is worse than the mean amount.
	// When every measured amount is the same, R2 is 1 for an exact fit and 0
	// otherwise.
	R2 float64
	// Residuals are fitted minus measured amounts, one per point, in input order.
	Residuals []float64
	// MaxAbsError is the largest |residual|.
	MaxAbsError float64
	// MaxRelError is the largest |residual| / measured amount, over points
	// with a measured amount > 0.
	MaxRelError float64
}

// FitResult is one model type fitted to measured points, for the CMS to
// compare side by side.
type FitResult struct {
	Type   ModelType
	Model  Model // nil when Err is set
	Report FitReport
	Err    error
}

// FitAll fits every model type that can be fitted (affine, power, piecewise)
// to points. Each result carries either a model and its report or the reason
// it could not be fitted.
func FitAll(points []Point) []FitResult {
	fits := []struct {
		typ ModelType
		fit func([]Point) (Model, FitReport, error)
	}{
		{Affine, func(p []Point) (Model, FitReport, error) { return FitAffine(p) }},
		{Power, func(p []Point) (Model, FitReport, error) { return FitPower(p) }},
		{Piecewise, func(p []Point) (Model, FitReport, error) { return FitPiecewise(p) }},
	}
	results := make([]FitResult, 0, len(fits))
	for _, f := range fits {
		m, report, err := f.fit(points)
		if err != nil {
			m = nil
		}
		results = append(results, FitResult{Type: f.typ, Model: m, Report: report, Err: err})
	}
	return results
}

// FitAffine fits a + b*u by ordinary least squares. When the best line would
// have a < 0 (a negative fixed part), it fits through the origin instead
// (a = 0). A single distinct unit count also fits through the origin.
// Amounts that fall as units grow give b < 0 and are rejected.
func FitAffine(points []Point) (AffineModel, FitReport, error) {
	if err := checkPoints(points); err != nil {
		return AffineModel{}, FitReport{}, err
	}
	a, b := 0.0, throughOrigin(points)
	if distinctUnits(points) >= 2 {
		a, b = leastSquares(points, func(p Point) (float64, float64) { return p.U, p.Amount })
		if b < 0 {
			return AffineModel{}, FitReport{}, fmt.Errorf("%w: amounts fall as units grow", ErrFit)
		}
		if a < 0 {
			a, b = 0, throughOrigin(points)
		}
	}
	m, err := NewAffine(a, b)
	if err != nil {
		return AffineModel{}, FitReport{}, fmt.Errorf("%w: %w", ErrFit, err)
	}
	if err := usable(m); err != nil {
		return AffineModel{}, FitReport{}, err
	}
	return m, report(m, points), nil
}

// FitPower fits a * u^b by least squares on ln(amount) = ln(a) + b*ln(u).
// Every amount must be > 0 (zero has no logarithm), there must be at least
// two distinct unit counts, and amounts that fall as units grow (b < 0) are
// rejected.
func FitPower(points []Point) (PowerModel, FitReport, error) {
	if err := checkPoints(points); err != nil {
		return PowerModel{}, FitReport{}, err
	}
	for i, p := range points {
		if p.Amount == 0 {
			return PowerModel{}, FitReport{}, fmt.Errorf("%w: point %d: a power fit needs every amount > 0", ErrFit, i+1)
		}
	}
	if distinctUnits(points) < 2 {
		return PowerModel{}, FitReport{}, fmt.Errorf("%w: a power fit needs at least two different unit counts", ErrFit)
	}
	lnA, b := leastSquares(points, func(p Point) (float64, float64) { return math.Log(p.U), math.Log(p.Amount) })
	if b < 0 {
		return PowerModel{}, FitReport{}, fmt.Errorf("%w: amounts fall as units grow", ErrFit)
	}
	m, err := NewPower(math.Exp(lnA), b)
	if err != nil {
		return PowerModel{}, FitReport{}, fmt.Errorf("%w: %w", ErrFit, err)
	}
	if err := usable(m); err != nil {
		return PowerModel{}, FitReport{}, err
	}
	return m, report(m, points), nil
}

// FitPiecewise builds the piecewise model through the points: sorted by
// units, with the amounts of repeated unit counts averaged. Amounts that fall
// as units grow are rejected, naming where.
func FitPiecewise(points []Point) (PiecewiseModel, FitReport, error) {
	if err := checkPoints(points); err != nil {
		return PiecewiseModel{}, FitReport{}, err
	}
	merged := mergeByUnits(points)
	for i := 1; i < len(merged); i++ {
		if merged[i].Amount < merged[i-1].Amount {
			return PiecewiseModel{}, FitReport{}, fmt.Errorf("%w: the amount falls from %v at u=%v to %v at u=%v",
				ErrFit, merged[i-1].Amount, merged[i-1].U, merged[i].Amount, merged[i].U)
		}
	}
	m, err := NewPiecewise(merged)
	if err != nil {
		return PiecewiseModel{}, FitReport{}, fmt.Errorf("%w: %w", ErrFit, err)
	}
	if err := usable(m); err != nil {
		return PiecewiseModel{}, FitReport{}, err
	}
	return m, report(m, points), nil
}

// usable rejects a fitted model that Validate would refuse to save, such as a
// power curve so steep it overflows within u = 0.5..100. A fit never hands
// the CMS a model it cannot store.
func usable(m Model) error {
	if err := Validate(m); err != nil {
		return fmt.Errorf("%w: the fitted %s model is unusable, check the measurements: %w", ErrFit, m.Type(), err)
	}
	return nil
}

// checkPoints accepts 1 to MaxPoints points, each with finite U > 0 and a
// finite amount >= 0.
func checkPoints(points []Point) error {
	if len(points) == 0 {
		return fmt.Errorf("%w: no measured points", ErrFit)
	}
	if len(points) > MaxPoints {
		return fmt.Errorf("%w: at most %d measured points, got %d", ErrFit, MaxPoints, len(points))
	}
	for i, p := range points {
		if !isFinite(p.U) || !isFinite(p.Amount) || p.U <= 0 || p.Amount < 0 {
			return fmt.Errorf("%w: point %d: units must be > 0 and the amount >= 0, got (%v, %v)", ErrFit, i+1, p.U, p.Amount)
		}
	}
	return nil
}

func distinctUnits(points []Point) int {
	return len(mergeByUnits(points))
}

// mergeByUnits sorts points by U and averages the amounts of equal U.
func mergeByUnits(points []Point) []Point {
	sorted := slices.Clone(points)
	slices.SortStableFunc(sorted, func(x, y Point) int { return cmpFloat(x.U, y.U) })
	var merged []Point
	for i := 0; i < len(sorted); {
		j, sum := i, 0.0
		for ; j < len(sorted) && sorted[j].U == sorted[i].U; j++ {
			sum += sorted[j].Amount
		}
		merged = append(merged, Point{U: sorted[i].U, Amount: sum / float64(j-i)})
		i = j
	}
	return merged
}

func cmpFloat(x, y float64) int {
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	default:
		return 0
	}
}

// leastSquares fits y = intercept + slope*x over the points mapped by xy,
// using centred sums for numerical stability. It needs two distinct x.
func leastSquares(points []Point, xy func(Point) (float64, float64)) (intercept, slope float64) {
	var meanX, meanY float64
	for _, p := range points {
		x, y := xy(p)
		meanX += x
		meanY += y
	}
	n := float64(len(points))
	meanX, meanY = meanX/n, meanY/n
	var sxx, sxy float64
	for _, p := range points {
		x, y := xy(p)
		sxx += (x - meanX) * (x - meanX)
		sxy += (x - meanX) * (y - meanY)
	}
	slope = sxy / sxx
	return meanY - slope*meanX, slope
}

// throughOrigin is the least-squares b of amount = b*u.
func throughOrigin(points []Point) float64 {
	var suy, suu float64
	for _, p := range points {
		suy += p.U * p.Amount
		suu += p.U * p.U
	}
	return suy / suu
}

func report(m Model, points []Point) FitReport {
	r := FitReport{Residuals: make([]float64, len(points))}
	var mean float64
	for _, p := range points {
		mean += p.Amount
	}
	mean /= float64(len(points))

	var ssRes, ssTot float64
	for i, p := range points {
		fitted, err := m.Resolve(p.U)
		if err != nil { // unreachable: points are valid and fitted models are well-formed
			fitted = math.NaN()
		}
		res := fitted - p.Amount
		r.Residuals[i] = res
		ssRes += res * res
		ssTot += (p.Amount - mean) * (p.Amount - mean)
		r.MaxAbsError = math.Max(r.MaxAbsError, math.Abs(res))
		if p.Amount > 0 {
			r.MaxRelError = math.Max(r.MaxRelError, math.Abs(res)/p.Amount)
		}
	}
	switch {
	case ssTot > 0:
		r.R2 = 1 - ssRes/ssTot
	case ssRes <= slack(0):
		r.R2 = 1
	default:
		r.R2 = 0
	}
	return r
}
