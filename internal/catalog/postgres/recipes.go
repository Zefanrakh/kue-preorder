package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
	"github.com/Zefanrakh/kue-preorder/internal/recipe"
)

// recipeChanged is the outbox event the batch job (M3) reacts to by
// recomputing open batches (§21).
const recipeChanged = "catalog.recipe_changed"

// ListVariantComponents implements catalog.Repository.
func (r *Repository) ListVariantComponents(ctx context.Context, tenantID, variantID uuid.UUID) ([]catalog.VariantComponent, error) {
	rows, err := r.q.ListVariantComponents(ctx, ListVariantComponentsParams{TenantID: tenantID, VariantID: variantID})
	return mapRows(rows, err, toVariantComponent)
}

// SetVariantComponents implements catalog.Repository.
func (r *Repository) SetVariantComponents(ctx context.Context, tenantID, variantID uuid.UUID, uses []catalog.VariantComponent, at time.Time) ([]catalog.VariantComponent, error) {
	var result []catalog.VariantComponent
	err := r.db.InTx(ctx, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		if _, err := q.LockVariant(ctx, LockVariantParams{TenantID: tenantID, ID: variantID}); err != nil {
			return mapErr(err)
		}
		current, err := q.ListVariantComponents(ctx, ListVariantComponentsParams{TenantID: tenantID, VariantID: variantID})
		if err != nil {
			return mapErr(err)
		}
		result = mapSlice(current, toVariantComponent)
		if sameComponents(result, uses) {
			return nil
		}

		keep := make([]uuid.UUID, len(uses))
		for i, u := range uses {
			keep[i] = u.ComponentID
		}
		if err := q.DeleteVariantComponentsExcept(ctx, DeleteVariantComponentsExceptParams{TenantID: tenantID, VariantID: variantID, Keep: keep}); err != nil {
			return mapErr(err)
		}
		for _, u := range uses {
			if err := q.UpsertVariantComponent(ctx, UpsertVariantComponentParams{
				TenantID: tenantID, VariantID: variantID, ComponentID: u.ComponentID, UnitsPerItem: u.UnitsPerItem, Now: at,
			}); err != nil {
				return mapErr(err)
			}
		}
		rows, err := q.ListVariantComponents(ctx, ListVariantComponentsParams{TenantID: tenantID, VariantID: variantID})
		if err != nil {
			return mapErr(err)
		}
		result = mapSlice(rows, toVariantComponent)
		return outbox.Append(ctx, tx, outbox.Event{
			TenantID: tenantID, Aggregate: "variant", Type: recipeChanged, At: at,
			Payload: map[string]any{"variant_id": variantID},
		})
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// sameComponents reports whether uses is exactly what is stored, in any order.
func sameComponents(stored, uses []catalog.VariantComponent) bool {
	if len(stored) != len(uses) {
		return false
	}
	want := make(map[uuid.UUID]float64, len(uses))
	for _, u := range uses {
		want[u.ComponentID] = u.UnitsPerItem
	}
	for _, s := range stored {
		if units, ok := want[s.ComponentID]; !ok || units != s.UnitsPerItem {
			return false
		}
	}
	return true
}

// ListRecipeLines implements catalog.Repository.
func (r *Repository) ListRecipeLines(ctx context.Context, tenantID, componentID uuid.UUID) ([]catalog.RecipeLine, error) {
	rows, err := r.q.ListRecipeLines(ctx, ListRecipeLinesParams{TenantID: tenantID, ComponentID: componentID})
	if err != nil {
		return nil, mapErr(err)
	}
	lines := make([]catalog.RecipeLine, len(rows))
	for i, row := range rows {
		if lines[i], err = toRecipeLine(row); err != nil {
			return nil, err
		}
	}
	return lines, nil
}

// SetRecipeLine implements catalog.Repository.
func (r *Repository) SetRecipeLine(ctx context.Context, tenantID uuid.UUID, w catalog.RecipeLineWrite, at time.Time) (catalog.RecipeLine, error) {
	points, err := encodePoints(w.Points)
	if err != nil {
		return catalog.RecipeLine{}, err
	}
	var row ComponentIngredient
	err = r.db.InTx(ctx, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		if _, err := q.LockComponent(ctx, LockComponentParams{TenantID: tenantID, ID: w.ComponentID}); err != nil {
			return mapErr(err)
		}
		key := GetRecipeLineParams{TenantID: tenantID, ComponentID: w.ComponentID, IngredientID: w.IngredientID}
		existing, err := q.GetRecipeLine(ctx, key)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			row, err = q.InsertRecipeLine(ctx, InsertRecipeLineParams{
				TenantID: tenantID, ComponentID: w.ComponentID, IngredientID: w.IngredientID,
				ModelType: string(w.ModelType), Params: w.Params, MeasuredPoints: points, WasteFactor: w.WasteFactor, Now: at,
			})
		case err != nil:
		default:
			var differs bool
			differs, err = q.RecipeLineDiffers(ctx, RecipeLineDiffersParams{
				TenantID: tenantID, ComponentID: w.ComponentID, IngredientID: w.IngredientID,
				ModelType: string(w.ModelType), Params: w.Params, MeasuredPoints: points, WasteFactor: w.WasteFactor,
			})
			if err != nil || !differs {
				row = existing
				return mapErr(err)
			}
			row, err = q.UpdateRecipeLine(ctx, UpdateRecipeLineParams{
				TenantID: tenantID, ComponentID: w.ComponentID, IngredientID: w.IngredientID,
				ModelType: string(w.ModelType), Params: w.Params, MeasuredPoints: points, WasteFactor: w.WasteFactor, Now: at,
			})
		}
		if err != nil {
			return mapErr(err)
		}
		return outbox.Append(ctx, tx, outbox.Event{
			TenantID: tenantID, Aggregate: "component", Type: recipeChanged, At: at,
			Payload: map[string]any{"component_id": w.ComponentID, "ingredient_id": w.IngredientID, "version": row.Version},
		})
	})
	if err != nil {
		return catalog.RecipeLine{}, err
	}
	return toRecipeLine(row)
}

// RemoveRecipeLine implements catalog.Repository.
func (r *Repository) RemoveRecipeLine(ctx context.Context, tenantID, componentID, ingredientID uuid.UUID, at time.Time) error {
	return r.db.InTx(ctx, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		if _, err := q.LockComponent(ctx, LockComponentParams{TenantID: tenantID, ID: componentID}); err != nil {
			return mapErr(err)
		}
		n, err := q.DeleteRecipeLine(ctx, DeleteRecipeLineParams{TenantID: tenantID, ComponentID: componentID, IngredientID: ingredientID})
		if err != nil {
			return mapErr(err)
		}
		if n == 0 {
			return catalog.ErrNotFound
		}
		return outbox.Append(ctx, tx, outbox.Event{
			TenantID: tenantID, Aggregate: "component", Type: recipeChanged, At: at,
			Payload: map[string]any{"component_id": componentID, "ingredient_id": ingredientID, "removed": true},
		})
	})
}

// ComponentUses implements catalog.Repository.
func (r *Repository) ComponentUses(ctx context.Context, tenantID uuid.UUID, variantIDs []uuid.UUID) ([]catalog.ComponentUse, error) {
	rows, err := r.q.ComponentsOfVariants(ctx, ComponentsOfVariantsParams{TenantID: tenantID, VariantIds: variantIDs})
	return mapRows(rows, err, func(row ComponentsOfVariantsRow) catalog.ComponentUse {
		return catalog.ComponentUse{VariantID: row.VariantID, ComponentID: row.ComponentID, UnitsPerItem: row.UnitsPerItem}
	})
}

// StoredRecipeLines implements catalog.Repository.
func (r *Repository) StoredRecipeLines(ctx context.Context, tenantID uuid.UUID, componentIDs []uuid.UUID) ([]catalog.StoredRecipeLine, error) {
	rows, err := r.q.IngredientsOfComponents(ctx, IngredientsOfComponentsParams{TenantID: tenantID, ComponentIds: componentIDs})
	return mapRows(rows, err, func(row IngredientsOfComponentsRow) catalog.StoredRecipeLine {
		return catalog.StoredRecipeLine{
			ComponentID: row.ComponentID, IngredientID: row.IngredientID, ModelType: recipe.ModelType(row.ModelType),
			Params: row.Params, WasteFactor: row.WasteFactor, Version: row.Version, BaseUnit: catalog.BaseUnit(row.BaseUnit),
		}
	})
}

// DefaultPacks implements catalog.Repository.
func (r *Repository) DefaultPacks(ctx context.Context, tenantID uuid.UUID, ingredientIDs []uuid.UUID) ([]catalog.DefaultPack, error) {
	rows, err := r.q.DefaultPacks(ctx, DefaultPacksParams{TenantID: tenantID, IngredientIds: ingredientIDs})
	return mapRows(rows, err, func(row DefaultPacksRow) catalog.DefaultPack {
		return catalog.DefaultPack{IngredientID: row.IngredientID, SupplierID: row.SupplierID, Size: row.PackSize, Unit: row.PackUnit, PriceIDR: row.PriceIdr}
	})
}

// VariantSummaries implements catalog.Repository.
func (r *Repository) VariantSummaries(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]catalog.VariantSummary, error) {
	rows, err := r.q.VariantsByIDs(ctx, VariantsByIDsParams{TenantID: tenantID, Ids: ids})
	return mapRows(rows, err, func(row VariantsByIDsRow) catalog.VariantSummary {
		return catalog.VariantSummary{
			ID: row.ID, ProductID: row.ProductID, Name: row.Name, PriceIDR: row.PriceIdr,
			ProductionMinutes: row.ProductionMinutes, MinNoticeHours: row.MinNoticeHours, Active: row.IsActive,
		}
	})
}

func toVariantComponent(row ListVariantComponentsRow) catalog.VariantComponent {
	return catalog.VariantComponent{ComponentID: row.ComponentID, UnitsPerItem: row.UnitsPerItem}
}

func toRecipeLine(row ComponentIngredient) (catalog.RecipeLine, error) {
	var pairs [][2]float64
	if err := json.Unmarshal(row.MeasuredPoints, &pairs); err != nil {
		return catalog.RecipeLine{}, fmt.Errorf("decode measured points of %s/%s: %w", row.ComponentID, row.IngredientID, err)
	}
	points := make([]recipe.Point, len(pairs))
	for i, p := range pairs {
		points[i] = recipe.Point{U: p[0], Amount: p[1]}
	}
	return catalog.RecipeLine{
		ComponentID: row.ComponentID, IngredientID: row.IngredientID, ModelType: recipe.ModelType(row.ModelType),
		Params: row.Params, Points: points, WasteFactor: row.WasteFactor, Version: row.Version, UpdatedAt: row.UpdatedAt,
	}, nil
}

// encodePoints stores points as [[units, amount], ...], or [] when there are none.
func encodePoints(points []recipe.Point) ([]byte, error) {
	pairs := make([][2]float64, len(points))
	for i, p := range points {
		pairs[i] = [2]float64{p.U, p.Amount}
	}
	b, err := json.Marshal(pairs)
	if err != nil {
		return nil, fmt.Errorf("encode measured points: %w", err)
	}
	return b, nil
}

func mapSlice[R, T any](rows []R, to func(R) T) []T {
	out := make([]T, len(rows))
	for i, row := range rows {
		out[i] = to(row)
	}
	return out
}
