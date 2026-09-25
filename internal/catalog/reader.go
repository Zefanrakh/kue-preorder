package catalog

import (
	"context"
	"errors"
	"fmt"

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

// VariantSummary is what checkout needs to price and schedule a variant.
type VariantSummary struct {
	ID                uuid.UUID
	ProductID         uuid.UUID
	Name              string
	PriceIDR          int64
	ProductionMinutes int32
	MinNoticeHours    int32
	Active            bool
}

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
