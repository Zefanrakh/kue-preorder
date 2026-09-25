package catalog

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Repository is the persistence port of the catalog. Every method is scoped
// to one tenant; a record of another tenant is ErrNotFound. Unique and
// foreign-key clashes come back as *ValidationError naming the field.
type Repository interface {
	ListProducts(ctx context.Context, tenantID uuid.UUID) ([]Product, error)
	CreateProduct(ctx context.Context, tenantID uuid.UUID, in ProductInput, at time.Time) (Product, error)
	UpdateProduct(ctx context.Context, tenantID, id uuid.UUID, in ProductInput, at time.Time) (Product, error)

	ListVariants(ctx context.Context, tenantID, productID uuid.UUID) ([]Variant, error)
	CreateVariant(ctx context.Context, tenantID, productID uuid.UUID, in VariantInput, priceIDR int64, at time.Time) (Variant, error)
	UpdateVariant(ctx context.Context, tenantID, id uuid.UUID, in VariantInput, at time.Time) (Variant, error)
	// ChangeVariantPrice sets the price and writes its audit entry in one
	// transaction. Setting the current price again changes and records nothing.
	ChangeVariantPrice(ctx context.Context, c PriceChange) (Variant, error)

	ListComponents(ctx context.Context, tenantID uuid.UUID) ([]Component, error)
	CreateComponent(ctx context.Context, tenantID uuid.UUID, in ComponentInput, at time.Time) (Component, error)
	UpdateComponent(ctx context.Context, tenantID, id uuid.UUID, in ComponentInput, at time.Time) (Component, error)

	ListIngredients(ctx context.Context, tenantID uuid.UUID) ([]Ingredient, error)
	CreateIngredient(ctx context.Context, tenantID uuid.UUID, in IngredientInput, at time.Time) (Ingredient, error)
	UpdateIngredient(ctx context.Context, tenantID, id uuid.UUID, in IngredientInput, at time.Time) (Ingredient, error)

	ListSuppliers(ctx context.Context, tenantID uuid.UUID) ([]Supplier, error)
	CreateSupplier(ctx context.Context, tenantID uuid.UUID, in SupplierInput, at time.Time) (Supplier, error)
	UpdateSupplier(ctx context.Context, tenantID, id uuid.UUID, in SupplierInput, at time.Time) (Supplier, error)

	ListPacks(ctx context.Context, tenantID, ingredientID uuid.UUID) ([]Pack, error)
	CreatePack(ctx context.Context, tenantID, ingredientID uuid.UUID, in PackInput, at time.Time) (Pack, error)
	UpdatePack(ctx context.Context, tenantID, id uuid.UUID, in PackInput, at time.Time) (Pack, error)
	// SetDefaultPack makes the pack its ingredient's only default, in one transaction.
	SetDefaultPack(ctx context.Context, tenantID, id uuid.UUID, at time.Time) (Pack, error)

	// Recipe writes lock the variant or component, and on a real change write
	// a catalog.recipe_changed outbox event in the same transaction. Writing
	// what is already stored changes nothing and emits nothing.
	ListVariantComponents(ctx context.Context, tenantID, variantID uuid.UUID) ([]VariantComponent, error)
	SetVariantComponents(ctx context.Context, tenantID, variantID uuid.UUID, uses []VariantComponent, at time.Time) ([]VariantComponent, error)
	ListRecipeLines(ctx context.Context, tenantID, componentID uuid.UUID) ([]RecipeLine, error)
	SetRecipeLine(ctx context.Context, tenantID uuid.UUID, w RecipeLineWrite, at time.Time) (RecipeLine, error)
	RemoveRecipeLine(ctx context.Context, tenantID, componentID, ingredientID uuid.UUID, at time.Time) error

	// Reads for other modules, through Reader.
	ComponentUses(ctx context.Context, tenantID uuid.UUID, variantIDs []uuid.UUID) ([]ComponentUse, error)
	StoredRecipeLines(ctx context.Context, tenantID uuid.UUID, componentIDs []uuid.UUID) ([]StoredRecipeLine, error)
	DefaultPacks(ctx context.Context, tenantID uuid.UUID, ingredientIDs []uuid.UUID) ([]DefaultPack, error)
	VariantSummaries(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]VariantSummary, error)
}
