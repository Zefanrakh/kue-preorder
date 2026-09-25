package connect

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"

	catalogv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/catalog/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/catalog/v1/catalogv1connect"
	"github.com/Zefanrakh/kue-preorder/internal/catalog"
)

// Handler serves kuepreorder.catalog.v1.CatalogAdminService.
//
// Malformed IDs are reported before the service authorizes the caller, so a
// caller without a role may learn that an ID string is not a UUID; nothing
// about stored records leaks that way.
type Handler struct {
	svc    *catalog.Service
	logger *slog.Logger
}

var _ catalogv1connect.CatalogAdminServiceHandler = (*Handler)(nil)

// NewHandler returns a handler backed by svc.
func NewHandler(svc *catalog.Service, logger *slog.Logger) *Handler {
	return &Handler{svc: svc, logger: logger}
}

func (h *Handler) fail(ctx context.Context, err error) error {
	return connectError(ctx, h.logger, err)
}

func toProtos[T, P any](xs []T, to func(T) P) []P {
	out := make([]P, len(xs))
	for i, x := range xs {
		out[i] = to(x)
	}
	return out
}

// ListProducts implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) ListProducts(ctx context.Context, _ *connect.Request[catalogv1.ListProductsRequest]) (*connect.Response[catalogv1.ListProductsResponse], error) {
	products, err := h.svc.ListProducts(ctx)
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.ListProductsResponse{Products: toProtos(products, productToProto)}), nil
}

// CreateProduct implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) CreateProduct(ctx context.Context, req *connect.Request[catalogv1.CreateProductRequest]) (*connect.Response[catalogv1.CreateProductResponse], error) {
	m := req.Msg
	product, err := h.svc.CreateProduct(ctx, catalog.ProductInput{
		Name: m.GetName(), Slug: m.GetSlug(), Description: m.GetDescription(), ImagePath: m.GetImagePath(), Active: m.GetActive(),
	})
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.CreateProductResponse{Product: productToProto(product)}), nil
}

// UpdateProduct implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) UpdateProduct(ctx context.Context, req *connect.Request[catalogv1.UpdateProductRequest]) (*connect.Response[catalogv1.UpdateProductResponse], error) {
	m := req.Msg
	var p ids
	id := p.parse("id", m.GetId())
	if err := p.err(); err != nil {
		return nil, h.fail(ctx, err)
	}
	product, err := h.svc.UpdateProduct(ctx, id, catalog.ProductInput{
		Name: m.GetName(), Slug: m.GetSlug(), Description: m.GetDescription(), ImagePath: m.GetImagePath(), Active: m.GetActive(),
	})
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.UpdateProductResponse{Product: productToProto(product)}), nil
}

// ListVariants implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) ListVariants(ctx context.Context, req *connect.Request[catalogv1.ListVariantsRequest]) (*connect.Response[catalogv1.ListVariantsResponse], error) {
	var p ids
	productID := p.parse("product_id", req.Msg.GetProductId())
	if err := p.err(); err != nil {
		return nil, h.fail(ctx, err)
	}
	variants, err := h.svc.ListVariants(ctx, productID)
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.ListVariantsResponse{Variants: toProtos(variants, variantToProto)}), nil
}

// CreateVariant implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) CreateVariant(ctx context.Context, req *connect.Request[catalogv1.CreateVariantRequest]) (*connect.Response[catalogv1.CreateVariantResponse], error) {
	m := req.Msg
	var p ids
	productID := p.parse("product_id", m.GetProductId())
	if err := p.err(); err != nil {
		return nil, h.fail(ctx, err)
	}
	variant, err := h.svc.CreateVariant(ctx, productID, catalog.VariantInput{
		SKU: m.GetSku(), Name: m.GetName(), Options: m.GetOptions(),
		ProductionMinutes: m.GetProductionMinutes(), MinNoticeHours: m.GetMinNoticeHours(), Active: m.GetActive(),
	}, m.GetPriceIdr())
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.CreateVariantResponse{Variant: variantToProto(variant)}), nil
}

// UpdateVariant implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) UpdateVariant(ctx context.Context, req *connect.Request[catalogv1.UpdateVariantRequest]) (*connect.Response[catalogv1.UpdateVariantResponse], error) {
	m := req.Msg
	var p ids
	id := p.parse("id", m.GetId())
	if err := p.err(); err != nil {
		return nil, h.fail(ctx, err)
	}
	variant, err := h.svc.UpdateVariant(ctx, id, catalog.VariantInput{
		SKU: m.GetSku(), Name: m.GetName(), Options: m.GetOptions(),
		ProductionMinutes: m.GetProductionMinutes(), MinNoticeHours: m.GetMinNoticeHours(), Active: m.GetActive(),
	})
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.UpdateVariantResponse{Variant: variantToProto(variant)}), nil
}

// ChangeVariantPrice implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) ChangeVariantPrice(ctx context.Context, req *connect.Request[catalogv1.ChangeVariantPriceRequest]) (*connect.Response[catalogv1.ChangeVariantPriceResponse], error) {
	m := req.Msg
	var p ids
	id := p.parse("id", m.GetId())
	if err := p.err(); err != nil {
		return nil, h.fail(ctx, err)
	}
	variant, err := h.svc.ChangeVariantPrice(ctx, id, m.GetPriceIdr(), m.GetReason())
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.ChangeVariantPriceResponse{Variant: variantToProto(variant)}), nil
}

// ListComponents implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) ListComponents(ctx context.Context, _ *connect.Request[catalogv1.ListComponentsRequest]) (*connect.Response[catalogv1.ListComponentsResponse], error) {
	components, err := h.svc.ListComponents(ctx)
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.ListComponentsResponse{Components: toProtos(components, componentToProto)}), nil
}

// CreateComponent implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) CreateComponent(ctx context.Context, req *connect.Request[catalogv1.CreateComponentRequest]) (*connect.Response[catalogv1.CreateComponentResponse], error) {
	component, err := h.svc.CreateComponent(ctx, catalog.ComponentInput{Name: req.Msg.GetName(), UnitLabel: req.Msg.GetUnitLabel()})
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.CreateComponentResponse{Component: componentToProto(component)}), nil
}

// UpdateComponent implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) UpdateComponent(ctx context.Context, req *connect.Request[catalogv1.UpdateComponentRequest]) (*connect.Response[catalogv1.UpdateComponentResponse], error) {
	m := req.Msg
	var p ids
	id := p.parse("id", m.GetId())
	if err := p.err(); err != nil {
		return nil, h.fail(ctx, err)
	}
	component, err := h.svc.UpdateComponent(ctx, id, catalog.ComponentInput{Name: m.GetName(), UnitLabel: m.GetUnitLabel()})
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.UpdateComponentResponse{Component: componentToProto(component)}), nil
}

// ListIngredients implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) ListIngredients(ctx context.Context, _ *connect.Request[catalogv1.ListIngredientsRequest]) (*connect.Response[catalogv1.ListIngredientsResponse], error) {
	ingredients, err := h.svc.ListIngredients(ctx)
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.ListIngredientsResponse{Ingredients: toProtos(ingredients, ingredientToProto)}), nil
}

// CreateIngredient implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) CreateIngredient(ctx context.Context, req *connect.Request[catalogv1.CreateIngredientRequest]) (*connect.Response[catalogv1.CreateIngredientResponse], error) {
	m := req.Msg
	ingredient, err := h.svc.CreateIngredient(ctx, catalog.IngredientInput{
		Name: m.GetName(), BaseUnit: fromProto(baseUnits, m.GetBaseUnit()), Perishable: m.GetPerishable(),
		ShelfLifeDays: m.ShelfLifeDays, LeftoverPolicy: fromProto(leftoverPolicies, m.GetLeftoverPolicy()),
	})
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.CreateIngredientResponse{Ingredient: ingredientToProto(ingredient)}), nil
}

// UpdateIngredient implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) UpdateIngredient(ctx context.Context, req *connect.Request[catalogv1.UpdateIngredientRequest]) (*connect.Response[catalogv1.UpdateIngredientResponse], error) {
	m := req.Msg
	var p ids
	id := p.parse("id", m.GetId())
	if err := p.err(); err != nil {
		return nil, h.fail(ctx, err)
	}
	ingredient, err := h.svc.UpdateIngredient(ctx, id, catalog.IngredientInput{
		Name: m.GetName(), BaseUnit: fromProto(baseUnits, m.GetBaseUnit()), Perishable: m.GetPerishable(),
		ShelfLifeDays: m.ShelfLifeDays, LeftoverPolicy: fromProto(leftoverPolicies, m.GetLeftoverPolicy()),
	})
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.UpdateIngredientResponse{Ingredient: ingredientToProto(ingredient)}), nil
}

// ListSuppliers implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) ListSuppliers(ctx context.Context, _ *connect.Request[catalogv1.ListSuppliersRequest]) (*connect.Response[catalogv1.ListSuppliersResponse], error) {
	suppliers, err := h.svc.ListSuppliers(ctx)
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.ListSuppliersResponse{Suppliers: toProtos(suppliers, supplierToProto)}), nil
}

// CreateSupplier implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) CreateSupplier(ctx context.Context, req *connect.Request[catalogv1.CreateSupplierRequest]) (*connect.Response[catalogv1.CreateSupplierResponse], error) {
	m := req.Msg
	supplier, err := h.svc.CreateSupplier(ctx, catalog.SupplierInput{
		Name: m.GetName(), WhatsAppPhone: m.GetWhatsappPhone(), Adapter: fromProto(adapters, m.GetAdapter()),
	})
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.CreateSupplierResponse{Supplier: supplierToProto(supplier)}), nil
}

// UpdateSupplier implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) UpdateSupplier(ctx context.Context, req *connect.Request[catalogv1.UpdateSupplierRequest]) (*connect.Response[catalogv1.UpdateSupplierResponse], error) {
	m := req.Msg
	var p ids
	id := p.parse("id", m.GetId())
	if err := p.err(); err != nil {
		return nil, h.fail(ctx, err)
	}
	supplier, err := h.svc.UpdateSupplier(ctx, id, catalog.SupplierInput{
		Name: m.GetName(), WhatsAppPhone: m.GetWhatsappPhone(), Adapter: fromProto(adapters, m.GetAdapter()),
	})
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.UpdateSupplierResponse{Supplier: supplierToProto(supplier)}), nil
}

// ListPacks implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) ListPacks(ctx context.Context, req *connect.Request[catalogv1.ListPacksRequest]) (*connect.Response[catalogv1.ListPacksResponse], error) {
	var p ids
	ingredientID := p.parse("ingredient_id", req.Msg.GetIngredientId())
	if err := p.err(); err != nil {
		return nil, h.fail(ctx, err)
	}
	packs, err := h.svc.ListPacks(ctx, ingredientID)
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.ListPacksResponse{Packs: toProtos(packs, packToProto)}), nil
}

// CreatePack implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) CreatePack(ctx context.Context, req *connect.Request[catalogv1.CreatePackRequest]) (*connect.Response[catalogv1.CreatePackResponse], error) {
	m := req.Msg
	var p ids
	ingredientID := p.parse("ingredient_id", m.GetIngredientId())
	supplierID := p.parse("supplier_id", m.GetSupplierId())
	if err := p.err(); err != nil {
		return nil, h.fail(ctx, err)
	}
	pack, err := h.svc.CreatePack(ctx, ingredientID, catalog.PackInput{
		SupplierID: supplierID, SupplierSKU: m.GetSupplierSku(), Size: m.GetSize(), Unit: m.GetUnit(), PriceIDR: m.PriceIdr,
	})
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.CreatePackResponse{Pack: packToProto(pack)}), nil
}

// UpdatePack implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) UpdatePack(ctx context.Context, req *connect.Request[catalogv1.UpdatePackRequest]) (*connect.Response[catalogv1.UpdatePackResponse], error) {
	m := req.Msg
	var p ids
	id := p.parse("id", m.GetId())
	supplierID := p.parse("supplier_id", m.GetSupplierId())
	if err := p.err(); err != nil {
		return nil, h.fail(ctx, err)
	}
	pack, err := h.svc.UpdatePack(ctx, id, catalog.PackInput{
		SupplierID: supplierID, SupplierSKU: m.GetSupplierSku(), Size: m.GetSize(), Unit: m.GetUnit(), PriceIDR: m.PriceIdr,
	})
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.UpdatePackResponse{Pack: packToProto(pack)}), nil
}

// SetDefaultPack implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) SetDefaultPack(ctx context.Context, req *connect.Request[catalogv1.SetDefaultPackRequest]) (*connect.Response[catalogv1.SetDefaultPackResponse], error) {
	var p ids
	id := p.parse("id", req.Msg.GetId())
	if err := p.err(); err != nil {
		return nil, h.fail(ctx, err)
	}
	pack, err := h.svc.SetDefaultPack(ctx, id)
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.SetDefaultPackResponse{Pack: packToProto(pack)}), nil
}

// ListVariantComponents implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) ListVariantComponents(ctx context.Context, req *connect.Request[catalogv1.ListVariantComponentsRequest]) (*connect.Response[catalogv1.ListVariantComponentsResponse], error) {
	var p ids
	variantID := p.parse("variant_id", req.Msg.GetVariantId())
	if err := p.err(); err != nil {
		return nil, h.fail(ctx, err)
	}
	uses, err := h.svc.ListVariantComponents(ctx, variantID)
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.ListVariantComponentsResponse{Components: variantComponentsToProto(uses)}), nil
}

// SetVariantComponents implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) SetVariantComponents(ctx context.Context, req *connect.Request[catalogv1.SetVariantComponentsRequest]) (*connect.Response[catalogv1.SetVariantComponentsResponse], error) {
	m := req.Msg
	var p ids
	variantID := p.parse("variant_id", m.GetVariantId())
	uses := make([]catalog.VariantComponent, len(m.GetComponents()))
	for i, c := range m.GetComponents() {
		uses[i] = catalog.VariantComponent{ComponentID: p.parse("components", c.GetComponentId()), UnitsPerItem: c.GetUnitsPerItem()}
	}
	if err := p.err(); err != nil {
		return nil, h.fail(ctx, err)
	}
	uses, err := h.svc.SetVariantComponents(ctx, variantID, uses)
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.SetVariantComponentsResponse{Components: variantComponentsToProto(uses)}), nil
}

// ListRecipeLines implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) ListRecipeLines(ctx context.Context, req *connect.Request[catalogv1.ListRecipeLinesRequest]) (*connect.Response[catalogv1.ListRecipeLinesResponse], error) {
	var p ids
	componentID := p.parse("component_id", req.Msg.GetComponentId())
	if err := p.err(); err != nil {
		return nil, h.fail(ctx, err)
	}
	lines, err := h.svc.ListRecipeLines(ctx, componentID)
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.ListRecipeLinesResponse{Lines: toProtos(lines, recipeLineToProto)}), nil
}

// SetRecipeLine implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) SetRecipeLine(ctx context.Context, req *connect.Request[catalogv1.SetRecipeLineRequest]) (*connect.Response[catalogv1.SetRecipeLineResponse], error) {
	m := req.Msg
	var p ids
	componentID := p.parse("component_id", m.GetComponentId())
	ingredientID := p.parse("ingredient_id", m.GetIngredientId())
	if err := p.err(); err != nil {
		return nil, h.fail(ctx, err)
	}
	line, err := h.svc.SetRecipeLine(ctx, componentID, ingredientID, catalog.RecipeLineInput{
		ModelType: fromProto(modelTypes, m.GetModelType()), Params: paramsFromProto(m.GetParamsJson()),
		Points: pointsFromProto(m.GetPoints()), WasteFactor: m.GetWasteFactor(),
	})
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.SetRecipeLineResponse{Line: recipeLineToProto(line)}), nil
}

// RemoveRecipeLine implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) RemoveRecipeLine(ctx context.Context, req *connect.Request[catalogv1.RemoveRecipeLineRequest]) (*connect.Response[catalogv1.RemoveRecipeLineResponse], error) {
	var p ids
	componentID := p.parse("component_id", req.Msg.GetComponentId())
	ingredientID := p.parse("ingredient_id", req.Msg.GetIngredientId())
	if err := p.err(); err != nil {
		return nil, h.fail(ctx, err)
	}
	if err := h.svc.RemoveRecipeLine(ctx, componentID, ingredientID); err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.RemoveRecipeLineResponse{}), nil
}

// FitPreview implements catalogv1connect.CatalogAdminServiceHandler.
func (h *Handler) FitPreview(ctx context.Context, req *connect.Request[catalogv1.FitPreviewRequest]) (*connect.Response[catalogv1.FitPreviewResponse], error) {
	results, err := h.svc.FitPreview(ctx, pointsFromProto(req.Msg.GetPoints()))
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&catalogv1.FitPreviewResponse{Results: toProtos(results, fitResultToProto)}), nil
}
