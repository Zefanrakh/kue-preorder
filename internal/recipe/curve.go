package recipe

import "math"

// The shape of a Curve: u = 0 plus curveSteps equal steps, ending at
// curveMinEnd or the next multiple of it above the furthest measured point.
const (
	curveSteps  = validateSteps                // 200 steps, 201 samples
	curveMinEnd = validateSteps * validateStep // 100, the top of Validate's range
)

// Curve samples m for the CMS fit chart, so the browser draws a model without
// evaluating it (docs/screens.md, C4). The samples start at u = 0 and take
// 200 equal steps to the end of the chart: u = 100, the top of the range
// Validate checks, or, when upTo (the furthest measured units) lies beyond
// it, the next multiple of 100 above upTo. With every point within 100 the
// samples are Validate's grid (0.5, 1.0, ..., 100) plus u = 0; every step is
// a multiple of 0.5.
//
// The curve stops before the first u that m cannot resolve. A fitted model
// passed Validate, so for one that can only happen above u = 100.
func Curve(m Model, upTo float64) []Point {
	end := curveMinEnd
	if isFinite(upTo) && upTo > end {
		if e := curveMinEnd * math.Ceil(upTo/curveMinEnd); isFinite(e) {
			end = e
		}
	}
	step := end / curveSteps
	curve := make([]Point, 0, curveSteps+1)
	for i := 0; i <= curveSteps; i++ {
		u := float64(i) * step
		v, err := m.Resolve(u)
		if err != nil {
			break
		}
		curve = append(curve, Point{U: u, Amount: v})
	}
	return curve
}
