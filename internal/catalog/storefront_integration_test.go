//go:build integration

package catalog_test

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/catalog/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
)

// tenantOf resolves every request to one tenant.
type tenantOf uuid.UUID

func (t tenantOf) TenantID(context.Context) uuid.UUID { return uuid.UUID(t) }

// forSale creates a product with variants through the CMS; a variant name
// starting with "-" is inactive.
func forSale(t *testing.T, s *catalog.Service, in catalog.ProductInput, variants map[string]int64) (catalog.Product, map[string]catalog.Variant) {
	t.Helper()
	p, err := s.CreateProduct(t.Context(), in)
	noErr(t, err)
	out := map[string]catalog.Variant{}
	for name, price := range variants {
		active := name[0] != '-'
		v, err := s.CreateVariant(t.Context(), p.ID, catalog.VariantInput{
			SKU: in.Slug + "-" + uuid.NewString()[:8], Name: name, Options: map[string]string{"rasa": name},
			ProductionMinutes: 60, MinNoticeHours: 24, Active: active,
		}, price)
		noErr(t, err)
		out[name] = v
	}
	return p, out
}

func TestStorefront_ShowsOnlyWhatIsOnSale(t *testing.T) {
	d := dbtest.New(t)
	boss := owner(dbtest.DefaultTenantID).service(d)
	ctx := t.Context()

	donut, dv := forSale(t, boss, catalog.ProductInput{Name: "Donut", Slug: "donut", Description: "Empuk", ImagePath: "products/donut.jpg", Active: true},
		map[string]int64{"Coklat": 8000, "Gula": 5000, "-Lama": 1000})
	forSale(t, boss, catalog.ProductInput{Name: "Lapis", Slug: "lapis", Active: true}, map[string]int64{"-Legit": 300000})
	forSale(t, boss, catalog.ProductInput{Name: "Bolu", Slug: "bolu", Active: false}, map[string]int64{"Pandan": 60000})
	forSale(t, boss, catalog.ProductInput{Name: "Kue Kosong", Slug: "kue-kosong", Active: true}, nil)
	other := dbtest.CreateTenant(t, d, "Toko Lain")
	forSale(t, owner(other).service(d), catalog.ProductInput{Name: "Donut", Slug: "donut", Active: true}, map[string]int64{"Keju": 7000})
	shop := catalog.NewStorefront(postgres.NewRepository(d), tenantOf(dbtest.DefaultTenantID))

	products, err := shop.Products(ctx)
	noErr(t, err)

	if len(products) != 1 || products[0].ID != donut.ID {
		t.Fatalf("Products() = %+v, want only the donut: active, with an active variant, of this tenant", products)
	}
	p := products[0]
	if p.Name != "Donut" || p.Slug != "donut" || p.Description != "Empuk" || p.ImagePath != "products/donut.jpg" {
		t.Errorf("product = %+v", p)
	}
	if len(p.Variants) != 2 || p.Variants[0].ID != dv["Gula"].ID || p.Variants[1].ID != dv["Coklat"].ID {
		t.Fatalf("variants = %+v, want the two active ones, cheapest first", p.Variants)
	}
	v := p.Variants[0]
	if v.Name != "Gula" || v.PriceIDR != 5000 || v.MinNoticeHours != 24 || !maps.Equal(v.Options, map[string]string{"rasa": "Gula"}) {
		t.Errorf("variant = %+v", v)
	}
}

func TestStorefront_Product(t *testing.T) {
	d := dbtest.New(t)
	boss := owner(dbtest.DefaultTenantID).service(d)
	donut, _ := forSale(t, boss, catalog.ProductInput{Name: "Donut", Slug: "donut", Active: true}, map[string]int64{"Coklat": 8000, "-Lama": 1000})
	forSale(t, boss, catalog.ProductInput{Name: "Lapis", Slug: "lapis", Active: true}, map[string]int64{"-Legit": 300000})
	forSale(t, boss, catalog.ProductInput{Name: "Bolu", Slug: "bolu", Active: false}, map[string]int64{"Pandan": 60000})
	shop := catalog.NewStorefront(postgres.NewRepository(d), tenantOf(dbtest.DefaultTenantID))

	for _, slug := range []string{"donut", " Donut "} {
		p, err := shop.Product(t.Context(), slug)
		if err != nil || p.ID != donut.ID || len(p.Variants) != 1 {
			t.Errorf("Product(%q) = %+v, %v; want the donut with its one active variant", slug, p, err)
		}
	}
	for _, slug := range []string{"lapis", "bolu", "kue-kosong", "", "../donut", "donut'; drop table products; --"} {
		if _, err := shop.Product(t.Context(), slug); !errors.Is(err, catalog.ErrNotFound) {
			t.Errorf("Product(%q) error = %v, want ErrNotFound", slug, err)
		}
	}
}
