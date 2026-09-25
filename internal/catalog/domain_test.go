package catalog

import (
	"errors"
	"math"
	"testing"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/recipe"
)

// invalidFields returns the fields a validation error names, or fails.
func invalidFields(t *testing.T, err error) map[string]string {
	t.Helper()
	var v *ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
	return v.Fields
}

func ptr[T any](v T) *T { return &v }

func TestProductInput(t *testing.T) {
	in := ProductInput{Name: "  Kue Lapis ", Slug: " Kue-Lapis "}.normalize()
	if in.Name != "Kue Lapis" || in.Slug != "kue-lapis" {
		t.Errorf("normalize() = %+v, want trimmed name and lowercase slug", in)
	}
	if err := in.validate(); err != nil {
		t.Errorf("validate() = %v", err)
	}

	f := invalidFields(t, ProductInput{Slug: "kue lapis"}.normalize().validate())
	if f["name"] == "" || f["slug"] == "" {
		t.Errorf("fields = %v, want name and slug", f)
	}
}

func TestVariantInput(t *testing.T) {
	valid := VariantInput{SKU: " donut-coklat ", Name: "Donut Coklat",
		Options: map[string]string{" rasa ": " coklat "}, ProductionMinutes: 90, MinNoticeHours: 24}.normalize()
	if valid.SKU != "DONUT-COKLAT" || valid.Options["rasa"] != "coklat" {
		t.Errorf("normalize() = %+v, want an uppercase SKU and trimmed options", valid)
	}
	if err := valid.validate(); err != nil {
		t.Fatalf("validate() = %v", err)
	}

	tests := []struct {
		name  string
		edit  func(*VariantInput)
		field string
	}{
		{"SKU with a space", func(v *VariantInput) { v.SKU = "DONUT COKLAT" }, "sku"},
		{"no name", func(v *VariantInput) { v.Name = "" }, "name"},
		{"no production time", func(v *VariantInput) { v.ProductionMinutes = 0 }, "production_minutes"},
		{"production over 4 hours", func(v *VariantInput) { v.ProductionMinutes = 241 }, "production_minutes"},
		{"negative notice", func(v *VariantInput) { v.MinNoticeHours = -1 }, "min_notice_hours"},
		{"option without a value", func(v *VariantInput) { v.Options = map[string]string{"rasa": ""} }, "options"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := valid
			tt.edit(&in)
			if f := invalidFields(t, in.validate()); f[tt.field] == "" {
				t.Errorf("fields = %v, want %s", f, tt.field)
			}
		})
	}

	if f := invalidFields(t, validatePrice(-1)); f["price_idr"] == "" {
		t.Errorf("fields = %v, want price_idr", f)
	}
	if err := validatePrice(0); err != nil {
		t.Errorf("validatePrice(0) = %v, want free items allowed", err)
	}
}

func TestIngredientInput(t *testing.T) {
	egg := IngredientInput{Name: "Telur", BaseUnit: Piece, Perishable: true}.normalize()
	if egg.LeftoverPolicy != LeftoverConfirm {
		t.Errorf("perishable default policy = %q, want confirm", egg.LeftoverPolicy)
	}
	flour := IngredientInput{Name: "Tepung", BaseUnit: Gram}.normalize()
	if flour.LeftoverPolicy != LeftoverAuto {
		t.Errorf("default policy = %q, want auto", flour.LeftoverPolicy)
	}

	tests := []struct {
		name  string
		in    IngredientInput
		field string
	}{
		{"perishable counted without a check", IngredientInput{Name: "Butter", BaseUnit: Gram, Perishable: true, LeftoverPolicy: LeftoverAuto}, "leftover_policy"},
		{"unknown base unit", IngredientInput{Name: "Gula", BaseUnit: "kg"}, "base_unit"},
		{"unknown leftover policy", IngredientInput{Name: "Gula", BaseUnit: Gram, LeftoverPolicy: "sometimes"}, "leftover_policy"},
		{"zero shelf life", IngredientInput{Name: "Susu", BaseUnit: Millilitre, ShelfLifeDays: ptr[int32](0)}, "shelf_life_days"},
		{"no name", IngredientInput{BaseUnit: Gram}, "name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if f := invalidFields(t, tt.in.normalize().validate()); f[tt.field] == "" {
				t.Errorf("fields = %v, want %s", f, tt.field)
			}
		})
	}
}

func TestSupplierInput_NormalizesIndonesianPhoneNumbers(t *testing.T) {
	tests := []struct{ raw, want string }{
		{"0812-3456-789", "+628123456789"},
		{"62 812 3456 789", "+628123456789"},
		{"+62 (812) 3456.789", "+628123456789"},
		{"  ", ""},
	}
	for _, tt := range tests {
		if got := normalizePhone(tt.raw); got != tt.want {
			t.Errorf("normalizePhone(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}

	in := SupplierInput{Name: "Toko Bahan"}.normalize()
	if in.Adapter != AdapterManual {
		t.Errorf("default adapter = %q, want manual", in.Adapter)
	}
	for name, bad := range map[string]SupplierInput{
		"letters in the number":     {Name: "A", WhatsAppPhone: "0812-ABC"},
		"WhatsApp without a number": {Name: "A", Adapter: AdapterWhatsApp},
		"unknown adapter":           {Name: "A", Adapter: "email"},
	} {
		if err := bad.normalize().validate(); err == nil {
			t.Errorf("%s: validate() = nil", name)
		}
	}
}

func TestPackInput(t *testing.T) {
	valid := PackInput{SupplierID: uuid.New(), Size: 1000, Unit: "sak 1 kg"}
	if err := valid.normalize().validate(); err != nil {
		t.Fatalf("validate() = %v", err)
	}
	for name, edit := range map[string]func(*PackInput){
		"empty pack":     func(p *PackInput) { p.Size = 0 },
		"NaN size":       func(p *PackInput) { p.Size = math.NaN() },
		"no label":       func(p *PackInput) { p.Unit = " " },
		"negative price": func(p *PackInput) { p.PriceIDR = ptr[int64](-5) },
	} {
		in := valid
		edit(&in)
		if err := in.normalize().validate(); err == nil {
			t.Errorf("%s: validate() = nil", name)
		}
	}
}

func TestPriceChange_NeedsAReason(t *testing.T) {
	if f := invalidFields(t, PriceChange{PriceIDR: 9000, Reason: "  "}.validate()); f["reason"] == "" {
		t.Errorf("fields = %v, want reason", f)
	}
}

func TestBaseUnit_Rounding(t *testing.T) {
	if Piece.Rounding() != recipe.RoundUp || Gram.Rounding() != recipe.RoundNearest || Millilitre.Rounding() != recipe.RoundNearest {
		t.Error("pcs must round up, g and ml to the nearest (§10)")
	}
}

func TestValidationError_ListsFieldsInOrder(t *testing.T) {
	err := &ValidationError{Fields: map[string]string{"sku": "b", "name": "a"}}
	if got := err.Error(); got != "invalid input: name: a; sku: b" {
		t.Errorf("Error() = %q", got)
	}
}
