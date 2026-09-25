package catalog

import (
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/recipe"
)

// VariantComponent says how many units of a component one item of a variant
// uses: the linear first level of a recipe (§9.3).
type VariantComponent struct {
	ComponentID  uuid.UUID
	UnitsPerItem float64
}

const maxComponentsPerVariant = 50

func validateVariantComponents(uses []VariantComponent) error {
	f := fields{}
	f.check(len(uses) <= maxComponentsPerVariant, "components", "Paling banyak 50 komponen per varian.")
	seen := map[uuid.UUID]bool{}
	for _, u := range uses {
		f.check(u.ComponentID != uuid.Nil, "components", "Pilih komponennya.")
		f.check(!seen[u.ComponentID], "components", "Komponen yang sama tidak boleh dipilih dua kali.")
		f.check(u.UnitsPerItem > 0 && isFinite(u.UnitsPerItem), "components", "Jumlah komponen per item harus lebih dari 0.")
		seen[u.ComponentID] = true
	}
	return f.err()
}

// RecipeLine is how much of an ingredient a component needs: a recipe model
// applied to a batch's total units of the component (§10, §11).
type RecipeLine struct {
	ComponentID  uuid.UUID
	IngredientID uuid.UUID
	ModelType    recipe.ModelType
	Params       json.RawMessage // canonical, as recipe.Params writes it
	Points       []recipe.Point  // the measurements behind the model, if any
	WasteFactor  float64
	Version      int32 // grows by one on every real change
	UpdatedAt    time.Time
}

// RecipeLineInput sets a recipe line either from a model (ModelType and
// Params) or from measurements (ModelType and Points, which are fitted).
// Points given together with Params are kept for reference.
type RecipeLineInput struct {
	ModelType   recipe.ModelType
	Params      json.RawMessage
	Points      []recipe.Point
	WasteFactor float64 // 0 means 1: nothing lost
}

// RecipeLineWrite is a validated recipe line ready to store.
type RecipeLineWrite struct {
	ComponentID  uuid.UUID
	IngredientID uuid.UUID
	ModelType    recipe.ModelType
	Params       json.RawMessage
	Points       []recipe.Point
	WasteFactor  float64
}

// resolve checks the input with the recipe engine and returns the line to
// store. Every stored model has passed recipe.Validate, so a bad formula is
// refused here, at input, and never reaches a batch (§10).
func (in RecipeLineInput) resolve(componentID, ingredientID uuid.UUID) (RecipeLineWrite, error) {
	f := fields{}
	f.check(ingredientID != uuid.Nil, "ingredient_id", "Pilih bahannya.")
	waste := in.WasteFactor
	if waste == 0 {
		waste = 1
	}
	f.check(isFinite(waste) && waste >= 1, "waste_factor", "Faktor susut minimal 1, misalnya 1,05 untuk susut 5%.")
	f.check(len(in.Points) <= recipe.MaxPoints, "points", "Paling banyak 100 titik ukur.")
	for _, p := range in.Points {
		f.check(isFinite(p.U) && isFinite(p.Amount) && p.U > 0 && p.Amount >= 0, "points", "Setiap titik ukur butuh jumlah unit > 0 dan jumlah bahan >= 0.")
	}

	var model recipe.Model
	switch {
	case len(in.Params) > 0:
		m, err := recipe.Build(in.ModelType, in.Params)
		if err == nil {
			err = recipe.Validate(m)
		}
		f.check(err == nil, "params", ExplainRecipeError(err))
		model = m
	case len(in.Points) > 0:
		m, err := fit(in.ModelType, in.Points)
		f.check(err == nil, "points", ExplainRecipeError(err))
		model = m
	default:
		f.check(false, "params", "Isi parameter resep atau titik ukur.")
	}
	if err := f.err(); err != nil {
		return RecipeLineWrite{}, err
	}
	params, err := recipe.Params(model)
	if err != nil {
		return RecipeLineWrite{}, err
	}
	return RecipeLineWrite{
		ComponentID: componentID, IngredientID: ingredientID, ModelType: model.Type(),
		Params: params, Points: in.Points, WasteFactor: waste,
	}, nil
}

func fit(t recipe.ModelType, points []recipe.Point) (recipe.Model, error) {
	switch t {
	case recipe.Affine:
		m, _, err := recipe.FitAffine(points)
		return m, err
	case recipe.Power:
		m, _, err := recipe.FitPower(points)
		return m, err
	case recipe.Piecewise:
		m, _, err := recipe.FitPiecewise(points)
		return m, err
	default:
		return nil, errNotFittable
	}
}

var errNotFittable = errors.New("only affine, power, and piecewise models can be fitted")

// ExplainRecipeError explains a recipe engine error to the person editing,
// in Indonesian, with the engine's own detail after it.
func ExplainRecipeError(err error) string {
	var msg string
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errNotFittable):
		return "Hanya model affine, power, dan piecewise yang bisa dihitung dari titik ukur."
	case errors.Is(err, recipe.ErrDecreasing):
		msg = "Jumlah bahan turun saat jumlah unit naik"
	case errors.Is(err, recipe.ErrInvalidResult):
		msg = "Resep menghasilkan jumlah yang negatif atau tak terhingga"
	case errors.Is(err, recipe.ErrFit):
		msg = "Titik ukur tidak bisa dipakai"
	default:
		msg = "Parameter resep tidak valid"
	}
	return msg + " (" + err.Error() + ")."
}

func isFinite(x float64) bool {
	return !math.IsNaN(x) && !math.IsInf(x, 0)
}
