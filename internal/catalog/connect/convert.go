package connect

import (
	"encoding/json"

	"google.golang.org/protobuf/types/known/timestamppb"

	catalogv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/catalog/v1"
	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/recipe"
)

func productToProto(p catalog.Product) *catalogv1.Product {
	return &catalogv1.Product{
		Id: p.ID.String(), Name: p.Name, Slug: p.Slug, Description: p.Description,
		ImagePath: p.ImagePath, Active: p.Active, UpdatedAt: timestamppb.New(p.UpdatedAt),
	}
}

func variantToProto(v catalog.Variant) *catalogv1.Variant {
	return &catalogv1.Variant{
		Id: v.ID.String(), ProductId: v.ProductID.String(), Sku: v.SKU, Name: v.Name, Options: v.Options,
		PriceIdr: v.PriceIDR, ProductionMinutes: v.ProductionMinutes, MinNoticeHours: v.MinNoticeHours,
		Active: v.Active, UpdatedAt: timestamppb.New(v.UpdatedAt),
	}
}

func componentToProto(c catalog.Component) *catalogv1.Component {
	return &catalogv1.Component{Id: c.ID.String(), Name: c.Name, UnitLabel: c.UnitLabel, UpdatedAt: timestamppb.New(c.UpdatedAt)}
}

var baseUnits = map[catalog.BaseUnit]catalogv1.BaseUnit{
	catalog.Gram:       catalogv1.BaseUnit_BASE_UNIT_GRAM,
	catalog.Millilitre: catalogv1.BaseUnit_BASE_UNIT_MILLILITRE,
	catalog.Piece:      catalogv1.BaseUnit_BASE_UNIT_PIECE,
}

var leftoverPolicies = map[catalog.LeftoverPolicy]catalogv1.LeftoverPolicy{
	catalog.LeftoverAuto:    catalogv1.LeftoverPolicy_LEFTOVER_POLICY_AUTO,
	catalog.LeftoverConfirm: catalogv1.LeftoverPolicy_LEFTOVER_POLICY_CONFIRM,
	catalog.LeftoverNever:   catalogv1.LeftoverPolicy_LEFTOVER_POLICY_NEVER,
}

var adapters = map[catalog.AdapterKey]catalogv1.ProcurementAdapter{
	catalog.AdapterManual:   catalogv1.ProcurementAdapter_PROCUREMENT_ADAPTER_MANUAL,
	catalog.AdapterWhatsApp: catalogv1.ProcurementAdapter_PROCUREMENT_ADAPTER_WHATSAPP,
}

var modelTypes = map[recipe.ModelType]catalogv1.ModelType{
	recipe.Affine:    catalogv1.ModelType_MODEL_TYPE_AFFINE,
	recipe.Power:     catalogv1.ModelType_MODEL_TYPE_POWER,
	recipe.Piecewise: catalogv1.ModelType_MODEL_TYPE_PIECEWISE,
	recipe.Formula:   catalogv1.ModelType_MODEL_TYPE_FORMULA,
}

// fromProto inverts one of the enum maps above. An unspecified or unknown
// value becomes the zero domain value, which the service defaults or rejects
// in its own words.
func fromProto[D comparable, P comparable](m map[D]P, v P) D {
	for d, p := range m {
		if p == v {
			return d
		}
	}
	var zero D
	return zero
}

func ingredientToProto(i catalog.Ingredient) *catalogv1.Ingredient {
	return &catalogv1.Ingredient{
		Id: i.ID.String(), Name: i.Name, BaseUnit: baseUnits[i.BaseUnit], Perishable: i.Perishable,
		ShelfLifeDays: i.ShelfLifeDays, LeftoverPolicy: leftoverPolicies[i.LeftoverPolicy],
		UpdatedAt: timestamppb.New(i.UpdatedAt),
	}
}

func supplierToProto(s catalog.Supplier) *catalogv1.Supplier {
	return &catalogv1.Supplier{
		Id: s.ID.String(), Name: s.Name, WhatsappPhone: s.WhatsAppPhone, Adapter: adapters[s.Adapter],
		UpdatedAt: timestamppb.New(s.UpdatedAt),
	}
}

func packToProto(p catalog.Pack) *catalogv1.Pack {
	return &catalogv1.Pack{
		Id: p.ID.String(), IngredientId: p.IngredientID.String(), SupplierId: p.SupplierID.String(),
		SupplierSku: p.SupplierSKU, Size: p.Size, Unit: p.Unit, PriceIdr: p.PriceIDR, IsDefault: p.Default,
		UpdatedAt: timestamppb.New(p.UpdatedAt),
	}
}

func variantComponentsToProto(uses []catalog.VariantComponent) []*catalogv1.VariantComponent {
	out := make([]*catalogv1.VariantComponent, len(uses))
	for i, u := range uses {
		out[i] = &catalogv1.VariantComponent{ComponentId: u.ComponentID.String(), UnitsPerItem: u.UnitsPerItem}
	}
	return out
}

func pointsToProto(points []recipe.Point) []*catalogv1.MeasuredPoint {
	out := make([]*catalogv1.MeasuredPoint, len(points))
	for i, p := range points {
		out[i] = &catalogv1.MeasuredPoint{Units: p.U, Amount: p.Amount}
	}
	return out
}

func pointsFromProto(points []*catalogv1.MeasuredPoint) []recipe.Point {
	out := make([]recipe.Point, len(points))
	for i, p := range points {
		out[i] = recipe.Point{U: p.GetUnits(), Amount: p.GetAmount()}
	}
	return out
}

func recipeLineToProto(l catalog.RecipeLine) *catalogv1.RecipeLine {
	return &catalogv1.RecipeLine{
		ComponentId: l.ComponentID.String(), IngredientId: l.IngredientID.String(), ModelType: modelTypes[l.ModelType],
		ParamsJson: string(l.Params), Points: pointsToProto(l.Points), WasteFactor: l.WasteFactor,
		Version: l.Version, UpdatedAt: timestamppb.New(l.UpdatedAt),
	}
}

func fitResultToProto(r recipe.FitResult) *catalogv1.FitResult {
	out := &catalogv1.FitResult{
		ModelType: modelTypes[r.Type], R2: r.Report.R2, Residuals: r.Report.Residuals,
		MaxAbsError: r.Report.MaxAbsError, MaxRelError: r.Report.MaxRelError,
	}
	if r.Err != nil {
		out.Error = catalog.ExplainRecipeError(r.Err)
		return out
	}
	if params, err := recipe.Params(r.Model); err == nil {
		out.ParamsJson = string(params)
	}
	return out
}

// paramsFromProto keeps an empty params_json empty (fit from points) and
// passes anything else to the recipe engine, which reports bad JSON itself.
func paramsFromProto(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}
