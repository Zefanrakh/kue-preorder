package recipe

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Build turns a stored recipe line (component_ingredients.model_type and
// params) into a Model. Params must hold exactly the model's fields:
//
//	affine     {"a": 10, "b": 90}
//	power      {"a": 100, "b": 0.926}
//	piecewise  {"points": [[1, 100], [2, 190], [4, 360]]}
//	formula    {"expr": "10 + 90*u"}
func Build(t ModelType, params []byte) (Model, error) {
	switch t {
	case Affine, Power:
		var p struct {
			A *float64 `json:"a"`
			B *float64 `json:"b"`
		}
		if err := decodeStrict(params, &p); err != nil {
			return nil, err
		}
		if p.A == nil || p.B == nil {
			return nil, fmt.Errorf("%w: %s needs both \"a\" and \"b\"", ErrInvalidParams, t)
		}
		if t == Affine {
			return NewAffine(*p.A, *p.B)
		}
		return NewPower(*p.A, *p.B)

	case Piecewise:
		var p struct {
			Points *[][]float64 `json:"points"`
		}
		if err := decodeStrict(params, &p); err != nil {
			return nil, err
		}
		if p.Points == nil {
			return nil, fmt.Errorf("%w: piecewise needs \"points\"", ErrInvalidParams)
		}
		points := make([]Point, len(*p.Points))
		for i, pair := range *p.Points {
			if len(pair) != 2 {
				return nil, fmt.Errorf("%w: point %d must be [units, amount]", ErrInvalidParams, i+1)
			}
			points[i] = Point{U: pair[0], Amount: pair[1]}
		}
		return NewPiecewise(points)

	case Formula:
		var p struct {
			Expr *string `json:"expr"`
		}
		if err := decodeStrict(params, &p); err != nil {
			return nil, err
		}
		if p.Expr == nil {
			return nil, fmt.Errorf("%w: formula needs \"expr\"", ErrInvalidParams)
		}
		return NewFormula(*p.Expr)

	default:
		return nil, fmt.Errorf("%w: unknown model type %q", ErrInvalidParams, t)
	}
}

// decodeStrict decodes one JSON object into v, rejecting unknown fields and
// trailing data, so a typo in params fails loudly instead of being ignored.
func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidParams, err)
	}
	if dec.More() {
		return fmt.Errorf("%w: unexpected data after the JSON object", ErrInvalidParams)
	}
	return nil
}
