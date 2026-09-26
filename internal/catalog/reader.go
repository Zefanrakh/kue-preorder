package catalog

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/recipe"
)

// ErrBrokenRecipe means a stored recipe line cannot be turned into a model.
// The batch job marks its batch as failed and alerts; it never guesses (§11).
var ErrBrokenRecipe = errors.New("stored recipe cannot be evaluated")

// ComponentUse is one component of one variant, per item of the variant.
type ComponentUse struct {
	VariantID    uuid.UUID
	ComponentID  uuid.UUID
	UnitsPerItem float64
}

// StoredRecipeLine is a recipe line as stored, with its ingredient's unit.
type StoredRecipeLine struct {
	ComponentID  uuid.UUID
	IngredientID uuid.UUID
	ModelType    recipe.ModelType
	Params       []byte
	WasteFactor  float64
	Version      int32
	BaseUnit     BaseUnit
}

// RecipeModel is a recipe line ready for the engine: its model, its waste
// factor, and how its ingredient's amounts round (§10).
type RecipeModel struct {
	ComponentID  uuid.UUID
	IngredientID uuid.UUID
	Model        recipe.Model
	WasteFactor  float64
	Rounding     recipe.Rounding
	Version      int32
}

// DefaultPack is the pack a shopping list rounds an ingredient to.
type DefaultPack struct {
	IngredientID uuid.UUID
	SupplierID   uuid.UUID
	Size         float64 // in the ingredient's base unit
	Unit         string
	PriceIDR     *int64
}

// VariantSummary is what checkout needs to price, name, and schedule a
// variant.
type VariantSummary struct {
	ID                uuid.UUID
	ProductID         uuid.UUID
	ProductName       string
	Name              string
	PriceIDR          int64
	ProductionMinutes int32
	MinNoticeHours    int32
	Active            bool
	ProductActive     bool
}

// OnSale reports whether customers may order the variant: it and its
// product are both active, as the storefront shows them.
func (v VariantSummary) OnSale() bool { return v.Active && v.ProductActive }

// IngredientCost is the estimated ingredient cost of one item of a variant,
// for the down payment (§14).
type IngredientCost struct {
	PerItemIDR int64 // rounded up to the rupiah
	// Unpriced lists the ingredients left out of the estimate because they
	// have no default pack or the pack has no price.
	Unpriced []uuid.UUID
}

const (
	// maxEstimateIDR bounds an estimate; beyond it the recipe or a pack
	// price is surely wrong.
	maxEstimateIDR = 1 << 40
	// floatNoiseIDR is far below a rupiah and far above float64 error for
	// any realistic cost (under a billion rupiah per item).
	floatNoiseIDR = 1e-6
)

// Reader lets other modules read the catalog in-process: the batch job (M3)
// and checkout (M2) act for a tenant, not for a signed-in person, so Reader
// takes the tenant explicitly and checks no roles. It never writes.
type Reader struct {
	repo Repository
}

// NewReader returns a Reader over repo.
func NewReader(repo Repository) *Reader {
	return &Reader{repo: repo}
}

// ComponentUses returns the components of the variants (level 1 of §11), in
// (variant, component) order.
func (r *Reader) ComponentUses(ctx context.Context, tenantID uuid.UUID, variantIDs []uuid.UUID) ([]ComponentUse, error) {
	return r.repo.ComponentUses(ctx, tenantID, variantIDs)
}

// RecipeModels returns the recipe lines of the components as engine models
// (level 2 of §11), in (component, ingredient) order. A line that cannot be
// built fails the whole call with ErrBrokenRecipe naming the component and
// the ingredient.
func (r *Reader) RecipeModels(ctx context.Context, tenantID uuid.UUID, componentIDs []uuid.UUID) ([]RecipeModel, error) {
	lines, err := r.repo.StoredRecipeLines(ctx, tenantID, componentIDs)
	if err != nil {
		return nil, err
	}
	models := make([]RecipeModel, len(lines))
	for i, l := range lines {
		m, err := recipe.Build(l.ModelType, l.Params)
		if err != nil {
			return nil, fmt.Errorf("%w: component %s, ingredient %s: %w", ErrBrokenRecipe, l.ComponentID, l.IngredientID, err)
		}
		models[i] = RecipeModel{
			ComponentID: l.ComponentID, IngredientID: l.IngredientID, Model: m,
			WasteFactor: l.WasteFactor, Rounding: l.BaseUnit.Rounding(), Version: l.Version,
		}
	}
	return models, nil
}

// DefaultPacks returns the default pack of each ingredient that has one
// (level 3 of §11).
func (r *Reader) DefaultPacks(ctx context.Context, tenantID uuid.UUID, ingredientIDs []uuid.UUID) ([]DefaultPack, error) {
	return r.repo.DefaultPacks(ctx, tenantID, ingredientIDs)
}

// Variants returns what checkout needs about the variants, active or not;
// the caller decides what an inactive variant means for it.
func (r *Reader) Variants(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]VariantSummary, error) {
	return r.repo.VariantSummaries(ctx, tenantID, ids)
}

// IngredientCosts estimates what one item of each variant costs in
// ingredients (§14): every recipe evaluated at u = 1, where a unit costs the
// most since a larger batch only lowers the cost per unit, with the waste
// factor, priced by the default pack. Amounts are not rounded to whole
// units, since this is the cost of one item's share. Every requested variant
// is in the result; one without components costs 0.
func (r *Reader) IngredientCosts(ctx context.Context, tenantID uuid.UUID, variantIDs []uuid.UUID) (map[uuid.UUID]IngredientCost, error) {
	uses, err := r.ComponentUses(ctx, tenantID, variantIDs)
	if err != nil {
		return nil, err
	}
	models, err := r.RecipeModels(ctx, tenantID, uniqueIDs(uses, func(u ComponentUse) uuid.UUID { return u.ComponentID }))
	if err != nil {
		return nil, err
	}
	packs, err := r.DefaultPacks(ctx, tenantID, uniqueIDs(models, func(m RecipeModel) uuid.UUID { return m.IngredientID }))
	if err != nil {
		return nil, err
	}
	packOf := make(map[uuid.UUID]DefaultPack, len(packs))
	for _, p := range packs {
		packOf[p.IngredientID] = p
	}

	perUnit := map[uuid.UUID]float64{}      // component -> rupiah per unit
	unpriced := map[uuid.UUID][]uuid.UUID{} // component -> ingredients without a price
	for _, m := range models {
		amount, err := m.Model.Resolve(1)
		if err != nil {
			return nil, fmt.Errorf("%w: component %s, ingredient %s at u=1: %w", ErrBrokenRecipe, m.ComponentID, m.IngredientID, err)
		}
		pack, ok := packOf[m.IngredientID]
		if !ok || pack.PriceIDR == nil {
			unpriced[m.ComponentID] = append(unpriced[m.ComponentID], m.IngredientID)
			continue
		}
		perUnit[m.ComponentID] += amount * m.WasteFactor * float64(*pack.PriceIDR) / pack.Size
	}

	sums := make(map[uuid.UUID]float64, len(variantIDs))
	out := make(map[uuid.UUID]IngredientCost, len(variantIDs))
	for _, id := range variantIDs {
		out[id] = IngredientCost{}
	}
	for _, u := range uses {
		sums[u.VariantID] += u.UnitsPerItem * perUnit[u.ComponentID]
		c := out[u.VariantID]
		for _, ing := range unpriced[u.ComponentID] {
			if !slices.Contains(c.Unpriced, ing) {
				c.Unpriced = append(c.Unpriced, ing)
			}
		}
		out[u.VariantID] = c
	}
	for id, sum := range sums {
		if !isFinite(sum) || sum < 0 || sum > maxEstimateIDR {
			return nil, fmt.Errorf("ingredient cost of variant %s is %v rupiah: check its recipes and pack prices", id, sum)
		}
		c := out[id]
		// Float noise such as 1530.0000000000002 must not round up to 1531.
		c.PerItemIDR = int64(math.Ceil(sum - floatNoiseIDR))
		out[id] = c
	}
	return out, nil
}

// uniqueIDs returns the distinct ids of xs, in first-seen order.
func uniqueIDs[T any](xs []T, id func(T) uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	out := []uuid.UUID{}
	for _, x := range xs {
		if i := id(x); !seen[i] {
			seen[i] = true
			out = append(out, i)
		}
	}
	return out
}
