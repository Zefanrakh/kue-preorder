//go:build integration

package connect_test

import (
	"maps"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	catalogv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/catalog/v1"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/identity/identitytest"
)

func TestStorefrontAPI(t *testing.T) {
	s := newServer(t)
	boss := s.as(t, identity.RoleOwner)
	donut := call(t, boss.CreateProduct, &catalogv1.CreateProductRequest{Name: "Donut", Slug: "donut", Description: "Empuk", Active: true}).GetProduct()
	coklat := call(t, boss.CreateVariant, &catalogv1.CreateVariantRequest{
		ProductId: donut.GetId(), Sku: "DNT-CKL", Name: "Coklat", Options: map[string]string{"rasa": "coklat"},
		PriceIdr: 8000, ProductionMinutes: 90, MinNoticeHours: 24, Active: true,
	}).GetVariant()
	call(t, boss.CreateVariant, &catalogv1.CreateVariantRequest{ProductId: donut.GetId(), Sku: "DNT-OLD", Name: "Lama", PriceIdr: 1000, ProductionMinutes: 60})
	lapis := call(t, boss.CreateProduct, &catalogv1.CreateProductRequest{Name: "Lapis", Slug: "lapis"}).GetProduct()
	call(t, boss.CreateVariant, &catalogv1.CreateVariantRequest{ProductId: lapis.GetId(), Sku: "LPS", Name: "Legit", PriceIdr: 300000, ProductionMinutes: 240, Active: true})

	anonymous := s.shop("")
	products := call(t, anonymous.ListShopProducts, &catalogv1.ListShopProductsRequest{}).GetProducts()

	if s.lastMethod() != http.MethodGet {
		t.Errorf("request method = %s, want GET: storefront reads must be cacheable", s.lastMethod())
	}
	if len(products) != 1 || products[0].GetId() != donut.GetId() || products[0].GetDescription() != "Empuk" {
		t.Fatalf("ListShopProducts() = %v, want only the active donut", products)
	}
	variants := products[0].GetVariants()
	if len(variants) != 1 || variants[0].GetId() != coklat.GetId() || variants[0].GetPriceIdr() != 8000 ||
		variants[0].GetMinNoticeHours() != 24 || !maps.Equal(variants[0].GetOptions(), map[string]string{"rasa": "coklat"}) {
		t.Errorf("variants = %v, want only the active Coklat at 8000", variants)
	}

	// A signed-in customer sees the same shop.
	customer := s.shop("Bearer " + s.issuer.Sign(t, identitytest.Claims(uuid.New(), now)))
	if got := call(t, customer.GetShopProduct, &catalogv1.GetShopProductRequest{Slug: "Donut"}).GetProduct(); got.GetId() != donut.GetId() || len(got.GetVariants()) != 1 {
		t.Errorf("GetShopProduct(Donut) = %v, want the donut with one variant", got)
	}
	for _, slug := range []string{"lapis", "bolu", ""} {
		if _, err := anonymous.GetShopProduct(t.Context(), connect.NewRequest(&catalogv1.GetShopProductRequest{Slug: slug})); connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("GetShopProduct(%q) code = %v, want NotFound", slug, connect.CodeOf(err))
		}
	}

	// A broken session fails instead of quietly turning anonymous.
	_, err := s.shop("Bearer not-a-jwt").ListShopProducts(t.Context(), connect.NewRequest(&catalogv1.ListShopProductsRequest{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("ListShopProducts(broken token) code = %v, want Unauthenticated", connect.CodeOf(err))
	}
}
