//go:build integration

package postgres_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Zefanrakh/kue-preorder/internal/catalog/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
	"github.com/Zefanrakh/kue-preorder/internal/recipe"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

// donutShop is the §11 example: chocolate and cheese donuts share one dough.
type donutShop struct {
	tenant                          uuid.UUID
	product, chocolate, cheese      uuid.UUID
	dough, chocTopping, cheeseTopng uuid.UUID
	flour, egg, cocoa               uuid.UUID
	supplier                        uuid.UUID
}

func insertID(t *testing.T, d *db.DB, sql string, args ...any) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := d.Pool().QueryRow(t.Context(), sql+" returning id", args...).Scan(&id); err != nil {
		t.Fatalf("seed %q: %v", sql, err)
	}
	return id
}

func seedDonutShop(t *testing.T, d *db.DB, tenant uuid.UUID) donutShop {
	t.Helper()
	s := donutShop{tenant: tenant}
	s.product = insertID(t, d, `insert into products (tenant_id, name, slug) values ($1, 'Donut', 'donut')`, tenant)
	variant := `insert into product_variants (tenant_id, product_id, sku, name, options, price_idr, production_minutes, min_notice_hours)
	            values ($1, $2, $3, $4, $5, 8000, 90, 24)`
	s.chocolate = insertID(t, d, variant, tenant, s.product, "DONUT-COKLAT", "Donut Coklat", `{"rasa":"coklat"}`)
	s.cheese = insertID(t, d, variant, tenant, s.product, "DONUT-KEJU", "Donut Keju", `{"rasa":"keju"}`)

	component := `insert into components (tenant_id, name, unit_label) values ($1, $2, 'porsi')`
	s.dough = insertID(t, d, component, tenant, "Adonan donut")
	s.chocTopping = insertID(t, d, component, tenant, "Topping coklat")
	s.cheeseTopng = insertID(t, d, component, tenant, "Topping keju")

	uses := `insert into variant_components (tenant_id, variant_id, component_id, units_per_item) values ($1, $2, $3, $4)`
	insertID(t, d, uses, tenant, s.chocolate, s.dough, 1)
	insertID(t, d, uses, tenant, s.chocolate, s.chocTopping, 1)
	insertID(t, d, uses, tenant, s.cheese, s.dough, 1)
	insertID(t, d, uses, tenant, s.cheese, s.cheeseTopng, 1.5)

	s.flour = insertID(t, d, `insert into ingredients (tenant_id, name, base_unit) values ($1, 'Tepung terigu', 'g')`, tenant)
	s.egg = insertID(t, d, `insert into ingredients (tenant_id, name, base_unit, is_perishable, leftover_policy, shelf_life_days)
	                        values ($1, 'Telur', 'pcs', true, 'confirm', 14)`, tenant)
	s.cocoa = insertID(t, d, `insert into ingredients (tenant_id, name, base_unit) values ($1, 'Coklat bubuk', 'g')`, tenant)

	line := `insert into component_ingredients (tenant_id, component_id, ingredient_id, model_type, params, waste_factor)
	         values ($1, $2, $3, $4, $5, $6)`
	insertID(t, d, line, tenant, s.dough, s.flour, "power", `{"a":100,"b":0.926}`, 1.02)
	insertID(t, d, line, tenant, s.dough, s.egg, "affine", `{"a":0,"b":0.4}`, 1)
	insertID(t, d, line, tenant, s.chocTopping, s.cocoa, "affine", `{"a":5,"b":20}`, 1)

	s.supplier = insertID(t, d, `insert into suppliers (tenant_id, name) values ($1, 'Toko Bahan Kue')`, tenant)
	pack := `insert into ingredient_suppliers (tenant_id, ingredient_id, supplier_id, pack_size, pack_unit, price_idr, is_default)
	         values ($1, $2, $3, $4, $5, $6, $7)`
	insertID(t, d, pack, tenant, s.flour, s.supplier, 1000, "sak 1 kg", 15000, true)
	insertID(t, d, pack, tenant, s.flour, s.supplier, 25000, "karung 25 kg", 300000, false)
	insertID(t, d, pack, tenant, s.egg, s.supplier, 10, "tray", nil, true)
	return s
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

const (
	ok             = ""
	checkViolation = "23514"
	uniqueViolated = "23505"
	fkViolation    = "23503"
)

func TestCatalogSchema_Constraints(t *testing.T) {
	d := dbtest.New(t)
	a := seedDonutShop(t, d, dbtest.DefaultTenantID)
	b := seedDonutShop(t, d, dbtest.CreateTenant(t, d, "Toko Lain"))

	variant := `insert into product_variants (tenant_id, product_id, sku, name, price_idr, production_minutes, min_notice_hours)
	            values ($1, $2, $3, 'X', $4, $5, 24)`
	line := `insert into component_ingredients (tenant_id, component_id, ingredient_id, model_type, params, measured_points, waste_factor)
	         values ($1, $2, $3, $4, $5, $6, $7)`
	ingredient := `insert into ingredients (tenant_id, name, base_unit, is_perishable, leftover_policy) values ($1, $2, $3, $4, $5)`
	pack := `insert into ingredient_suppliers (tenant_id, ingredient_id, supplier_id, pack_size, pack_unit, is_default) values ($1, $2, $3, $4, 'x', $5)`
	uses := `insert into variant_components (tenant_id, variant_id, component_id, units_per_item) values ($1, $2, $3, $4)`

	tests := []struct {
		name string
		sql  string
		args []any
		want string
	}{
		{"production takes 240 minutes at most", variant, []any{a.tenant, a.product, "V-240", 8000, 240}, ok},
		{"production of 0 minutes", variant, []any{a.tenant, a.product, "V-0", 8000, 0}, checkViolation},
		{"production over 4 hours", variant, []any{a.tenant, a.product, "V-241", 8000, 241}, checkViolation},
		{"negative price", variant, []any{a.tenant, a.product, "V-NEG", -1, 60}, checkViolation},
		{"SKU taken in the same tenant", variant, []any{a.tenant, a.product, "DONUT-KEJU", 8000, 60}, uniqueViolated},
		{"slug that is not kebab-case", `insert into products (tenant_id, name, slug) values ($1, 'Kue', 'Kue Lapis')`, []any{a.tenant}, checkViolation},
		{"slug taken in the same tenant", `insert into products (tenant_id, name, slug) values ($1, 'Donut 2', 'donut')`, []any{a.tenant}, uniqueViolated},
		{"variant options that are not an object", `insert into product_variants (tenant_id, product_id, sku, name, options, price_idr, production_minutes, min_notice_hours) values ($1, $2, 'V-OPT', 'X', '[1]', 1, 1, 1)`, []any{a.tenant, a.product}, checkViolation},
		{"zero units of a component per item", uses, []any{a.tenant, a.chocolate, a.cheeseTopng, 0}, checkViolation},
		{"the same component twice in a variant", uses, []any{a.tenant, a.chocolate, a.dough, 2}, uniqueViolated},
		{"waste factor below 1", line, []any{a.tenant, a.chocTopping, a.flour, "affine", `{"a":1,"b":1}`, `[]`, 0.9}, checkViolation},
		{"unknown model type", line, []any{a.tenant, a.chocTopping, a.flour, "linear", `{"a":1,"b":1}`, `[]`, 1}, checkViolation},
		{"params that are not an object", line, []any{a.tenant, a.chocTopping, a.flour, "affine", `[1,2]`, `[]`, 1}, checkViolation},
		{"measured points that are not an array", line, []any{a.tenant, a.chocTopping, a.flour, "affine", `{"a":1,"b":1}`, `{}`, 1}, checkViolation},
		{"the same ingredient twice in a component", line, []any{a.tenant, a.dough, a.flour, "affine", `{"a":1,"b":1}`, `[]`, 1}, uniqueViolated},
		{"base unit other than g, ml, pcs", ingredient, []any{a.tenant, "Gula", "kg", false, "auto"}, checkViolation},
		{"perishable counted without a check", ingredient, []any{a.tenant, "Butter", "g", true, "auto"}, checkViolation},
		{"perishable that must be checked", ingredient, []any{a.tenant, "Butter", "g", true, "confirm"}, ok},
		{"ingredient name taken, any case", ingredient, []any{a.tenant, "TEPUNG TERIGU", "g", false, "auto"}, uniqueViolated},
		{"WhatsApp supplier without a phone", `insert into suppliers (tenant_id, name, adapter_key) values ($1, 'Pasar', 'whatsapp')`, []any{a.tenant}, checkViolation},
		{"phone not in E.164", `insert into suppliers (tenant_id, name, whatsapp_phone) values ($1, 'Pasar', '08123456789')`, []any{a.tenant}, checkViolation},
		{"WhatsApp supplier with an E.164 phone", `insert into suppliers (tenant_id, name, whatsapp_phone, adapter_key) values ($1, 'Pasar', '+628123456789', 'whatsapp')`, []any{a.tenant}, ok},
		{"empty pack", pack, []any{a.tenant, a.cocoa, a.supplier, 0, false}, checkViolation},
		{"second default pack for an ingredient", pack, []any{a.tenant, a.flour, a.supplier, 5000, true}, uniqueViolated},
		{"another non-default pack", pack, []any{a.tenant, a.flour, a.supplier, 5000, false}, ok},

		// The same codes and names are free in another tenant...
		{"SKU reused by another tenant", variant, []any{b.tenant, b.product, "DONUT-COKLAT-2", 8000, 60}, ok},
		// ...but no row may join two tenants.
		{"variant of another tenant's product", variant, []any{a.tenant, b.product, "V-X", 8000, 60}, fkViolation},
		{"recipe using another tenant's component", uses, []any{a.tenant, a.chocolate, b.cheeseTopng, 1}, fkViolation},
		{"recipe line with another tenant's ingredient", line, []any{a.tenant, a.chocTopping, b.flour, "affine", `{"a":1,"b":1}`, `[]`, 1}, fkViolation},
		{"pack from another tenant's supplier", pack, []any{a.tenant, a.cocoa, b.supplier, 500, false}, fkViolation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := d.Pool().Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(t.Context()) }()

			_, err = tx.Exec(t.Context(), tt.sql, tt.args...)

			if got := pgCode(err); got != tt.want || (tt.want == ok && err != nil) {
				t.Errorf("error = %v (code %q), want code %q", err, got, tt.want)
			}
		})
	}
}

func TestComponentsOfVariants(t *testing.T) {
	d := dbtest.New(t)
	a := seedDonutShop(t, d, dbtest.DefaultTenantID)
	b := seedDonutShop(t, d, dbtest.CreateTenant(t, d, "Toko Lain"))
	q := postgres.New(d.Pool())

	rows, err := q.ComponentsOfVariants(t.Context(), postgres.ComponentsOfVariantsParams{
		TenantID: a.tenant, VariantIds: []uuid.UUID{a.chocolate, a.cheese},
	})
	if err != nil {
		t.Fatalf("ComponentsOfVariants() error = %v", err)
	}

	type use struct {
		variant, component uuid.UUID
		units              float64
	}
	want := []use{
		{a.chocolate, a.dough, 1}, {a.chocolate, a.chocTopping, 1},
		{a.cheese, a.dough, 1}, {a.cheese, a.cheeseTopng, 1.5},
	}
	got := make([]use, len(rows))
	for i, r := range rows {
		got[i] = use{r.VariantID, r.ComponentID, r.UnitsPerItem}
	}
	sortUses := func(u []use) {
		slices.SortFunc(u, func(x, y use) int {
			if c := compareUUID(x.variant, y.variant); c != 0 {
				return c
			}
			return compareUUID(x.component, y.component)
		})
	}
	sortUses(want)
	if !slices.Equal(got, want) {
		t.Errorf("rows = %v, want %v in (variant, component) order", got, want)
	}

	// Scoped by tenant: tenant B cannot read tenant A's recipes.
	other, err := q.ComponentsOfVariants(t.Context(), postgres.ComponentsOfVariantsParams{
		TenantID: b.tenant, VariantIds: []uuid.UUID{a.chocolate, a.cheese},
	})
	if err != nil || len(other) != 0 {
		t.Errorf("another tenant's read = %v, %v; want no rows", other, err)
	}
}

func compareUUID(x, y uuid.UUID) int {
	return slices.Compare(x[:], y[:])
}

func TestIngredientsOfComponents_FeedTheRecipeEngine(t *testing.T) {
	d := dbtest.New(t)
	a := seedDonutShop(t, d, dbtest.DefaultTenantID)
	q := postgres.New(d.Pool())

	rows, err := q.IngredientsOfComponents(t.Context(), postgres.IngredientsOfComponentsParams{
		TenantID: a.tenant, ComponentIds: []uuid.UUID{a.dough, a.chocTopping, a.cheeseTopng},
	})
	if err != nil {
		t.Fatalf("IngredientsOfComponents() error = %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 recipe lines", len(rows))
	}
	for i := 1; i < len(rows); i++ {
		prev, cur := rows[i-1], rows[i]
		if c := compareUUID(prev.ComponentID, cur.ComponentID); c > 0 || (c == 0 && compareUUID(prev.IngredientID, cur.IngredientID) > 0) {
			t.Errorf("rows are not in (component, ingredient) order")
		}
	}

	// §11: 6 chocolate + 6 cheese donuts = 12 portions of dough, resolved once.
	need := map[uuid.UUID]int64{}
	for _, r := range rows {
		if r.ComponentID != a.dough {
			continue
		}
		m, err := recipe.Build(recipe.ModelType(r.ModelType), r.Params)
		if err != nil {
			t.Fatalf("recipe.Build(%s, %s) error = %v", r.ModelType, r.Params, err)
		}
		rounding := recipe.RoundNearest
		if r.BaseUnit == "pcs" {
			rounding = recipe.RoundUp
		}
		qty, err := recipe.Quantity(m, 12, r.WasteFactor, rounding)
		if err != nil {
			t.Fatalf("recipe.Quantity() error = %v", err)
		}
		need[r.IngredientID] = qty
		if r.Version != 1 {
			t.Errorf("version = %d, want 1 for a new line", r.Version)
		}
	}
	if need[a.egg] != 5 { // 0.4 * 12 = 4.8 eggs, rounded up
		t.Errorf("eggs = %d, want 5", need[a.egg])
	}
	if need[a.flour] < 1000 || need[a.flour] > 1100 { // 100 * 12^0.926 * 1.02 ≈ 1016 g
		t.Errorf("flour = %d g, want about 1016", need[a.flour])
	}
}

func TestDefaultPacks(t *testing.T) {
	d := dbtest.New(t)
	a := seedDonutShop(t, d, dbtest.DefaultTenantID)
	q := postgres.New(d.Pool())

	rows, err := q.DefaultPacks(t.Context(), postgres.DefaultPacksParams{
		TenantID: a.tenant, IngredientIds: []uuid.UUID{a.flour, a.egg, a.cocoa},
	})
	if err != nil {
		t.Fatalf("DefaultPacks() error = %v", err)
	}

	byIngredient := map[uuid.UUID]postgres.DefaultPacksRow{}
	for _, r := range rows {
		byIngredient[r.IngredientID] = r
	}
	if len(rows) != 2 {
		t.Errorf("rows = %d, want 2 (cocoa has no pack)", len(rows))
	}
	if f := byIngredient[a.flour]; f.PackSize != 1000 || f.PackUnit != "sak 1 kg" || f.PriceIdr == nil || *f.PriceIdr != 15000 {
		t.Errorf("flour pack = %+v, want the default 1000 g bag at Rp15.000", f)
	}
	if e := byIngredient[a.egg]; e.PackSize != 10 || e.PriceIdr != nil {
		t.Errorf("egg pack = %+v, want a tray of 10 without a price", e)
	}
}
