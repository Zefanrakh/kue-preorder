package catalog

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/identity"
)

// ShopProduct is a product as customers see it: on sale, with the variants
// they can buy, cheapest first. Nothing about how it is made shows.
type ShopProduct struct {
	ID          uuid.UUID
	Name        string
	Slug        string
	Description string
	ImagePath   string // Supabase Storage path; empty when there is no photo
	Variants    []ShopVariant
}

// ShopVariant is a variant as customers see it.
type ShopVariant struct {
	ID             uuid.UUID
	Name           string
	Options        map[string]string
	PriceIDR       int64
	MinNoticeHours int32 // order to pickup, shopping time included (§15)
}

// Storefront serves the public shop. Anyone may read it, signed in or not;
// the tenant comes from the request, not from a person. A product is on sale
// when it is active and has at least one active variant, and only active
// variants show.
type Storefront struct {
	repo    Repository
	tenants identity.TenantResolver
}

// NewStorefront returns a Storefront reading through repo.
func NewStorefront(repo Repository, tenants identity.TenantResolver) *Storefront {
	return &Storefront{repo: repo, tenants: tenants}
}

// Products returns every product on sale, by name.
func (s *Storefront) Products(ctx context.Context) ([]ShopProduct, error) {
	return s.repo.ShopCatalog(ctx, s.tenants.TenantID(ctx))
}

// Product returns the product on sale at slug, or ErrNotFound. Slugs are
// matched case-insensitively, as a customer may type one into the address bar.
func (s *Storefront) Product(ctx context.Context, slug string) (ShopProduct, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if !slugPattern.MatchString(slug) {
		return ShopProduct{}, ErrNotFound
	}
	return s.repo.ShopProduct(ctx, s.tenants.TenantID(ctx), slug)
}
