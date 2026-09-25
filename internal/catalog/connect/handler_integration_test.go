//go:build integration

package connect_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	catalogv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/catalog/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/catalog/v1/catalogv1connect"
	validationv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/validation/v1"
	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	catalogrpc "github.com/Zefanrakh/kue-preorder/internal/catalog/connect"
	"github.com/Zefanrakh/kue-preorder/internal/catalog/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
	identityrpc "github.com/Zefanrakh/kue-preorder/internal/identity/connect"
	"github.com/Zefanrakh/kue-preorder/internal/identity/identitytest"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

var now = time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)

type server struct {
	url    string
	http   *http.Client
	issuer *identitytest.TokenIssuer
	roles  *identitytest.Repository
}

// newServer serves the catalog as cmd/api does, with the real auth
// interceptor, over HTTP, backed by a fresh database. Roles live in memory.
func newServer(t *testing.T) *server {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	roles := identitytest.NewRepository(dbtest.DefaultTenantID)
	issuer := identitytest.NewTokenIssuer(t)
	keys, err := identity.NewJWKS(t.Context(), issuer.JWKSURL, logger)
	if err != nil {
		t.Fatalf("NewJWKS() error = %v", err)
	}
	tenants, err := identity.ResolveSingleTenant(t.Context(), roles)
	if err != nil {
		t.Fatalf("ResolveSingleTenant() error = %v", err)
	}
	verifier := identity.NewTokenVerifier(keys, identitytest.Issuer, clock.NewFake(now))
	svc := catalog.NewService(postgres.NewRepository(dbtest.New(t)), identity.NewService(roles, tenants), clock.NewFake(now))

	mux := http.NewServeMux()
	mux.Handle(catalogv1connect.NewCatalogAdminServiceHandler(catalogrpc.NewHandler(svc, logger),
		connect.WithInterceptors(identityrpc.NewAuthInterceptor(verifier, logger))))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &server{url: srv.URL, http: srv.Client(), issuer: issuer, roles: roles}
}

// as returns a client signed in as a new user holding roles; with no roles,
// a user who is not staff.
func (s *server) as(t *testing.T, roles ...identity.Role) catalogv1connect.CatalogAdminServiceClient {
	t.Helper()
	user := uuid.New()
	if len(roles) > 0 {
		s.roles.Grant(dbtest.DefaultTenantID, user, roles...)
	}
	return s.withAuthorization("Bearer " + s.issuer.Sign(t, identitytest.Claims(user, now)))
}

func (s *server) withAuthorization(value string) catalogv1connect.CatalogAdminServiceClient {
	header := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if value != "" {
				req.Header().Set("Authorization", value)
			}
			return next(ctx, req)
		}
	})
	return catalogv1connect.NewCatalogAdminServiceClient(s.http, s.url, connect.WithInterceptors(header))
}

func call[Req, Res any](t *testing.T, rpc func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error), msg *Req) *Res {
	t.Helper()
	res, err := rpc(t.Context(), connect.NewRequest(msg))
	if err != nil {
		t.Fatalf("%T: %v", msg, err)
	}
	return res.Msg
}

// fieldErrors checks the code of err and returns its FieldErrors detail, as
// a client reads it.
func fieldErrors(t *testing.T, err error, want connect.Code) map[string]string {
	t.Helper()
	var cerr *connect.Error
	if !errors.As(err, &cerr) || cerr.Code() != want {
		t.Fatalf("error = %v, want %v", err, want)
	}
	for _, d := range cerr.Details() {
		v, derr := d.Value()
		if derr != nil {
			t.Fatalf("read detail: %v", derr)
		}
		if fe, ok := v.(*validationv1.FieldErrors); ok {
			return fe.GetFields()
		}
	}
	t.Fatalf("error %v has no FieldErrors detail", err)
	return nil
}

func ptr[T any](v T) *T { return &v }

// decode reads model params; the API promises JSON, not its formatting.
func decode(t *testing.T, params string) map[string]float64 {
	t.Helper()
	var out map[string]float64
	if err := json.Unmarshal([]byte(params), &out); err != nil {
		t.Fatalf("params %q: %v", params, err)
	}
	return out
}

func TestCatalogAPI_ProductsAndVariants(t *testing.T) {
	s := newServer(t)
	boss := s.as(t, identity.RoleOwner)

	product := call(t, boss.CreateProduct, &catalogv1.CreateProductRequest{Name: " Donut ", Slug: "Donut", Active: true}).GetProduct()
	if product.GetName() != "Donut" || product.GetSlug() != "donut" || !product.GetUpdatedAt().AsTime().Equal(now) {
		t.Errorf("CreateProduct() = %v, want the normalized product stamped %v", product, now)
	}
	product = call(t, boss.UpdateProduct, &catalogv1.UpdateProductRequest{Id: product.GetId(), Name: "Donut Kentang", Slug: "donut-kentang"}).GetProduct()
	if product.GetName() != "Donut Kentang" || product.GetActive() {
		t.Errorf("UpdateProduct() = %v, want renamed and inactive", product)
	}
	if got := call(t, boss.ListProducts, &catalogv1.ListProductsRequest{}).GetProducts(); len(got) != 1 || got[0].GetId() != product.GetId() {
		t.Errorf("ListProducts() = %v, want the one product", got)
	}

	variant := call(t, boss.CreateVariant, &catalogv1.CreateVariantRequest{
		ProductId: product.GetId(), Sku: "dnt-ckl", Name: "Coklat", Options: map[string]string{"rasa": "coklat"},
		PriceIdr: 8000, ProductionMinutes: 90, MinNoticeHours: 24, Active: true,
	}).GetVariant()
	if variant.GetSku() != "DNT-CKL" || variant.GetPriceIdr() != 8000 || variant.GetProductId() != product.GetId() ||
		!maps.Equal(variant.GetOptions(), map[string]string{"rasa": "coklat"}) {
		t.Errorf("CreateVariant() = %v", variant)
	}
	variant = call(t, boss.UpdateVariant, &catalogv1.UpdateVariantRequest{
		Id: variant.GetId(), Sku: "DNT-CKL", Name: "Coklat Keju", ProductionMinutes: 120, MinNoticeHours: 48, Active: true,
	}).GetVariant()
	if variant.GetName() != "Coklat Keju" || variant.GetPriceIdr() != 8000 || len(variant.GetOptions()) != 0 {
		t.Errorf("UpdateVariant() = %v, want renamed, options cleared, price kept", variant)
	}
	variant = call(t, boss.ChangeVariantPrice, &catalogv1.ChangeVariantPriceRequest{Id: variant.GetId(), PriceIdr: 9000, Reason: "Harga coklat naik"}).GetVariant()
	if variant.GetPriceIdr() != 9000 {
		t.Errorf("ChangeVariantPrice() price = %d, want 9000", variant.GetPriceIdr())
	}
	if got := call(t, boss.ListVariants, &catalogv1.ListVariantsRequest{ProductId: product.GetId()}).GetVariants(); len(got) != 1 || got[0].GetPriceIdr() != 9000 {
		t.Errorf("ListVariants() = %v, want the one variant at 9000", got)
	}
}

func TestCatalogAPI_IngredientsSuppliersAndPacks(t *testing.T) {
	s := newServer(t)
	boss := s.as(t, identity.RoleOwner)

	butter := call(t, boss.CreateIngredient, &catalogv1.CreateIngredientRequest{
		Name: "Mentega", BaseUnit: catalogv1.BaseUnit_BASE_UNIT_GRAM, Perishable: true, ShelfLifeDays: ptr[int32](30),
	}).GetIngredient()
	if butter.GetBaseUnit() != catalogv1.BaseUnit_BASE_UNIT_GRAM || butter.GetShelfLifeDays() != 30 ||
		butter.GetLeftoverPolicy() != catalogv1.LeftoverPolicy_LEFTOVER_POLICY_CONFIRM {
		t.Errorf("CreateIngredient() = %v, want grams, 30 days, and confirm (the perishable default)", butter)
	}
	butter = call(t, boss.UpdateIngredient, &catalogv1.UpdateIngredientRequest{
		Id: butter.GetId(), Name: "Mentega", BaseUnit: catalogv1.BaseUnit_BASE_UNIT_GRAM, Perishable: true,
		LeftoverPolicy: catalogv1.LeftoverPolicy_LEFTOVER_POLICY_NEVER,
	}).GetIngredient()
	if butter.ShelfLifeDays != nil || butter.GetLeftoverPolicy() != catalogv1.LeftoverPolicy_LEFTOVER_POLICY_NEVER {
		t.Errorf("UpdateIngredient() = %v, want no shelf life and never", butter)
	}
	if got := call(t, boss.ListIngredients, &catalogv1.ListIngredientsRequest{}).GetIngredients(); len(got) != 1 {
		t.Errorf("ListIngredients() = %v, want one", got)
	}

	shop := call(t, boss.CreateSupplier, &catalogv1.CreateSupplierRequest{Name: "Toko Bahan", WhatsappPhone: "0812-3456-789"}).GetSupplier()
	if shop.GetWhatsappPhone() != "+628123456789" || shop.GetAdapter() != catalogv1.ProcurementAdapter_PROCUREMENT_ADAPTER_MANUAL {
		t.Errorf("CreateSupplier() = %v, want E.164 and manual (the default)", shop)
	}
	shop = call(t, boss.UpdateSupplier, &catalogv1.UpdateSupplierRequest{
		Id: shop.GetId(), Name: "Toko Bahan", WhatsappPhone: shop.GetWhatsappPhone(), Adapter: catalogv1.ProcurementAdapter_PROCUREMENT_ADAPTER_WHATSAPP,
	}).GetSupplier()
	if shop.GetAdapter() != catalogv1.ProcurementAdapter_PROCUREMENT_ADAPTER_WHATSAPP {
		t.Errorf("UpdateSupplier() adapter = %v, want whatsapp", shop.GetAdapter())
	}
	if got := call(t, boss.ListSuppliers, &catalogv1.ListSuppliersRequest{}).GetSuppliers(); len(got) != 1 {
		t.Errorf("ListSuppliers() = %v, want one", got)
	}

	small := call(t, boss.CreatePack, &catalogv1.CreatePackRequest{
		IngredientId: butter.GetId(), SupplierId: shop.GetId(), Size: 200, Unit: "blok 200 g", PriceIdr: ptr[int64](15000),
	}).GetPack()
	if small.GetIngredientId() != butter.GetId() || small.GetPriceIdr() != 15000 || small.GetIsDefault() {
		t.Errorf("CreatePack() = %v", small)
	}
	big := call(t, boss.CreatePack, &catalogv1.CreatePackRequest{IngredientId: butter.GetId(), SupplierId: shop.GetId(), Size: 1000, Unit: "blok 1 kg"}).GetPack()
	if big.PriceIdr != nil {
		t.Errorf("CreatePack() price = %d, want unset", big.GetPriceIdr())
	}
	big = call(t, boss.UpdatePack, &catalogv1.UpdatePackRequest{Id: big.GetId(), SupplierId: shop.GetId(), Size: 1000, Unit: "blok 1 kg", PriceIdr: ptr[int64](70000)}).GetPack()
	if big.GetPriceIdr() != 70000 {
		t.Errorf("UpdatePack() price = %d, want 70000", big.GetPriceIdr())
	}
	if got := call(t, boss.SetDefaultPack, &catalogv1.SetDefaultPackRequest{Id: big.GetId()}).GetPack(); !got.GetIsDefault() {
		t.Errorf("SetDefaultPack() = %v, want default", got)
	}
	packs := call(t, boss.ListPacks, &catalogv1.ListPacksRequest{IngredientId: butter.GetId()}).GetPacks()
	if len(packs) != 2 || packs[0].GetId() != big.GetId() || !packs[0].GetIsDefault() || packs[1].GetIsDefault() {
		t.Errorf("ListPacks() = %v, want the default pack first", packs)
	}
}

func TestCatalogAPI_Recipes(t *testing.T) {
	s := newServer(t)
	boss, cook := s.as(t, identity.RoleOwner), s.as(t, identity.RoleKitchen)
	product := call(t, boss.CreateProduct, &catalogv1.CreateProductRequest{Name: "Donut", Slug: "donut"}).GetProduct()
	variant := call(t, boss.CreateVariant, &catalogv1.CreateVariantRequest{ProductId: product.GetId(), Sku: "DNT", Name: "Donut", ProductionMinutes: 60}).GetVariant()
	flour := call(t, cook.CreateIngredient, &catalogv1.CreateIngredientRequest{Name: "Terigu", BaseUnit: catalogv1.BaseUnit_BASE_UNIT_GRAM}).GetIngredient()

	dough := call(t, cook.CreateComponent, &catalogv1.CreateComponentRequest{Name: "Adonan", UnitLabel: "porsi"}).GetComponent()
	dough = call(t, cook.UpdateComponent, &catalogv1.UpdateComponentRequest{Id: dough.GetId(), Name: "Adonan donut", UnitLabel: "porsi"}).GetComponent()
	if got := call(t, cook.ListComponents, &catalogv1.ListComponentsRequest{}).GetComponents(); len(got) != 1 || got[0].GetName() != "Adonan donut" {
		t.Errorf("ListComponents() = %v, want the renamed component", got)
	}

	uses := call(t, cook.SetVariantComponents, &catalogv1.SetVariantComponentsRequest{
		VariantId:  variant.GetId(),
		Components: []*catalogv1.VariantComponent{{ComponentId: dough.GetId(), UnitsPerItem: 0.25}},
	}).GetComponents()
	listed := call(t, cook.ListVariantComponents, &catalogv1.ListVariantComponentsRequest{VariantId: variant.GetId()}).GetComponents()
	for _, got := range [][]*catalogv1.VariantComponent{uses, listed} {
		if len(got) != 1 || got[0].GetComponentId() != dough.GetId() || got[0].GetUnitsPerItem() != 0.25 {
			t.Errorf("variant components = %v, want the dough at 0.25 per item", got)
		}
	}

	// From measurements: a power law through the points of §10.
	points := []*catalogv1.MeasuredPoint{{Units: 1, Amount: 100}, {Units: 2, Amount: 190}, {Units: 4, Amount: 361}}
	line := call(t, cook.SetRecipeLine, &catalogv1.SetRecipeLineRequest{
		ComponentId: dough.GetId(), IngredientId: flour.GetId(), ModelType: catalogv1.ModelType_MODEL_TYPE_POWER, Points: points,
	}).GetLine()
	if params := decode(t, line.GetParamsJson()); line.GetModelType() != catalogv1.ModelType_MODEL_TYPE_POWER ||
		math.Abs(params["a"]-100) > 1 || math.Abs(params["b"]-0.926) > 0.001 || len(line.GetPoints()) != 3 || line.GetWasteFactor() != 1 || line.GetVersion() != 1 {
		t.Errorf("SetRecipeLine(points) = %v, want a fitted power model, the points, waste 1, version 1", line)
	}
	// From params, replacing it.
	line = call(t, cook.SetRecipeLine, &catalogv1.SetRecipeLineRequest{
		ComponentId: dough.GetId(), IngredientId: flour.GetId(), ModelType: catalogv1.ModelType_MODEL_TYPE_AFFINE,
		ParamsJson: `{"a": 10, "b": 90}`, WasteFactor: 1.05,
	}).GetLine()
	if !maps.Equal(decode(t, line.GetParamsJson()), map[string]float64{"a": 10, "b": 90}) || line.GetWasteFactor() != 1.05 || line.GetVersion() != 2 {
		t.Errorf("SetRecipeLine(params) = %v, want the params, waste 1.05, version 2", line)
	}
	if got := call(t, cook.ListRecipeLines, &catalogv1.ListRecipeLinesRequest{ComponentId: dough.GetId()}).GetLines(); len(got) != 1 || got[0].GetVersion() != 2 {
		t.Errorf("ListRecipeLines() = %v, want the one line at version 2", got)
	}
	call(t, cook.RemoveRecipeLine, &catalogv1.RemoveRecipeLineRequest{ComponentId: dough.GetId(), IngredientId: flour.GetId()})
	if got := call(t, cook.ListRecipeLines, &catalogv1.ListRecipeLinesRequest{ComponentId: dough.GetId()}).GetLines(); len(got) != 0 {
		t.Errorf("ListRecipeLines() after remove = %v, want none", got)
	}
}

func TestCatalogAPI_FitPreview(t *testing.T) {
	s := newServer(t)

	results := call(t, s.as(t, identity.RoleKitchen).FitPreview, &catalogv1.FitPreviewRequest{
		Points: []*catalogv1.MeasuredPoint{{Units: 1, Amount: 0}, {Units: 2, Amount: 10}},
	}).GetResults()

	byType := map[catalogv1.ModelType]*catalogv1.FitResult{}
	for _, r := range results {
		byType[r.GetModelType()] = r
	}
	if len(results) != 3 || len(byType) != 3 {
		t.Fatalf("FitPreview() = %v, want affine, power, and piecewise", results)
	}
	if r := byType[catalogv1.ModelType_MODEL_TYPE_AFFINE]; r.GetError() != "" || r.GetParamsJson() == "" || len(r.GetResiduals()) != 2 {
		t.Errorf("affine = %v, want params and one residual per point", r)
	}
	// Power needs every amount > 0; the reason is for the person editing.
	if r := byType[catalogv1.ModelType_MODEL_TYPE_POWER]; !strings.HasPrefix(r.GetError(), "Titik ukur tidak bisa dipakai") || r.GetParamsJson() != "" {
		t.Errorf("power = %v, want an Indonesian reason and no params", r)
	}
}

func TestCatalogAPI_InvalidInputCarriesFieldErrors(t *testing.T) {
	s := newServer(t)
	boss := s.as(t, identity.RoleOwner)
	ctx := t.Context()
	call(t, boss.CreateProduct, &catalogv1.CreateProductRequest{Name: "Donut", Slug: "donut"})
	flour := call(t, boss.CreateIngredient, &catalogv1.CreateIngredientRequest{Name: "Terigu", BaseUnit: catalogv1.BaseUnit_BASE_UNIT_GRAM}).GetIngredient()
	dough := call(t, boss.CreateComponent, &catalogv1.CreateComponentRequest{Name: "Adonan", UnitLabel: "porsi"}).GetComponent()

	_, err := boss.CreateProduct(ctx, connect.NewRequest(&catalogv1.CreateProductRequest{Slug: "Kue Lapis!"}))
	if got := fieldErrors(t, err, connect.CodeInvalidArgument); got["name"] == "" || got["slug"] == "" {
		t.Errorf("fields = %v, want name and slug", got)
	}

	_, err = boss.CreateProduct(ctx, connect.NewRequest(&catalogv1.CreateProductRequest{Name: "Donut lagi", Slug: "donut"}))
	if got := fieldErrors(t, err, connect.CodeAlreadyExists); got["slug"] != "Slug sudah dipakai produk lain." {
		t.Errorf("fields = %v, want slug in use", got)
	}

	_, err = boss.CreateIngredient(ctx, connect.NewRequest(&catalogv1.CreateIngredientRequest{Name: "Gula"}))
	if got := fieldErrors(t, err, connect.CodeInvalidArgument); got["base_unit"] == "" {
		t.Errorf("fields = %v, want base_unit (unspecified)", got)
	}

	// The service calls it params; the request calls it params_json.
	_, err = boss.SetRecipeLine(ctx, connect.NewRequest(&catalogv1.SetRecipeLineRequest{
		ComponentId: dough.GetId(), IngredientId: flour.GetId(), ModelType: catalogv1.ModelType_MODEL_TYPE_FORMULA, ParamsJson: `{"expr": "100 - u"}`,
	}))
	if got := fieldErrors(t, err, connect.CodeInvalidArgument); !strings.Contains(got["params_json"], "turun") {
		t.Errorf("fields = %v, want params_json explaining the amount falls", got)
	}
}

func TestCatalogAPI_MalformedIDs(t *testing.T) {
	s := newServer(t)
	boss := s.as(t, identity.RoleOwner)
	ctx := t.Context()

	_, err := boss.UpdateProduct(ctx, connect.NewRequest(&catalogv1.UpdateProductRequest{Id: "donut", Name: "Donut", Slug: "donut"}))
	if got := fieldErrors(t, err, connect.CodeInvalidArgument); !maps.Equal(got, map[string]string{"id": "ID tidak valid."}) {
		t.Errorf("fields = %v, want only id", got)
	}

	_, err = boss.CreatePack(ctx, connect.NewRequest(&catalogv1.CreatePackRequest{IngredientId: "1", SupplierId: "2", Size: 1, Unit: "sak"}))
	if got := fieldErrors(t, err, connect.CodeInvalidArgument); got["ingredient_id"] == "" || got["supplier_id"] == "" {
		t.Errorf("fields = %v, want ingredient_id and supplier_id", got)
	}

	_, err = boss.SetVariantComponents(ctx, connect.NewRequest(&catalogv1.SetVariantComponentsRequest{
		VariantId: uuid.NewString(), Components: []*catalogv1.VariantComponent{{ComponentId: "adonan", UnitsPerItem: 1}},
	}))
	if got := fieldErrors(t, err, connect.CodeInvalidArgument); got["components"] == "" {
		t.Errorf("fields = %v, want components", got)
	}

	// An empty ID is not malformed: the service asks for the record.
	_, err = boss.CreateVariant(ctx, connect.NewRequest(&catalogv1.CreateVariantRequest{Sku: "X", Name: "X", ProductionMinutes: 60}))
	if got := fieldErrors(t, err, connect.CodeInvalidArgument); got["product_id"] != "Pilih produknya." {
		t.Errorf("fields = %v, want product_id asking for a product", got)
	}
}

func TestCatalogAPI_AccessAndMissingRecords(t *testing.T) {
	s := newServer(t)
	boss, cook := s.as(t, identity.RoleOwner), s.as(t, identity.RoleKitchen)
	ctx := t.Context()

	_, err := boss.UpdateComponent(ctx, connect.NewRequest(&catalogv1.UpdateComponentRequest{Id: uuid.NewString(), Name: "Adonan", UnitLabel: "porsi"}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("UpdateComponent(unknown id) code = %v, want NotFound", connect.CodeOf(err))
	}

	tests := []struct {
		name   string
		client catalogv1connect.CatalogAdminServiceClient
		want   connect.Code
	}{
		{"kitchen", cook, connect.CodePermissionDenied},
		{"signed in, not staff", s.as(t), connect.CodePermissionDenied},
		{"anonymous", s.withAuthorization(""), connect.CodeUnauthenticated},
		{"broken token", s.withAuthorization("Bearer not-a-jwt"), connect.CodeUnauthenticated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.client.CreateProduct(ctx, connect.NewRequest(&catalogv1.CreateProductRequest{Name: "Donut", Slug: "donut"}))
			if connect.CodeOf(err) != tt.want {
				t.Errorf("CreateProduct() code = %v, want %v", connect.CodeOf(err), tt.want)
			}
		})
	}

	// The kitchen edits recipes but not prices.
	call(t, cook.CreateComponent, &catalogv1.CreateComponentRequest{Name: "Adonan", UnitLabel: "porsi"})
	_, err = cook.ChangeVariantPrice(ctx, connect.NewRequest(&catalogv1.ChangeVariantPriceRequest{Id: uuid.NewString(), PriceIdr: 1, Reason: "x"}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("kitchen ChangeVariantPrice() code = %v, want PermissionDenied", connect.CodeOf(err))
	}
}
