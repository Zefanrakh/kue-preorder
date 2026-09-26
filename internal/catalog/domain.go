// Package catalog manages what the shop sells and what it is made of
// (docs/architecture.md §6, §9.3): products and variants, components,
// ingredients, suppliers, packs, and the recipes between them.
//
// Authorization happens in Service, following the role table in §8: owners
// manage products, variants, prices, suppliers, and packs; owners and the
// kitchen manage components and ingredients; all staff read everything.
//
// Validation messages are shown to staff in the CMS, so they are Indonesian.
// The database constraints of migration 00003 stay as the last line of defence.
package catalog

import (
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/recipe"
)

// The catalog's errors are the shared ones of platform/apperr, so every
// module maps to Connect codes the same way.
var (
	// ErrNotFound means the record does not exist in the caller's tenant.
	ErrNotFound = apperr.ErrNotFound
	// ErrForbidden means the caller's roles do not allow the action.
	ErrForbidden = apperr.ErrForbidden
)

// ValidationError lists invalid fields with messages for the person editing.
// Conflict marks a clash with an existing record, such as a SKU in use.
type ValidationError = apperr.ValidationError

// fields collects field errors.
type fields = apperr.Fields

// BaseUnit is the unit an ingredient is weighed or counted in.
type BaseUnit string

// Base units (§9.3).
const (
	Gram       BaseUnit = "g"
	Millilitre BaseUnit = "ml"
	Piece      BaseUnit = "pcs"
)

// Rounding is how amounts of this unit become whole units (§10): pieces round
// up, since one dough cannot use part of an egg; grams and millilitres round
// to the nearest.
func (u BaseUnit) Rounding() recipe.Rounding {
	if u == Piece {
		return recipe.RoundUp
	}
	return recipe.RoundNearest
}

// LeftoverPolicy says whether leftovers count as stock (§12).
type LeftoverPolicy string

// Leftover policies (§12).
const (
	LeftoverAuto    LeftoverPolicy = "auto"    // counted while not expired
	LeftoverConfirm LeftoverPolicy = "confirm" // counted only if checked in the last 24 hours
	LeftoverNever   LeftoverPolicy = "never"   // always bought fresh
)

// AdapterKey says how purchase orders reach a supplier (§19).
type AdapterKey string

// Procurement adapters.
const (
	AdapterManual   AdapterKey = "manual"
	AdapterWhatsApp AdapterKey = "whatsapp"
)

// Product groups variants for the storefront.
type Product struct {
	ID          uuid.UUID
	Name        string
	Slug        string
	Description string
	ImagePath   string // Supabase Storage path; empty when there is no photo
	Active      bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ProductInput holds the editable fields of a product.
type ProductInput struct {
	Name, Slug, Description, ImagePath string
	Active                             bool
}

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func (in ProductInput) normalize() ProductInput {
	in.Name = strings.TrimSpace(in.Name)
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	in.Description = strings.TrimSpace(in.Description)
	in.ImagePath = strings.TrimSpace(in.ImagePath)
	return in
}

func (in ProductInput) validate() error {
	f := fields{}
	f.Check(in.Name != "", "name", "Nama produk wajib diisi.")
	f.Check(len(in.Name) <= 200, "name", "Nama produk paling panjang 200 karakter.")
	f.Check(slugPattern.MatchString(in.Slug), "slug", "Slug hanya boleh huruf kecil, angka, dan tanda hubung, misalnya kue-lapis.")
	f.Check(len(in.Description) <= 5000, "description", "Deskripsi paling panjang 5000 karakter.")
	return f.Err()
}

// Variant is what a customer buys, such as "Donut Coklat" or "Lapis 20x20".
type Variant struct {
	ID                uuid.UUID
	ProductID         uuid.UUID
	SKU               string
	Name              string
	Options           map[string]string // e.g. {"rasa": "coklat"}
	PriceIDR          int64
	ProductionMinutes int32
	MinNoticeHours    int32 // order to pickup, shopping time included (§15)
	Active            bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// VariantInput holds the editable fields of a variant. Its product is chosen
// once, in Service.CreateVariant, and never changes. The price is not here
// either: it is set on creation and then changes only through
// Service.ChangeVariantPrice, which is audited.
type VariantInput struct {
	SKU, Name         string
	Options           map[string]string
	ProductionMinutes int32
	MinNoticeHours    int32
	Active            bool
}

const (
	maxProductionMinutes = 240     // §15: production takes at most 4 hours
	maxMinNoticeHours    = 24 * 90 // a quarter ahead
	maxPriceIDR          = 1 << 40 // over a trillion rupiah: surely a typo
	maxVariantOptions    = 10
)

func (in VariantInput) normalize() VariantInput {
	in.SKU = strings.ToUpper(strings.TrimSpace(in.SKU))
	in.Name = strings.TrimSpace(in.Name)
	opts := make(map[string]string, len(in.Options))
	for k, v := range in.Options {
		opts[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	in.Options = opts
	return in
}

func (in VariantInput) validate() error {
	f := fields{}
	f.Check(in.SKU != "" && !strings.ContainsAny(in.SKU, " \t\n"), "sku", "SKU wajib diisi dan tidak boleh berisi spasi.")
	f.Check(len(in.SKU) <= 64, "sku", "SKU paling panjang 64 karakter.")
	f.Check(in.Name != "", "name", "Nama varian wajib diisi.")
	f.Check(in.ProductionMinutes >= 1 && in.ProductionMinutes <= maxProductionMinutes, "production_minutes", "Waktu produksi 1 sampai 240 menit.")
	f.Check(in.MinNoticeHours >= 0 && in.MinNoticeHours <= maxMinNoticeHours, "min_notice_hours", "Jarak pesan ke ambil 0 sampai 2160 jam.")
	f.Check(len(in.Options) <= maxVariantOptions, "options", "Paling banyak 10 pilihan.")
	for k, v := range in.Options {
		f.Check(k != "" && v != "", "options", "Setiap pilihan butuh nama dan nilai, misalnya rasa: coklat.")
	}
	return f.Err()
}

func validatePrice(priceIDR int64) error {
	f := fields{}
	f.Check(priceIDR >= 0 && priceIDR <= maxPriceIDR, "price_idr", "Harga harus 0 atau lebih, dalam rupiah.")
	return f.Err()
}

// Component is a half-made good made in one go and shared by variants:
// dough, filling, topping.
type Component struct {
	ID        uuid.UUID
	Name      string
	UnitLabel string // "porsi", "loyang", "pcs"
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ComponentInput holds the editable fields of a component.
type ComponentInput struct {
	Name, UnitLabel string
}

func (in ComponentInput) normalize() ComponentInput {
	in.Name = strings.TrimSpace(in.Name)
	in.UnitLabel = strings.TrimSpace(in.UnitLabel)
	return in
}

func (in ComponentInput) validate() error {
	f := fields{}
	f.Check(in.Name != "", "name", "Nama komponen wajib diisi.")
	f.Check(in.UnitLabel != "", "unit_label", "Satuan komponen wajib diisi, misalnya porsi atau loyang.")
	return f.Err()
}

// Ingredient is something the kitchen buys.
type Ingredient struct {
	ID             uuid.UUID
	Name           string
	BaseUnit       BaseUnit
	Perishable     bool
	ShelfLifeDays  *int32 // nil when unknown
	LeftoverPolicy LeftoverPolicy
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// IngredientInput holds the editable fields of an ingredient.
type IngredientInput struct {
	Name           string
	BaseUnit       BaseUnit
	Perishable     bool
	ShelfLifeDays  *int32
	LeftoverPolicy LeftoverPolicy
}

func (in IngredientInput) normalize() IngredientInput {
	in.Name = strings.TrimSpace(in.Name)
	if in.LeftoverPolicy == "" {
		in.LeftoverPolicy = LeftoverAuto
		if in.Perishable {
			in.LeftoverPolicy = LeftoverConfirm
		}
	}
	return in
}

func (in IngredientInput) validate() error {
	f := fields{}
	f.Check(in.Name != "", "name", "Nama bahan wajib diisi.")
	f.Check(slices.Contains([]BaseUnit{Gram, Millilitre, Piece}, in.BaseUnit), "base_unit", "Satuan dasar harus g, ml, atau pcs.")
	f.Check(slices.Contains([]LeftoverPolicy{LeftoverAuto, LeftoverConfirm, LeftoverNever}, in.LeftoverPolicy), "leftover_policy", "Aturan sisa harus auto, confirm, atau never.")
	f.Check(!in.Perishable || in.LeftoverPolicy != LeftoverAuto, "leftover_policy", "Sisa bahan mudah rusak harus dicek dulu: pilih confirm atau never.")
	f.Check(in.ShelfLifeDays == nil || *in.ShelfLifeDays > 0, "shelf_life_days", "Umur simpan harus lebih dari 0 hari.")
	return f.Err()
}

// Supplier sells ingredients.
type Supplier struct {
	ID            uuid.UUID
	Name          string
	WhatsAppPhone string // E.164; empty when unknown
	Adapter       AdapterKey
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// SupplierInput holds the editable fields of a supplier.
type SupplierInput struct {
	Name, WhatsAppPhone string
	Adapter             AdapterKey
}

var e164Pattern = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

func (in SupplierInput) normalize() SupplierInput {
	in.Name = strings.TrimSpace(in.Name)
	in.WhatsAppPhone = normalizePhone(in.WhatsAppPhone)
	if in.Adapter == "" {
		in.Adapter = AdapterManual
	}
	return in
}

// normalizePhone turns the ways Indonesians write numbers (0812-3456-789,
// 62 812 3456 789) into E.164 (+628123456789).
func normalizePhone(raw string) string {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' || r == '+' {
			return r
		}
		if r == ' ' || r == '-' || r == '.' || r == '(' || r == ')' {
			return -1
		}
		return r // anything else stays and fails validation
	}, strings.TrimSpace(raw))
	switch {
	case digits == "":
		return ""
	case strings.HasPrefix(digits, "0"):
		return "+62" + digits[1:]
	case strings.HasPrefix(digits, "62"):
		return "+" + digits
	default:
		return digits
	}
}

func (in SupplierInput) validate() error {
	f := fields{}
	f.Check(in.Name != "", "name", "Nama supplier wajib diisi.")
	f.Check(in.WhatsAppPhone == "" || e164Pattern.MatchString(in.WhatsAppPhone), "whatsapp_phone", "Nomor WhatsApp tidak valid, misalnya 0812 3456 789.")
	f.Check(slices.Contains([]AdapterKey{AdapterManual, AdapterWhatsApp}, in.Adapter), "adapter", "Cara pesan harus manual atau whatsapp.")
	f.Check(in.Adapter != AdapterWhatsApp || in.WhatsAppPhone != "", "whatsapp_phone", "Supplier yang dipesan lewat WhatsApp butuh nomor WhatsApp.")
	return f.Err()
}

// Pack is how a supplier sells an ingredient. Size is in the ingredient's
// base unit (1000 for a 1 kg bag of flour in grams), so rounding a shopping
// list up to packs never mixes units; Unit is only a label ("sak 1 kg").
type Pack struct {
	ID           uuid.UUID
	IngredientID uuid.UUID
	SupplierID   uuid.UUID
	SupplierSKU  string
	Size         float64
	Unit         string
	PriceIDR     *int64 // per pack; nil when unknown
	Default      bool   // the pack the shopping list rounds to
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// PackInput holds the editable fields of a pack. Its ingredient is chosen
// once, in Service.CreatePack, and never changes; which pack is the default
// changes only through Service.SetDefaultPack.
type PackInput struct {
	SupplierID  uuid.UUID
	SupplierSKU string
	Size        float64
	Unit        string
	PriceIDR    *int64
}

func (in PackInput) normalize() PackInput {
	in.SupplierSKU = strings.TrimSpace(in.SupplierSKU)
	in.Unit = strings.TrimSpace(in.Unit)
	return in
}

func (in PackInput) validate() error {
	f := fields{}
	f.Check(in.SupplierID != uuid.Nil, "supplier_id", "Pilih suppliernya.")
	f.Check(in.Size > 0 && !math.IsInf(in.Size, 0) && !math.IsNaN(in.Size), "size", "Isi kemasan harus lebih dari 0, dalam satuan dasar bahan.")
	f.Check(in.Unit != "", "unit", "Nama kemasan wajib diisi, misalnya sak 1 kg.")
	f.Check(in.PriceIDR == nil || (*in.PriceIDR >= 0 && *in.PriceIDR <= maxPriceIDR), "price_idr", "Harga kemasan harus 0 atau lebih, dalam rupiah.")
	return f.Err()
}

// PriceChange is an audited change of a variant's price.
type PriceChange struct {
	TenantID  uuid.UUID
	VariantID uuid.UUID
	ActorID   uuid.UUID
	PriceIDR  int64
	Reason    string
	At        time.Time
}

func (c PriceChange) validate() error {
	if err := validatePrice(c.PriceIDR); err != nil {
		return err
	}
	f := fields{}
	f.Check(strings.TrimSpace(c.Reason) != "", "reason", "Alasan perubahan harga wajib diisi.")
	return f.Err()
}
