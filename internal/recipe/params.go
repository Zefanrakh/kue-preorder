package recipe

import (
	"encoding/json"
	"fmt"
)

// Params encodes m as the params JSON that Build reads back, for storing a
// fitted model in component_ingredients.params.
func Params(m Model) ([]byte, error) {
	switch m := m.(type) {
	case AffineModel:
		return json.Marshal(struct {
			A float64 `json:"a"`
			B float64 `json:"b"`
		}{m.A, m.B})
	case PowerModel:
		return json.Marshal(struct {
			A float64 `json:"a"`
			B float64 `json:"b"`
		}{m.A, m.B})
	case PiecewiseModel:
		points := make([][2]float64, 0, len(m.points)-1)
		for _, p := range m.Points() {
			points = append(points, [2]float64{p.U, p.Amount})
		}
		return json.Marshal(struct {
			Points [][2]float64 `json:"points"`
		}{points})
	case FormulaModel:
		return json.Marshal(struct {
			Expr string `json:"expr"`
		}{m.expr})
	default:
		return nil, fmt.Errorf("%w: cannot encode %T", ErrInvalidParams, m)
	}
}
