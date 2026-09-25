package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/platform/audit"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
)

// Repository implements catalog.Repository on the sqlc queries.
type Repository struct {
	db *db.DB
	q  *Queries
}

var _ catalog.Repository = (*Repository)(nil)

// NewRepository returns a repository over d.
func NewRepository(d *db.DB) *Repository {
	return &Repository{db: d, q: New(d.Pool())}
}

// ListProducts implements catalog.Repository.
func (r *Repository) ListProducts(ctx context.Context, tenantID uuid.UUID) ([]catalog.Product, error) {
	rows, err := r.q.ListProducts(ctx, tenantID)
	return mapRows(rows, err, toProduct)
}

// CreateProduct implements catalog.Repository.
func (r *Repository) CreateProduct(ctx context.Context, tenantID uuid.UUID, in catalog.ProductInput, at time.Time) (catalog.Product, error) {
	row, err := r.q.CreateProduct(ctx, CreateProductParams{
		TenantID: tenantID, Name: in.Name, Slug: in.Slug, Description: in.Description,
		ImagePath: optional(in.ImagePath), IsActive: in.Active, Now: at,
	})
	return mapRow(row, err, toProduct)
}

// UpdateProduct implements catalog.Repository.
func (r *Repository) UpdateProduct(ctx context.Context, tenantID, id uuid.UUID, in catalog.ProductInput, at time.Time) (catalog.Product, error) {
	row, err := r.q.UpdateProduct(ctx, UpdateProductParams{
		TenantID: tenantID, ID: id, Name: in.Name, Slug: in.Slug, Description: in.Description,
		ImagePath: optional(in.ImagePath), IsActive: in.Active, Now: at,
	})
	return mapRow(row, err, toProduct)
}

// ListVariants implements catalog.Repository.
func (r *Repository) ListVariants(ctx context.Context, tenantID, productID uuid.UUID) ([]catalog.Variant, error) {
	rows, err := r.q.ListVariants(ctx, ListVariantsParams{TenantID: tenantID, ProductID: productID})
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]catalog.Variant, len(rows))
	for i, row := range rows {
		if out[i], err = toVariant(row); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// CreateVariant implements catalog.Repository.
func (r *Repository) CreateVariant(ctx context.Context, tenantID, productID uuid.UUID, in catalog.VariantInput, priceIDR int64, at time.Time) (catalog.Variant, error) {
	options, err := json.Marshal(in.Options)
	if err != nil {
		return catalog.Variant{}, fmt.Errorf("encode variant options: %w", err)
	}
	row, err := r.q.CreateVariant(ctx, CreateVariantParams{
		TenantID: tenantID, ProductID: productID, Sku: in.SKU, Name: in.Name, Options: options,
		PriceIdr: priceIDR, ProductionMinutes: in.ProductionMinutes, MinNoticeHours: in.MinNoticeHours,
		IsActive: in.Active, Now: at,
	})
	if err != nil {
		return catalog.Variant{}, mapErr(err)
	}
	return toVariant(row)
}

// UpdateVariant implements catalog.Repository.
func (r *Repository) UpdateVariant(ctx context.Context, tenantID, id uuid.UUID, in catalog.VariantInput, at time.Time) (catalog.Variant, error) {
	options, err := json.Marshal(in.Options)
	if err != nil {
		return catalog.Variant{}, fmt.Errorf("encode variant options: %w", err)
	}
	row, err := r.q.UpdateVariant(ctx, UpdateVariantParams{
		TenantID: tenantID, ID: id, Sku: in.SKU, Name: in.Name, Options: options,
		ProductionMinutes: in.ProductionMinutes, MinNoticeHours: in.MinNoticeHours,
		IsActive: in.Active, Now: at,
	})
	if err != nil {
		return catalog.Variant{}, mapErr(err)
	}
	return toVariant(row)
}

// ChangeVariantPrice implements catalog.Repository.
func (r *Repository) ChangeVariantPrice(ctx context.Context, c catalog.PriceChange) (catalog.Variant, error) {
	var row ProductVariant
	err := r.db.InTx(ctx, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		old, err := q.LockVariantPrice(ctx, LockVariantPriceParams{TenantID: c.TenantID, ID: c.VariantID})
		if err != nil {
			return mapErr(err)
		}
		if old == c.PriceIDR {
			row, err = q.GetVariant(ctx, GetVariantParams{TenantID: c.TenantID, ID: c.VariantID})
			return mapErr(err)
		}
		row, err = q.SetVariantPrice(ctx, SetVariantPriceParams{TenantID: c.TenantID, ID: c.VariantID, PriceIdr: c.PriceIDR, Now: c.At})
		if err != nil {
			return mapErr(err)
		}
		return audit.Record(ctx, tx, audit.Entry{
			TenantID: c.TenantID,
			ActorID:  c.ActorID,
			Action:   "catalog.variant.price_changed",
			Entity:   "product_variant",
			EntityID: c.VariantID,
			Before:   map[string]int64{"price_idr": old},
			After:    map[string]int64{"price_idr": c.PriceIDR},
			Reason:   c.Reason,
			At:       c.At,
		})
	})
	if err != nil {
		return catalog.Variant{}, err
	}
	return toVariant(row)
}

// ListComponents implements catalog.Repository.
func (r *Repository) ListComponents(ctx context.Context, tenantID uuid.UUID) ([]catalog.Component, error) {
	rows, err := r.q.ListComponents(ctx, tenantID)
	return mapRows(rows, err, toComponent)
}

// CreateComponent implements catalog.Repository.
func (r *Repository) CreateComponent(ctx context.Context, tenantID uuid.UUID, in catalog.ComponentInput, at time.Time) (catalog.Component, error) {
	row, err := r.q.CreateComponent(ctx, CreateComponentParams{TenantID: tenantID, Name: in.Name, UnitLabel: in.UnitLabel, Now: at})
	return mapRow(row, err, toComponent)
}

// UpdateComponent implements catalog.Repository.
func (r *Repository) UpdateComponent(ctx context.Context, tenantID, id uuid.UUID, in catalog.ComponentInput, at time.Time) (catalog.Component, error) {
	row, err := r.q.UpdateComponent(ctx, UpdateComponentParams{TenantID: tenantID, ID: id, Name: in.Name, UnitLabel: in.UnitLabel, Now: at})
	return mapRow(row, err, toComponent)
}

// ListIngredients implements catalog.Repository.
func (r *Repository) ListIngredients(ctx context.Context, tenantID uuid.UUID) ([]catalog.Ingredient, error) {
	rows, err := r.q.ListIngredients(ctx, tenantID)
	return mapRows(rows, err, toIngredient)
}

// CreateIngredient implements catalog.Repository.
func (r *Repository) CreateIngredient(ctx context.Context, tenantID uuid.UUID, in catalog.IngredientInput, at time.Time) (catalog.Ingredient, error) {
	row, err := r.q.CreateIngredient(ctx, CreateIngredientParams{
		TenantID: tenantID, Name: in.Name, BaseUnit: string(in.BaseUnit), IsPerishable: in.Perishable,
		ShelfLifeDays: in.ShelfLifeDays, LeftoverPolicy: string(in.LeftoverPolicy), Now: at,
	})
	return mapRow(row, err, toIngredient)
}

// UpdateIngredient implements catalog.Repository.
func (r *Repository) UpdateIngredient(ctx context.Context, tenantID, id uuid.UUID, in catalog.IngredientInput, at time.Time) (catalog.Ingredient, error) {
	row, err := r.q.UpdateIngredient(ctx, UpdateIngredientParams{
		TenantID: tenantID, ID: id, Name: in.Name, BaseUnit: string(in.BaseUnit), IsPerishable: in.Perishable,
		ShelfLifeDays: in.ShelfLifeDays, LeftoverPolicy: string(in.LeftoverPolicy), Now: at,
	})
	return mapRow(row, err, toIngredient)
}

// ListSuppliers implements catalog.Repository.
func (r *Repository) ListSuppliers(ctx context.Context, tenantID uuid.UUID) ([]catalog.Supplier, error) {
	rows, err := r.q.ListSuppliers(ctx, tenantID)
	return mapRows(rows, err, toSupplier)
}

// CreateSupplier implements catalog.Repository.
func (r *Repository) CreateSupplier(ctx context.Context, tenantID uuid.UUID, in catalog.SupplierInput, at time.Time) (catalog.Supplier, error) {
	row, err := r.q.CreateSupplier(ctx, CreateSupplierParams{
		TenantID: tenantID, Name: in.Name, WhatsappPhone: optional(in.WhatsAppPhone), AdapterKey: string(in.Adapter), Now: at,
	})
	return mapRow(row, err, toSupplier)
}

// UpdateSupplier implements catalog.Repository.
func (r *Repository) UpdateSupplier(ctx context.Context, tenantID, id uuid.UUID, in catalog.SupplierInput, at time.Time) (catalog.Supplier, error) {
	row, err := r.q.UpdateSupplier(ctx, UpdateSupplierParams{
		TenantID: tenantID, ID: id, Name: in.Name, WhatsappPhone: optional(in.WhatsAppPhone), AdapterKey: string(in.Adapter), Now: at,
	})
	return mapRow(row, err, toSupplier)
}

// ListPacks implements catalog.Repository.
func (r *Repository) ListPacks(ctx context.Context, tenantID, ingredientID uuid.UUID) ([]catalog.Pack, error) {
	rows, err := r.q.ListPacks(ctx, ListPacksParams{TenantID: tenantID, IngredientID: ingredientID})
	return mapRows(rows, err, toPack)
}

// CreatePack implements catalog.Repository.
func (r *Repository) CreatePack(ctx context.Context, tenantID, ingredientID uuid.UUID, in catalog.PackInput, at time.Time) (catalog.Pack, error) {
	row, err := r.q.CreatePack(ctx, CreatePackParams{
		TenantID: tenantID, IngredientID: ingredientID, SupplierID: in.SupplierID, SupplierSku: optional(in.SupplierSKU),
		PackSize: in.Size, PackUnit: in.Unit, PriceIdr: in.PriceIDR, Now: at,
	})
	return mapRow(row, err, toPack)
}

// UpdatePack implements catalog.Repository.
func (r *Repository) UpdatePack(ctx context.Context, tenantID, id uuid.UUID, in catalog.PackInput, at time.Time) (catalog.Pack, error) {
	row, err := r.q.UpdatePack(ctx, UpdatePackParams{
		TenantID: tenantID, ID: id, SupplierID: in.SupplierID, SupplierSku: optional(in.SupplierSKU),
		PackSize: in.Size, PackUnit: in.Unit, PriceIdr: in.PriceIDR, Now: at,
	})
	return mapRow(row, err, toPack)
}

// SetDefaultPack implements catalog.Repository.
func (r *Repository) SetDefaultPack(ctx context.Context, tenantID, id uuid.UUID, at time.Time) (catalog.Pack, error) {
	var row IngredientSupplier
	err := r.db.InTx(ctx, func(tx pgx.Tx) error {
		q := r.q.WithTx(tx)
		pack, err := q.GetPack(ctx, GetPackParams{TenantID: tenantID, ID: id})
		if err != nil {
			return mapErr(err)
		}
		if err := q.ClearDefaultPack(ctx, ClearDefaultPackParams{TenantID: tenantID, IngredientID: pack.IngredientID, Now: at}); err != nil {
			return mapErr(err)
		}
		row, err = q.MarkDefaultPack(ctx, MarkDefaultPackParams{TenantID: tenantID, ID: id, Now: at})
		return mapErr(err)
	})
	if err != nil {
		return catalog.Pack{}, err
	}
	return toPack(row), nil
}

// uniqueFields names the field a unique constraint protects and the message
// staff see when they hit it.
var uniqueFields = map[string][2]string{
	"products_tenant_id_slug_key":        {"slug", "Slug sudah dipakai produk lain."},
	"product_variants_tenant_id_sku_key": {"sku", "SKU sudah dipakai varian lain."},
	"components_tenant_name_key":         {"name", "Nama komponen sudah dipakai."},
	"ingredients_tenant_name_key":        {"name", "Nama bahan sudah dipakai."},
	"suppliers_tenant_name_key":          {"name", "Nama supplier sudah dipakai."},
	"ingredient_suppliers_one_default":   {"default", "Bahan ini sudah punya kemasan default."},
}

// foreignFields names the field whose referenced record is missing from the
// tenant, which is how another tenant's id looks from here.
var foreignFields = map[string][2]string{
	"product_variants_tenant_id_product_id_fkey":         {"product_id", "Produk tidak ditemukan."},
	"ingredient_suppliers_tenant_id_ingredient_id_fkey":  {"ingredient_id", "Bahan tidak ditemukan."},
	"ingredient_suppliers_tenant_id_supplier_id_fkey":    {"supplier_id", "Supplier tidak ditemukan."},
	"variant_components_tenant_id_component_id_fkey":     {"components", "Komponen tidak ditemukan."},
	"component_ingredients_tenant_id_ingredient_id_fkey": {"ingredient_id", "Bahan tidak ditemukan."},
}

// mapErr turns database errors into catalog errors: no row is ErrNotFound,
// unique and foreign-key violations become a *ValidationError on the field.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return catalog.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case "23505": // unique_violation
		if f, ok := uniqueFields[pgErr.ConstraintName]; ok {
			return &catalog.ValidationError{Fields: map[string]string{f[0]: f[1]}, Conflict: true}
		}
		if pgErr.TableName == "ingredient_suppliers" { // the (ingredient, supplier, size) key, whose name Postgres truncates
			return &catalog.ValidationError{Fields: map[string]string{"size": "Kemasan dengan isi ini dari supplier ini sudah ada."}, Conflict: true}
		}
	case "23503": // foreign_key_violation
		if f, ok := foreignFields[pgErr.ConstraintName]; ok {
			return &catalog.ValidationError{Fields: map[string]string{f[0]: f[1]}}
		}
	}
	return err
}

func mapRow[R, T any](row R, err error, to func(R) T) (T, error) {
	if err != nil {
		var zero T
		return zero, mapErr(err)
	}
	return to(row), nil
}

func mapRows[R, T any](rows []R, err error, to func(R) T) ([]T, error) {
	if err != nil {
		return nil, mapErr(err)
	}
	return mapSlice(rows, to), nil
}

// optional stores an empty string as NULL.
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func toProduct(p Product) catalog.Product {
	return catalog.Product{
		ID: p.ID, Name: p.Name, Slug: p.Slug, Description: p.Description, ImagePath: deref(p.ImagePath),
		Active: p.IsActive, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

func toVariant(v ProductVariant) (catalog.Variant, error) {
	options := map[string]string{}
	if err := json.Unmarshal(v.Options, &options); err != nil {
		return catalog.Variant{}, fmt.Errorf("decode options of variant %s: %w", v.ID, err)
	}
	return catalog.Variant{
		ID: v.ID, ProductID: v.ProductID, SKU: v.Sku, Name: v.Name, Options: options, PriceIDR: v.PriceIdr,
		ProductionMinutes: v.ProductionMinutes, MinNoticeHours: v.MinNoticeHours, Active: v.IsActive,
		CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
	}, nil
}

func toComponent(c Component) catalog.Component {
	return catalog.Component{ID: c.ID, Name: c.Name, UnitLabel: c.UnitLabel, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}

func toIngredient(i Ingredient) catalog.Ingredient {
	return catalog.Ingredient{
		ID: i.ID, Name: i.Name, BaseUnit: catalog.BaseUnit(i.BaseUnit), Perishable: i.IsPerishable,
		ShelfLifeDays: i.ShelfLifeDays, LeftoverPolicy: catalog.LeftoverPolicy(i.LeftoverPolicy),
		CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt,
	}
}

func toSupplier(s Supplier) catalog.Supplier {
	return catalog.Supplier{
		ID: s.ID, Name: s.Name, WhatsAppPhone: deref(s.WhatsappPhone), Adapter: catalog.AdapterKey(s.AdapterKey),
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
}

func toPack(p IngredientSupplier) catalog.Pack {
	return catalog.Pack{
		ID: p.ID, IngredientID: p.IngredientID, SupplierID: p.SupplierID, SupplierSKU: deref(p.SupplierSku),
		Size: p.PackSize, Unit: p.PackUnit, PriceIDR: p.PriceIdr, Default: p.IsDefault,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}
