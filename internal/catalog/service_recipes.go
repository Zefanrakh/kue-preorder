package catalog

import (
	"context"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/recipe"
)

// Recipes are the kitchen's domain: owners and the kitchen both edit them (§8).

// ListVariantComponents returns the components one item of a variant uses. Staff only.
func (s *Service) ListVariantComponents(ctx context.Context, variantID uuid.UUID) ([]VariantComponent, error) {
	p, err := s.authorize(ctx, staff)
	if err != nil {
		return nil, err
	}
	return s.repo.ListVariantComponents(ctx, p.TenantID, variantID)
}

// SetVariantComponents replaces the components of a variant in one
// transaction and returns them. Owners and the kitchen.
func (s *Service) SetVariantComponents(ctx context.Context, variantID uuid.UUID, uses []VariantComponent) ([]VariantComponent, error) {
	p, err := s.authorize(ctx, staff)
	if err != nil {
		return nil, err
	}
	if err := validateVariantComponents(uses); err != nil {
		return nil, err
	}
	return s.repo.SetVariantComponents(ctx, p.TenantID, variantID, uses, s.clock.Now())
}

// ListRecipeLines returns the recipe lines of a component. Staff only.
func (s *Service) ListRecipeLines(ctx context.Context, componentID uuid.UUID) ([]RecipeLine, error) {
	p, err := s.authorize(ctx, staff)
	if err != nil {
		return nil, err
	}
	return s.repo.ListRecipeLines(ctx, p.TenantID, componentID)
}

// SetRecipeLine creates or replaces how much of an ingredient a component
// needs, from a model or from measured points. The model must pass the
// recipe engine's validation. Owners and the kitchen.
func (s *Service) SetRecipeLine(ctx context.Context, componentID, ingredientID uuid.UUID, in RecipeLineInput) (RecipeLine, error) {
	p, err := s.authorize(ctx, staff)
	if err != nil {
		return RecipeLine{}, err
	}
	w, err := in.resolve(componentID, ingredientID)
	if err != nil {
		return RecipeLine{}, err
	}
	return s.repo.SetRecipeLine(ctx, p.TenantID, w, s.clock.Now())
}

// RemoveRecipeLine deletes an ingredient from a component's recipe. Owners
// and the kitchen.
func (s *Service) RemoveRecipeLine(ctx context.Context, componentID, ingredientID uuid.UUID) error {
	p, err := s.authorize(ctx, staff)
	if err != nil {
		return err
	}
	return s.repo.RemoveRecipeLine(ctx, p.TenantID, componentID, ingredientID, s.clock.Now())
}

// FitPreview fits every fittable model to measured points for the CMS chart,
// without saving anything. Staff only.
func (s *Service) FitPreview(ctx context.Context, points []recipe.Point) ([]recipe.FitResult, error) {
	if _, err := s.authorize(ctx, staff); err != nil {
		return nil, err
	}
	return recipe.FitAll(points), nil
}
