//go:build integration

package catalog_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/catalog/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

var now = time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)

type principals struct {
	p   identity.Principal
	err error
}

func (s principals) Principal(context.Context) (identity.Principal, error) { return s.p, s.err }

// caller is a staff member (or customer) of a tenant.
type caller struct {
	tenant uuid.UUID
	user   uuid.UUID
	roles  []identity.Role
}

func (c caller) service(d *db.DB) *catalog.Service {
	p := identity.Principal{AuthUserID: c.user, TenantID: c.tenant, Roles: c.roles}
	return catalog.NewService(postgres.NewRepository(d), principals{p: p}, clock.NewFake(now))
}

func anonymous(d *db.DB) *catalog.Service {
	return catalog.NewService(postgres.NewRepository(d), principals{err: identity.ErrUnauthenticated}, clock.NewFake(now))
}

func owner(tenant uuid.UUID) caller {
	return caller{tenant: tenant, user: uuid.New(), roles: []identity.Role{identity.RoleOwner}}
}

func kitchen(tenant uuid.UUID) caller {
	return caller{tenant: tenant, user: uuid.New(), roles: []identity.Role{identity.RoleKitchen}}
}

func customer(tenant uuid.UUID) caller { return caller{tenant: tenant, user: uuid.New()} }

// noErr fails the test on an unexpected error.
func noErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// shop is a small catalog made by an owner.
type shop struct {
	product  catalog.Product
	variant  catalog.Variant
	flour    catalog.Ingredient
	supplier catalog.Supplier
	bag      catalog.Pack
}

func seedShop(t *testing.T, s *catalog.Service) shop {
	t.Helper()
	ctx := t.Context()
	var (
		out shop
		err error
	)
	out.product, err = s.CreateProduct(ctx, catalog.ProductInput{Name: "Donut", Slug: "donut", Active: true})
	noErr(t, err)
	out.variant, err = s.CreateVariant(ctx, out.product.ID, catalog.VariantInput{
		SKU: "donut-coklat", Name: "Donut Coklat",
		Options: map[string]string{"rasa": "coklat"}, ProductionMinutes: 90, MinNoticeHours: 24, Active: true,
	}, 8000)
	noErr(t, err)
	out.flour, err = s.CreateIngredient(ctx, catalog.IngredientInput{Name: "Tepung terigu", BaseUnit: catalog.Gram})
	noErr(t, err)
	out.supplier, err = s.CreateSupplier(ctx, catalog.SupplierInput{Name: "Toko Bahan Kue", WhatsAppPhone: "0812-3456-789"})
	noErr(t, err)
	out.bag, err = s.CreatePack(ctx, out.flour.ID, catalog.PackInput{SupplierID: out.supplier.ID, Size: 1000, Unit: "sak 1 kg", PriceIDR: ptr[int64](15000)})
	noErr(t, err)
	return out
}

func ptr[T any](v T) *T { return &v }

func validationFields(t *testing.T, err error) (map[string]string, bool) {
	t.Helper()
	var v *catalog.ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("error = %v, want *catalog.ValidationError", err)
	}
	return v.Fields, v.Conflict
}

func TestService_Authorization(t *testing.T) {
	d := dbtest.New(t)
	tenant := dbtest.DefaultTenantID
	boss := owner(tenant).service(d)
	s := seedShop(t, boss)
	dough, err := boss.CreateComponent(t.Context(), catalog.ComponentInput{Name: "Adonan donut", UnitLabel: "porsi"})
	noErr(t, err)
	variantInput := catalog.VariantInput{SKU: "DONUT-COKLAT", Name: "Donut Coklat", ProductionMinutes: 90, MinNoticeHours: 24}
	packInput := catalog.PackInput{SupplierID: s.supplier.ID, Size: 1000, Unit: "sak 1 kg"}

	// Every Service method, with who may call it (§8).
	ops := []struct {
		name   string
		owners bool // true: owners only; false: owners and the kitchen
		run    func(*catalog.Service) error
	}{
		{"update product", true, func(c *catalog.Service) error {
			_, err := c.UpdateProduct(t.Context(), s.product.ID, catalog.ProductInput{Name: "Donut", Slug: "donut", Active: true})
			return err
		}},
		{"list variants", false, func(c *catalog.Service) error { _, err := c.ListVariants(t.Context(), s.product.ID); return err }},
		// Only the owner's call writes (the others are refused first), so fixed
		// values cannot clash.
		{"create variant", true, func(c *catalog.Service) error {
			in := variantInput
			in.SKU = "DONUT-KEJU"
			_, err := c.CreateVariant(t.Context(), s.product.ID, in, 1000)
			return err
		}},
		{"update variant", true, func(c *catalog.Service) error {
			_, err := c.UpdateVariant(t.Context(), s.variant.ID, variantInput)
			return err
		}},
		{"list components", false, func(c *catalog.Service) error { _, err := c.ListComponents(t.Context()); return err }},
		{"update component", false, func(c *catalog.Service) error {
			_, err := c.UpdateComponent(t.Context(), dough.ID, catalog.ComponentInput{Name: "Adonan donut", UnitLabel: "porsi"})
			return err
		}},
		{"list suppliers", false, func(c *catalog.Service) error { _, err := c.ListSuppliers(t.Context()); return err }},
		{"update supplier", true, func(c *catalog.Service) error {
			_, err := c.UpdateSupplier(t.Context(), s.supplier.ID, catalog.SupplierInput{Name: "Toko Bahan Kue"})
			return err
		}},
		{"create pack", true, func(c *catalog.Service) error {
			in := packInput
			in.Size = 25000
			_, err := c.CreatePack(t.Context(), s.flour.ID, in)
			return err
		}},
		{"update pack", true, func(c *catalog.Service) error { _, err := c.UpdatePack(t.Context(), s.bag.ID, packInput); return err }},
		{"list products", false, func(c *catalog.Service) error { _, err := c.ListProducts(t.Context()); return err }},
		{"list ingredients", false, func(c *catalog.Service) error { _, err := c.ListIngredients(t.Context()); return err }},
		{"list packs", false, func(c *catalog.Service) error { _, err := c.ListPacks(t.Context(), s.flour.ID); return err }},
		{"create component", false, func(c *catalog.Service) error {
			_, err := c.CreateComponent(t.Context(), catalog.ComponentInput{Name: "Adonan " + uuid.NewString(), UnitLabel: "porsi"})
			return err
		}},
		{"create ingredient", false, func(c *catalog.Service) error {
			_, err := c.CreateIngredient(t.Context(), catalog.IngredientInput{Name: "Gula " + uuid.NewString(), BaseUnit: catalog.Gram})
			return err
		}},
		{"update ingredient", false, func(c *catalog.Service) error {
			_, err := c.UpdateIngredient(t.Context(), s.flour.ID, catalog.IngredientInput{Name: "Tepung terigu", BaseUnit: catalog.Gram})
			return err
		}},
		{"create product", true, func(c *catalog.Service) error {
			_, err := c.CreateProduct(t.Context(), catalog.ProductInput{Name: "Bolu", Slug: "bolu-" + uuid.NewString()[:8]})
			return err
		}},
		{"change a price", true, func(c *catalog.Service) error {
			_, err := c.ChangeVariantPrice(t.Context(), s.variant.ID, 8500, "promo")
			return err
		}},
		{"create supplier", true, func(c *catalog.Service) error {
			_, err := c.CreateSupplier(t.Context(), catalog.SupplierInput{Name: "Pasar " + uuid.NewString()})
			return err
		}},
		{"set the default pack", true, func(c *catalog.Service) error { _, err := c.SetDefaultPack(t.Context(), s.bag.ID); return err }},
	}
	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			if err := op.run(owner(tenant).service(d)); err != nil {
				t.Errorf("owner: error = %v, want allowed", err)
			}
			err := op.run(kitchen(tenant).service(d))
			if op.owners && !errors.Is(err, catalog.ErrForbidden) {
				t.Errorf("kitchen: error = %v, want ErrForbidden", err)
			}
			if !op.owners && err != nil {
				t.Errorf("kitchen: error = %v, want allowed", err)
			}
			if err := op.run(customer(tenant).service(d)); !errors.Is(err, catalog.ErrForbidden) {
				t.Errorf("customer: error = %v, want ErrForbidden", err)
			}
			if err := op.run(anonymous(d)); !errors.Is(err, identity.ErrUnauthenticated) {
				t.Errorf("anonymous: error = %v, want ErrUnauthenticated", err)
			}
		})
	}
}

func TestService_StoresNormalizedRecords(t *testing.T) {
	d := dbtest.New(t)
	svc := owner(dbtest.DefaultTenantID).service(d)
	s := seedShop(t, svc)

	if s.variant.SKU != "DONUT-COKLAT" || s.variant.Options["rasa"] != "coklat" || s.variant.PriceIDR != 8000 {
		t.Errorf("variant = %+v", s.variant)
	}
	if s.supplier.WhatsAppPhone != "+628123456789" || s.supplier.Adapter != catalog.AdapterManual {
		t.Errorf("supplier = %+v, want an E.164 number and the manual adapter", s.supplier)
	}
	if !s.product.CreatedAt.Equal(now) || !s.variant.UpdatedAt.Equal(now) {
		t.Errorf("timestamps = %v / %v, want the clock's %v", s.product.CreatedAt, s.variant.UpdatedAt, now)
	}
	egg, err := svc.CreateIngredient(t.Context(), catalog.IngredientInput{Name: "Telur", BaseUnit: catalog.Piece, Perishable: true, ShelfLifeDays: ptr[int32](14)})
	noErr(t, err)
	if egg.LeftoverPolicy != catalog.LeftoverConfirm || *egg.ShelfLifeDays != 14 {
		t.Errorf("egg = %+v, want confirm and 14 days", egg)
	}

	variants, err := svc.ListVariants(t.Context(), s.product.ID)
	noErr(t, err)
	if len(variants) != 1 || variants[0].ID != s.variant.ID {
		t.Errorf("ListVariants() = %+v", variants)
	}
}

func TestService_UpdateVariantKeepsThePrice(t *testing.T) {
	d := dbtest.New(t)
	svc := owner(dbtest.DefaultTenantID).service(d)
	s := seedShop(t, svc)

	got, err := svc.UpdateVariant(t.Context(), s.variant.ID, catalog.VariantInput{
		SKU: "DONUT-COKLAT", Name: "Donut Coklat Lumer", ProductionMinutes: 120, MinNoticeHours: 36,
	})
	noErr(t, err)

	if got.Name != "Donut Coklat Lumer" || got.ProductionMinutes != 120 || got.PriceIDR != 8000 || got.Active {
		t.Errorf("UpdateVariant() = %+v, want new fields and the old price", got)
	}
}

func TestService_Conflicts(t *testing.T) {
	d := dbtest.New(t)
	svc := owner(dbtest.DefaultTenantID).service(d)
	s := seedShop(t, svc)
	ctx := t.Context()

	tests := []struct {
		name  string
		err   error
		field string
	}{
		{"slug in use", func() error {
			_, err := svc.CreateProduct(ctx, catalog.ProductInput{Name: "Donut 2", Slug: "donut"})
			return err
		}(), "slug"},
		{"SKU in use, any case", func() error {
			_, err := svc.CreateVariant(ctx, s.product.ID, catalog.VariantInput{SKU: "Donut-Coklat", Name: "X", ProductionMinutes: 1}, 1)
			return err
		}(), "sku"},
		{"ingredient name in use, any case", func() error {
			_, err := svc.CreateIngredient(ctx, catalog.IngredientInput{Name: "TEPUNG TERIGU", BaseUnit: catalog.Gram})
			return err
		}(), "name"},
		{"same pack twice", func() error {
			_, err := svc.CreatePack(ctx, s.flour.ID, catalog.PackInput{SupplierID: s.supplier.ID, Size: 1000, Unit: "sak lagi"})
			return err
		}(), "size"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields, conflict := validationFields(t, tt.err)
			if fields[tt.field] == "" || !conflict {
				t.Errorf("fields = %v, conflict = %v; want a conflict on %s", fields, conflict, tt.field)
			}
		})
	}
}

func TestService_OtherTenantsRecordsAreInvisible(t *testing.T) {
	d := dbtest.New(t)
	mine := seedShop(t, owner(dbtest.DefaultTenantID).service(d))
	other := owner(dbtest.CreateTenant(t, d, "Toko Lain")).service(d)
	theirs := seedShop(t, other)
	ctx := t.Context()

	if _, err := other.UpdateProduct(ctx, mine.product.ID, catalog.ProductInput{Name: "Hijacked", Slug: "hijacked"}); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("update another tenant's product: error = %v, want ErrNotFound", err)
	}
	if _, err := other.ChangeVariantPrice(ctx, mine.variant.ID, 1, "sabotage"); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("change another tenant's price: error = %v, want ErrNotFound", err)
	}
	_, err := other.CreateVariant(ctx, mine.product.ID, catalog.VariantInput{SKU: "X", Name: "X", ProductionMinutes: 1}, 1)
	if fields, _ := validationFields(t, err); fields["product_id"] == "" {
		t.Errorf("variant of another tenant's product: fields = %v, want product_id", fields)
	}
	_, err = other.CreatePack(ctx, theirs.flour.ID, catalog.PackInput{SupplierID: mine.supplier.ID, Size: 500, Unit: "x"})
	if fields, _ := validationFields(t, err); fields["supplier_id"] == "" {
		t.Errorf("pack from another tenant's supplier: fields = %v, want supplier_id", fields)
	}

	products, err := other.ListProducts(ctx)
	noErr(t, err)
	if len(products) != 1 || products[0].ID != theirs.product.ID {
		t.Errorf("ListProducts() = %+v, want only the other tenant's own product", products)
	}
}

type auditRow struct {
	actor, entity  uuid.UUID
	action, reason string
	before, after  map[string]int64
}

func auditRows(t *testing.T, d *db.DB) []auditRow {
	t.Helper()
	rows, err := d.Pool().Query(t.Context(), "select actor_id, entity_id, action, reason, before, after from audit_log order by created_at")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		var before, after []byte
		if err := rows.Scan(&r.actor, &r.entity, &r.action, &r.reason, &before, &after); err != nil {
			t.Fatal(err)
		}
		if json.Unmarshal(before, &r.before) != nil || json.Unmarshal(after, &r.after) != nil {
			t.Fatalf("before/after = %s/%s", before, after)
		}
		out = append(out, r)
	}
	return out
}

func TestService_ChangeVariantPriceIsAudited(t *testing.T) {
	d := dbtest.New(t)
	boss := owner(dbtest.DefaultTenantID)
	svc := boss.service(d)
	s := seedShop(t, svc)
	ctx := t.Context()

	got, err := svc.ChangeVariantPrice(ctx, s.variant.ID, 9000, "harga coklat naik")
	noErr(t, err)

	if got.PriceIDR != 9000 {
		t.Fatalf("price = %d, want 9000", got.PriceIDR)
	}
	rows := auditRows(t, d)
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.actor != boss.user || r.entity != s.variant.ID || r.action != "catalog.variant.price_changed" ||
		r.reason != "harga coklat naik" || r.before["price_idr"] != 8000 || r.after["price_idr"] != 9000 {
		t.Errorf("audit row = %+v, want who, which variant, 8000 → 9000, and why", r)
	}

	// Setting the same price again is not a change: nothing new is recorded.
	_, err = svc.ChangeVariantPrice(ctx, s.variant.ID, 9000, "klik dua kali")
	noErr(t, err)
	if n := len(auditRows(t, d)); n != 1 {
		t.Errorf("audit rows after a no-op = %d, want still 1", n)
	}
}

func TestService_ChangeVariantPriceRejectsBadInput(t *testing.T) {
	d := dbtest.New(t)
	svc := owner(dbtest.DefaultTenantID).service(d)
	s := seedShop(t, svc)

	for name, tc := range map[string]struct {
		price  int64
		reason string
		field  string
	}{
		"no reason":      {9000, "  ", "reason"},
		"negative price": {-1, "typo", "price_idr"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.ChangeVariantPrice(t.Context(), s.variant.ID, tc.price, tc.reason)
			if fields, _ := validationFields(t, err); fields[tc.field] == "" {
				t.Errorf("fields = %v, want %s", fields, tc.field)
			}
		})
	}
	variants, err := svc.ListVariants(t.Context(), s.product.ID)
	noErr(t, err)
	if variants[0].PriceIDR != 8000 {
		t.Errorf("price = %d, want unchanged 8000", variants[0].PriceIDR)
	}
	if n := len(auditRows(t, d)); n != 0 {
		t.Errorf("audit rows = %d, want 0", n)
	}
}

func TestService_SetDefaultPackMovesTheDefault(t *testing.T) {
	d := dbtest.New(t)
	svc := owner(dbtest.DefaultTenantID).service(d)
	s := seedShop(t, svc)
	ctx := t.Context()
	sack, err := svc.CreatePack(ctx, s.flour.ID, catalog.PackInput{SupplierID: s.supplier.ID, Size: 25000, Unit: "karung 25 kg"})
	noErr(t, err)

	_, err = svc.SetDefaultPack(ctx, s.bag.ID)
	noErr(t, err)
	_, err = svc.SetDefaultPack(ctx, sack.ID)
	noErr(t, err)

	packs, err := svc.ListPacks(ctx, s.flour.ID)
	noErr(t, err)
	if len(packs) != 2 || packs[0].ID != sack.ID || !packs[0].Default || packs[1].Default {
		t.Errorf("packs = %+v, want only the 25 kg sack as default, listed first", packs)
	}
	if _, err := svc.SetDefaultPack(ctx, uuid.New()); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("unknown pack: error = %v, want ErrNotFound", err)
	}
}

func TestService_UpdatesRoundTrip(t *testing.T) {
	d := dbtest.New(t)
	svc := owner(dbtest.DefaultTenantID).service(d)
	s := seedShop(t, svc)
	ctx := t.Context()

	product, err := svc.UpdateProduct(ctx, s.product.ID, catalog.ProductInput{
		Name: "Donut Kentang", Slug: "donut-kentang", Description: "Empuk", ImagePath: "products/donut.jpg",
	})
	noErr(t, err)
	if product.Slug != "donut-kentang" || product.ImagePath != "products/donut.jpg" || product.Active {
		t.Errorf("UpdateProduct() = %+v", product)
	}

	dough, err := svc.CreateComponent(ctx, catalog.ComponentInput{Name: "Adonan donut", UnitLabel: "porsi"})
	noErr(t, err)
	dough, err = svc.UpdateComponent(ctx, dough.ID, catalog.ComponentInput{Name: "Adonan donut kentang", UnitLabel: "loyang"})
	noErr(t, err)
	components, err := svc.ListComponents(ctx)
	noErr(t, err)
	if len(components) != 1 || components[0].Name != "Adonan donut kentang" || components[0].UnitLabel != "loyang" {
		t.Errorf("ListComponents() = %+v", components)
	}

	flour, err := svc.UpdateIngredient(ctx, s.flour.ID, catalog.IngredientInput{Name: "Tepung protein tinggi", BaseUnit: catalog.Gram, LeftoverPolicy: catalog.LeftoverNever})
	noErr(t, err)
	if flour.Name != "Tepung protein tinggi" || flour.LeftoverPolicy != catalog.LeftoverNever {
		t.Errorf("UpdateIngredient() = %+v", flour)
	}

	_, err = svc.UpdateSupplier(ctx, s.supplier.ID, catalog.SupplierInput{Name: "Toko Bahan Kue", WhatsAppPhone: "+62 811 111 111", Adapter: catalog.AdapterWhatsApp})
	noErr(t, err)
	suppliers, err := svc.ListSuppliers(ctx)
	noErr(t, err)
	if len(suppliers) != 1 || suppliers[0].Adapter != catalog.AdapterWhatsApp || suppliers[0].WhatsAppPhone != "+62811111111" {
		t.Errorf("ListSuppliers() = %+v", suppliers)
	}

	bag, err := svc.UpdatePack(ctx, s.bag.ID, catalog.PackInput{SupplierID: s.supplier.ID, SupplierSKU: "TPG-1", Size: 500, Unit: "bungkus 500 g"})
	noErr(t, err)
	if bag.Size != 500 || bag.SupplierSKU != "TPG-1" || bag.PriceIDR != nil || bag.IngredientID != s.flour.ID {
		t.Errorf("UpdatePack() = %+v, want the new size, SKU, no price, same ingredient", bag)
	}

	missing := uuid.New()
	for name, err := range map[string]error{
		"product": func() error {
			_, err := svc.UpdateProduct(ctx, missing, catalog.ProductInput{Name: "x", Slug: "x"})
			return err
		}(),
		"variant": func() error {
			_, err := svc.UpdateVariant(ctx, missing, catalog.VariantInput{SKU: "X", Name: "X", ProductionMinutes: 1})
			return err
		}(),
		"component": func() error {
			_, err := svc.UpdateComponent(ctx, missing, catalog.ComponentInput{Name: "x", UnitLabel: "x"})
			return err
		}(),
		"supplier": func() error { _, err := svc.UpdateSupplier(ctx, missing, catalog.SupplierInput{Name: "x"}); return err }(),
		"pack": func() error {
			_, err := svc.UpdatePack(ctx, missing, catalog.PackInput{SupplierID: s.supplier.ID, Size: 1, Unit: "x"})
			return err
		}(),
	} {
		if !errors.Is(err, catalog.ErrNotFound) {
			t.Errorf("update of a missing %s: error = %v, want ErrNotFound", name, err)
		}
	}
}

// Invalid input is refused before anything reaches the database.
func TestService_RejectsInvalidInputBeforeStoring(t *testing.T) {
	d := dbtest.New(t)
	svc := owner(dbtest.DefaultTenantID).service(d)
	s := seedShop(t, svc)
	ctx := t.Context()
	id := uuid.New()

	calls := map[string]func() error{
		"create product": func() error { _, err := svc.CreateProduct(ctx, catalog.ProductInput{Slug: "BAD SLUG"}); return err },
		"update product": func() error {
			_, err := svc.UpdateProduct(ctx, s.product.ID, catalog.ProductInput{Name: "x", Slug: "no spaces allowed"})
			return err
		},
		"create variant": func() error {
			_, err := svc.CreateVariant(ctx, s.product.ID, catalog.VariantInput{SKU: "X"}, -5)
			return err
		},
		"update variant": func() error {
			_, err := svc.UpdateVariant(ctx, s.variant.ID, catalog.VariantInput{SKU: "X", Name: "X"})
			return err
		},
		"create component": func() error { _, err := svc.CreateComponent(ctx, catalog.ComponentInput{Name: "x"}); return err },
		"update component": func() error {
			_, err := svc.UpdateComponent(ctx, id, catalog.ComponentInput{UnitLabel: "porsi"})
			return err
		},
		"create ingredient": func() error {
			_, err := svc.CreateIngredient(ctx, catalog.IngredientInput{Name: "x", BaseUnit: "kg"})
			return err
		},
		"update ingredient": func() error {
			_, err := svc.UpdateIngredient(ctx, s.flour.ID, catalog.IngredientInput{Name: "x", BaseUnit: catalog.Gram, Perishable: true, LeftoverPolicy: catalog.LeftoverAuto})
			return err
		},
		"create supplier": func() error {
			_, err := svc.CreateSupplier(ctx, catalog.SupplierInput{Name: "x", Adapter: catalog.AdapterWhatsApp})
			return err
		},
		"update supplier": func() error {
			_, err := svc.UpdateSupplier(ctx, s.supplier.ID, catalog.SupplierInput{Name: "x", WhatsAppPhone: "halo"})
			return err
		},
		"create pack": func() error {
			_, err := svc.CreatePack(ctx, s.flour.ID, catalog.PackInput{SupplierID: s.supplier.ID, Unit: "x"})
			return err
		},
		"update pack": func() error {
			_, err := svc.UpdatePack(ctx, s.bag.ID, catalog.PackInput{SupplierID: s.supplier.ID, Size: 1})
			return err
		},
		"variant without a product": func() error {
			_, err := svc.CreateVariant(ctx, uuid.Nil, catalog.VariantInput{SKU: "X", Name: "X", ProductionMinutes: 1}, 1)
			return err
		},
		"pack without an ingredient": func() error {
			_, err := svc.CreatePack(ctx, uuid.Nil, catalog.PackInput{SupplierID: s.supplier.ID, Size: 1, Unit: "x"})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			var v *catalog.ValidationError
			if err := call(); !errors.As(err, &v) || v.Conflict {
				t.Errorf("error = %v, want a *ValidationError that is not a conflict", err)
			}
		})
	}

	// CreateVariant reports every problem at once: the input and the price.
	_, err := svc.CreateVariant(ctx, s.product.ID, catalog.VariantInput{SKU: "X"}, -5)
	if fields, _ := validationFields(t, err); fields["name"] == "" || fields["price_idr"] == "" {
		t.Errorf("fields = %v, want both name and price_idr", fields)
	}

	products, err := svc.ListProducts(ctx)
	noErr(t, err)
	if len(products) != 1 || products[0].Name != "Donut" {
		t.Errorf("products = %+v, want the seeded one untouched", products)
	}
}

func TestService_ListVariantsReportsCorruptOptions(t *testing.T) {
	d := dbtest.New(t)
	svc := owner(dbtest.DefaultTenantID).service(d)
	s := seedShop(t, svc)
	// Only a manual edit of the database can store a non-string option.
	if _, err := d.Pool().Exec(t.Context(), `update product_variants set options = '{"ukuran": 20}' where id = $1`, s.variant.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.ListVariants(t.Context(), s.product.ID); err == nil {
		t.Error("ListVariants() error = nil, want an error naming the bad options")
	}
}
