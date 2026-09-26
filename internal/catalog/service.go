package catalog

import (
	"context"
	"slices"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

// PrincipalSource tells who is calling; identity.Service implements it.
type PrincipalSource interface {
	Principal(ctx context.Context) (identity.Principal, error)
}

// Who may do what (§8).
var (
	owners = []identity.Role{identity.RoleOwner}
	staff  = []identity.Role{identity.RoleOwner, identity.RoleKitchen}
)

// Service is the catalog's application service. Every method authorizes the
// caller, normalizes and validates input, and works in the caller's tenant.
type Service struct {
	repo       Repository
	principals PrincipalSource
	clock      clock.Clock
}

// NewService returns a Service storing through repo.
func NewService(repo Repository, principals PrincipalSource, clk clock.Clock) *Service {
	return &Service{repo: repo, principals: principals, clock: clk}
}

// authorize returns the caller if they hold one of roles. It returns
// identity.ErrUnauthenticated without a signed-in user and ErrForbidden
// without a fitting role.
func (s *Service) authorize(ctx context.Context, roles []identity.Role) (identity.Principal, error) {
	p, err := s.principals.Principal(ctx)
	if err != nil {
		return identity.Principal{}, err
	}
	if !slices.ContainsFunc(roles, p.HasRole) {
		return identity.Principal{}, ErrForbidden
	}
	return p, nil
}

// ListProducts returns every product, active or not. Staff only.
func (s *Service) ListProducts(ctx context.Context) ([]Product, error) {
	p, err := s.authorize(ctx, staff)
	if err != nil {
		return nil, err
	}
	return s.repo.ListProducts(ctx, p.TenantID)
}

// CreateProduct adds a product. Owners only.
func (s *Service) CreateProduct(ctx context.Context, in ProductInput) (Product, error) {
	p, err := s.authorize(ctx, owners)
	if err != nil {
		return Product{}, err
	}
	in = in.normalize()
	if err := in.validate(); err != nil {
		return Product{}, err
	}
	return s.repo.CreateProduct(ctx, p.TenantID, in, s.clock.Now())
}

// UpdateProduct replaces a product's editable fields. Owners only.
func (s *Service) UpdateProduct(ctx context.Context, id uuid.UUID, in ProductInput) (Product, error) {
	p, err := s.authorize(ctx, owners)
	if err != nil {
		return Product{}, err
	}
	in = in.normalize()
	if err := in.validate(); err != nil {
		return Product{}, err
	}
	return s.repo.UpdateProduct(ctx, p.TenantID, id, in, s.clock.Now())
}

// ListVariants returns the variants of a product. Staff only.
func (s *Service) ListVariants(ctx context.Context, productID uuid.UUID) ([]Variant, error) {
	p, err := s.authorize(ctx, staff)
	if err != nil {
		return nil, err
	}
	return s.repo.ListVariants(ctx, p.TenantID, productID)
}

// CreateVariant adds a variant of a product with its first price. Owners only.
func (s *Service) CreateVariant(ctx context.Context, productID uuid.UUID, in VariantInput, priceIDR int64) (Variant, error) {
	p, err := s.authorize(ctx, owners)
	if err != nil {
		return Variant{}, err
	}
	in = in.normalize()
	if err := joinValidation(requireID(productID, "product_id", "Pilih produknya."), in.validate(), validatePrice(priceIDR)); err != nil {
		return Variant{}, err
	}
	return s.repo.CreateVariant(ctx, p.TenantID, productID, in, priceIDR, s.clock.Now())
}

// UpdateVariant replaces a variant's editable fields; the price and the
// product stay. Owners only.
func (s *Service) UpdateVariant(ctx context.Context, id uuid.UUID, in VariantInput) (Variant, error) {
	p, err := s.authorize(ctx, owners)
	if err != nil {
		return Variant{}, err
	}
	in = in.normalize()
	if err := in.validate(); err != nil {
		return Variant{}, err
	}
	return s.repo.UpdateVariant(ctx, p.TenantID, id, in, s.clock.Now())
}

// ChangeVariantPrice sets a new price and records who changed it, when, from
// what, and why (§22). The reason is required. Owners only.
func (s *Service) ChangeVariantPrice(ctx context.Context, id uuid.UUID, priceIDR int64, reason string) (Variant, error) {
	p, err := s.authorize(ctx, owners)
	if err != nil {
		return Variant{}, err
	}
	c := PriceChange{TenantID: p.TenantID, VariantID: id, ActorID: p.AuthUserID, PriceIDR: priceIDR, Reason: reason, At: s.clock.Now()}
	if err := c.validate(); err != nil {
		return Variant{}, err
	}
	return s.repo.ChangeVariantPrice(ctx, c)
}

// ListComponents returns every component. Staff only.
func (s *Service) ListComponents(ctx context.Context) ([]Component, error) {
	p, err := s.authorize(ctx, staff)
	if err != nil {
		return nil, err
	}
	return s.repo.ListComponents(ctx, p.TenantID)
}

// CreateComponent adds a component. Owners and the kitchen.
func (s *Service) CreateComponent(ctx context.Context, in ComponentInput) (Component, error) {
	p, err := s.authorize(ctx, staff)
	if err != nil {
		return Component{}, err
	}
	in = in.normalize()
	if err := in.validate(); err != nil {
		return Component{}, err
	}
	return s.repo.CreateComponent(ctx, p.TenantID, in, s.clock.Now())
}

// UpdateComponent replaces a component's editable fields. Owners and the kitchen.
func (s *Service) UpdateComponent(ctx context.Context, id uuid.UUID, in ComponentInput) (Component, error) {
	p, err := s.authorize(ctx, staff)
	if err != nil {
		return Component{}, err
	}
	in = in.normalize()
	if err := in.validate(); err != nil {
		return Component{}, err
	}
	return s.repo.UpdateComponent(ctx, p.TenantID, id, in, s.clock.Now())
}

// ListIngredients returns every ingredient. Staff only.
func (s *Service) ListIngredients(ctx context.Context) ([]Ingredient, error) {
	p, err := s.authorize(ctx, staff)
	if err != nil {
		return nil, err
	}
	return s.repo.ListIngredients(ctx, p.TenantID)
}

// CreateIngredient adds an ingredient. Owners and the kitchen.
func (s *Service) CreateIngredient(ctx context.Context, in IngredientInput) (Ingredient, error) {
	p, err := s.authorize(ctx, staff)
	if err != nil {
		return Ingredient{}, err
	}
	in = in.normalize()
	if err := in.validate(); err != nil {
		return Ingredient{}, err
	}
	return s.repo.CreateIngredient(ctx, p.TenantID, in, s.clock.Now())
}

// UpdateIngredient replaces an ingredient's editable fields. Owners and the kitchen.
func (s *Service) UpdateIngredient(ctx context.Context, id uuid.UUID, in IngredientInput) (Ingredient, error) {
	p, err := s.authorize(ctx, staff)
	if err != nil {
		return Ingredient{}, err
	}
	in = in.normalize()
	if err := in.validate(); err != nil {
		return Ingredient{}, err
	}
	return s.repo.UpdateIngredient(ctx, p.TenantID, id, in, s.clock.Now())
}

// ListSuppliers returns every supplier. Staff only.
func (s *Service) ListSuppliers(ctx context.Context) ([]Supplier, error) {
	p, err := s.authorize(ctx, staff)
	if err != nil {
		return nil, err
	}
	return s.repo.ListSuppliers(ctx, p.TenantID)
}

// CreateSupplier adds a supplier. Owners only.
func (s *Service) CreateSupplier(ctx context.Context, in SupplierInput) (Supplier, error) {
	p, err := s.authorize(ctx, owners)
	if err != nil {
		return Supplier{}, err
	}
	in = in.normalize()
	if err := in.validate(); err != nil {
		return Supplier{}, err
	}
	return s.repo.CreateSupplier(ctx, p.TenantID, in, s.clock.Now())
}

// UpdateSupplier replaces a supplier's editable fields. Owners only.
func (s *Service) UpdateSupplier(ctx context.Context, id uuid.UUID, in SupplierInput) (Supplier, error) {
	p, err := s.authorize(ctx, owners)
	if err != nil {
		return Supplier{}, err
	}
	in = in.normalize()
	if err := in.validate(); err != nil {
		return Supplier{}, err
	}
	return s.repo.UpdateSupplier(ctx, p.TenantID, id, in, s.clock.Now())
}

// ListPacks returns the packs an ingredient is sold in, the default first. Staff only.
func (s *Service) ListPacks(ctx context.Context, ingredientID uuid.UUID) ([]Pack, error) {
	p, err := s.authorize(ctx, staff)
	if err != nil {
		return nil, err
	}
	return s.repo.ListPacks(ctx, p.TenantID, ingredientID)
}

// CreatePack adds a pack of an ingredient, not yet the default. Owners only.
func (s *Service) CreatePack(ctx context.Context, ingredientID uuid.UUID, in PackInput) (Pack, error) {
	p, err := s.authorize(ctx, owners)
	if err != nil {
		return Pack{}, err
	}
	in = in.normalize()
	if err := joinValidation(requireID(ingredientID, "ingredient_id", "Pilih bahannya."), in.validate()); err != nil {
		return Pack{}, err
	}
	return s.repo.CreatePack(ctx, p.TenantID, ingredientID, in, s.clock.Now())
}

// UpdatePack replaces a pack's editable fields; its ingredient stays. Owners only.
func (s *Service) UpdatePack(ctx context.Context, id uuid.UUID, in PackInput) (Pack, error) {
	p, err := s.authorize(ctx, owners)
	if err != nil {
		return Pack{}, err
	}
	in = in.normalize()
	if err := in.validate(); err != nil {
		return Pack{}, err
	}
	return s.repo.UpdatePack(ctx, p.TenantID, id, in, s.clock.Now())
}

// SetDefaultPack makes a pack the one its ingredient's shopping list rounds
// to, replacing any other default. Owners only.
func (s *Service) SetDefaultPack(ctx context.Context, id uuid.UUID) (Pack, error) {
	p, err := s.authorize(ctx, owners)
	if err != nil {
		return Pack{}, err
	}
	return s.repo.SetDefaultPack(ctx, p.TenantID, id, s.clock.Now())
}

// requireID checks that a parent record was chosen.
func requireID(id uuid.UUID, field, msg string) error {
	f := fields{}
	f.Check(id != uuid.Nil, field, msg)
	return f.Err()
}

// joinValidation merges the field errors of several checks into one.
func joinValidation(errs ...error) error {
	return apperr.Join(errs...)
}
