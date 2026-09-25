//go:build integration

package catalog_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/catalog/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
	"github.com/Zefanrakh/kue-preorder/internal/recipe"
)

type event struct {
	aggregate, eventType string
	payload              map[string]any
}

func outboxEvents(t *testing.T, d *db.DB) []event {
	t.Helper()
	rows, err := d.Pool().Query(t.Context(), "select aggregate, event_type, payload from outbox")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []event
	for rows.Next() {
		var e event
		var payload []byte
		if err := rows.Scan(&e.aggregate, &e.eventType, &payload); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(payload, &e.payload); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

// kitchenShop extends seedShop with components for recipe tests.
type kitchenShop struct {
	shop
	dough, topping catalog.Component
	egg            catalog.Ingredient
}

func seedKitchen(t *testing.T, svc *catalog.Service) kitchenShop {
	t.Helper()
	k := kitchenShop{shop: seedShop(t, svc)}
	var err error
	k.dough, err = svc.CreateComponent(t.Context(), catalog.ComponentInput{Name: "Adonan donut", UnitLabel: "porsi"})
	noErr(t, err)
	k.topping, err = svc.CreateComponent(t.Context(), catalog.ComponentInput{Name: "Topping coklat", UnitLabel: "porsi"})
	noErr(t, err)
	k.egg, err = svc.CreateIngredient(t.Context(), catalog.IngredientInput{Name: "Telur", BaseUnit: catalog.Piece, Perishable: true})
	noErr(t, err)
	return k
}

func sameJSON(t *testing.T, got []byte, want string) bool {
	t.Helper()
	var g, w any
	if json.Unmarshal(got, &g) != nil || json.Unmarshal([]byte(want), &w) != nil {
		return false
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	return string(gb) == string(wb)
}

func TestRecipes_SetRecipeLineFromParamsBumpsVersionOnlyOnChange(t *testing.T) {
	d := dbtest.New(t)
	svc := kitchen(dbtest.DefaultTenantID).service(d)
	k := seedKitchen(t, owner(dbtest.DefaultTenantID).service(d))
	ctx := t.Context()

	line, err := svc.SetRecipeLine(ctx, k.dough.ID, k.flour.ID, catalog.RecipeLineInput{
		ModelType: recipe.Power, Params: json.RawMessage(`{"a":100,"b":0.926}`), WasteFactor: 1.02,
	})
	noErr(t, err)
	if line.Version != 1 || line.ModelType != recipe.Power || line.WasteFactor != 1.02 || !sameJSON(t, line.Params, `{"a":100,"b":0.926}`) {
		t.Errorf("first save = %+v", line)
	}

	// The same recipe written differently is not a change.
	line, err = svc.SetRecipeLine(ctx, k.dough.ID, k.flour.ID, catalog.RecipeLineInput{
		ModelType: recipe.Power, Params: json.RawMessage(`{ "b": 0.926, "a": 100.0 }`), WasteFactor: 1.02,
	})
	noErr(t, err)
	if line.Version != 1 {
		t.Errorf("version after an identical save = %d, want 1", line.Version)
	}

	line, err = svc.SetRecipeLine(ctx, k.dough.ID, k.flour.ID, catalog.RecipeLineInput{
		ModelType: recipe.Power, Params: json.RawMessage(`{"a":100,"b":0.926}`), WasteFactor: 1.05,
	})
	noErr(t, err)
	if line.Version != 2 || line.WasteFactor != 1.05 {
		t.Errorf("after a real change = %+v, want version 2", line)
	}

	// Events written at the same (fake) instant have no defined order, so
	// look them up by content.
	events := outboxEvents(t, d)
	if len(events) != 2 {
		t.Fatalf("outbox events = %d, want 2 (the identical save emits nothing)", len(events))
	}
	if !slices.ContainsFunc(events, func(e event) bool {
		return e.aggregate == "component" && e.eventType == "catalog.recipe_changed" &&
			e.payload["component_id"] == k.dough.ID.String() && e.payload["version"] == float64(2)
	}) {
		t.Errorf("events = %+v, want a catalog.recipe_changed event for version 2 of the dough", events)
	}

	lines, err := svc.ListRecipeLines(ctx, k.dough.ID)
	noErr(t, err)
	if len(lines) != 1 || lines[0].Version != 2 {
		t.Errorf("ListRecipeLines() = %+v", lines)
	}
}

func TestRecipes_SetRecipeLineFromMeasuredPoints(t *testing.T) {
	d := dbtest.New(t)
	svc := kitchen(dbtest.DefaultTenantID).service(d)
	k := seedKitchen(t, owner(dbtest.DefaultTenantID).service(d))
	points := []recipe.Point{{U: 1, Amount: 100}, {U: 2, Amount: 190}, {U: 4, Amount: 360}}

	line, err := svc.SetRecipeLine(t.Context(), k.dough.ID, k.flour.ID, catalog.RecipeLineInput{ModelType: recipe.Power, Points: points})
	noErr(t, err)

	var p struct{ A, B float64 }
	if err := json.Unmarshal(line.Params, &p); err != nil || p.A < 99 || p.A > 101 || p.B < 0.91 || p.B > 0.94 {
		t.Errorf("fitted params = %s, want about a=100, b=0.92", line.Params)
	}
	if !slices.Equal(line.Points, points) || line.WasteFactor != 1 {
		t.Errorf("stored points = %v, waste = %v; want the measurements and no waste", line.Points, line.WasteFactor)
	}

	line, err = svc.SetRecipeLine(t.Context(), k.dough.ID, k.egg.ID, catalog.RecipeLineInput{
		ModelType: recipe.Piecewise, Points: []recipe.Point{{U: 2, Amount: 1}, {U: 1, Amount: 0.5}, {U: 2, Amount: 1}},
	})
	noErr(t, err)
	if !sameJSON(t, line.Params, `{"points":[[1,0.5],[2,1]]}`) {
		t.Errorf("piecewise params = %s, want sorted points with repeats averaged", line.Params)
	}
}

func TestRecipes_RejectsBadRecipes(t *testing.T) {
	d := dbtest.New(t)
	svc := kitchen(dbtest.DefaultTenantID).service(d)
	k := seedKitchen(t, owner(dbtest.DefaultTenantID).service(d))

	formula := func(expr string) catalog.RecipeLineInput {
		params, _ := json.Marshal(map[string]string{"expr": expr})
		return catalog.RecipeLineInput{ModelType: recipe.Formula, Params: params}
	}
	tests := []struct {
		name    string
		in      catalog.RecipeLineInput
		field   string
		wantMsg string
	}{
		{"formula with an unknown variable", formula("x * 2"), "params", "tidak valid"},
		{"formula that shrinks as units grow", formula("100 - u"), "params", "turun"},
		{"formula that goes negative", formula("u - 10"), "params", "negatif"},
		{"unknown model type", catalog.RecipeLineInput{ModelType: "linear", Params: json.RawMessage(`{"a":1,"b":1}`)}, "params", "tidak valid"},
		{"neither params nor points", catalog.RecipeLineInput{ModelType: recipe.Affine}, "params", "Isi parameter"},
		{"waste factor below 1", catalog.RecipeLineInput{ModelType: recipe.Affine, Params: json.RawMessage(`{"a":1,"b":1}`), WasteFactor: 0.9}, "waste_factor", "minimal 1"},
		{"a point at zero units", catalog.RecipeLineInput{ModelType: recipe.Affine, Points: []recipe.Point{{U: 0, Amount: 5}}}, "points", "> 0"},
		{"falling measurements", catalog.RecipeLineInput{ModelType: recipe.Piecewise, Points: []recipe.Point{{U: 1, Amount: 100}, {U: 2, Amount: 50}}}, "points", "tidak bisa dipakai"},
		{"fitting a formula", catalog.RecipeLineInput{ModelType: recipe.Formula, Points: []recipe.Point{{U: 1, Amount: 100}}}, "points", "affine, power, dan piecewise"},
		{"too many reference points", catalog.RecipeLineInput{ModelType: recipe.Affine, Params: json.RawMessage(`{"a":1,"b":1}`), Points: make([]recipe.Point, recipe.MaxPoints+1)}, "points", "Paling banyak 100"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.SetRecipeLine(t.Context(), k.dough.ID, k.flour.ID, tt.in)
			fields, _ := validationFields(t, err)
			if !strings.Contains(fields[tt.field], tt.wantMsg) {
				t.Errorf("fields = %v, want %s to mention %q", fields, tt.field, tt.wantMsg)
			}
		})
	}

	lines, err := svc.ListRecipeLines(t.Context(), k.dough.ID)
	noErr(t, err)
	if len(lines) != 0 || len(outboxEvents(t, d)) != 0 {
		t.Errorf("stored %d lines and %d events, want none", len(lines), len(outboxEvents(t, d)))
	}
}

func TestRecipes_RemoveRecipeLine(t *testing.T) {
	d := dbtest.New(t)
	svc := kitchen(dbtest.DefaultTenantID).service(d)
	k := seedKitchen(t, owner(dbtest.DefaultTenantID).service(d))
	ctx := t.Context()
	_, err := svc.SetRecipeLine(ctx, k.dough.ID, k.egg.ID, catalog.RecipeLineInput{ModelType: recipe.Affine, Params: json.RawMessage(`{"a":0,"b":0.4}`)})
	noErr(t, err)

	noErr(t, svc.RemoveRecipeLine(ctx, k.dough.ID, k.egg.ID))

	lines, err := svc.ListRecipeLines(ctx, k.dough.ID)
	noErr(t, err)
	events := outboxEvents(t, d)
	removal := slices.ContainsFunc(events, func(e event) bool { return e.payload["removed"] == true })
	if len(lines) != 0 || len(events) != 2 || !removal {
		t.Errorf("lines = %v, events = %+v; want no lines and a removal event", lines, events)
	}
	if err := svc.RemoveRecipeLine(ctx, k.dough.ID, k.egg.ID); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("removing it again: error = %v, want ErrNotFound", err)
	}
}

func TestRecipes_SetVariantComponents(t *testing.T) {
	d := dbtest.New(t)
	svc := kitchen(dbtest.DefaultTenantID).service(d)
	k := seedKitchen(t, owner(dbtest.DefaultTenantID).service(d))
	ctx := t.Context()
	both := []catalog.VariantComponent{{ComponentID: k.dough.ID, UnitsPerItem: 1}, {ComponentID: k.topping.ID, UnitsPerItem: 1.5}}

	got, err := svc.SetVariantComponents(ctx, k.variant.ID, both)
	noErr(t, err)
	if len(got) != 2 {
		t.Fatalf("SetVariantComponents() = %v", got)
	}

	// The same set in another order changes nothing.
	_, err = svc.SetVariantComponents(ctx, k.variant.ID, []catalog.VariantComponent{both[1], both[0]})
	noErr(t, err)
	if n := len(outboxEvents(t, d)); n != 1 {
		t.Errorf("events after an identical save = %d, want 1", n)
	}

	// The same components with new units is a change.
	_, err = svc.SetVariantComponents(ctx, k.variant.ID, []catalog.VariantComponent{both[0], {ComponentID: k.topping.ID, UnitsPerItem: 2}})
	noErr(t, err)
	if n := len(outboxEvents(t, d)); n != 2 {
		t.Errorf("events after changing units = %d, want 2", n)
	}

	got, err = svc.SetVariantComponents(ctx, k.variant.ID, []catalog.VariantComponent{{ComponentID: k.dough.ID, UnitsPerItem: 2}})
	noErr(t, err)
	if len(got) != 1 || got[0].ComponentID != k.dough.ID || got[0].UnitsPerItem != 2 {
		t.Errorf("after replacing = %v, want only the dough at 2 units", got)
	}

	// A missing component fails the whole replacement, deletes included.
	_, err = svc.SetVariantComponents(ctx, k.variant.ID, []catalog.VariantComponent{{ComponentID: k.topping.ID, UnitsPerItem: 1}, {ComponentID: uuid.New(), UnitsPerItem: 1}})
	if fields, _ := validationFields(t, err); fields["components"] == "" {
		t.Errorf("fields = %v, want components", fields)
	}
	stored, err := svc.ListVariantComponents(ctx, k.variant.ID)
	noErr(t, err)
	if len(stored) != 1 || stored[0].UnitsPerItem != 2 || len(outboxEvents(t, d)) != 3 {
		t.Errorf("after a failed replacement: stored = %v, events = %d; want the dough at 2 and 3 events", stored, len(outboxEvents(t, d)))
	}

	for name, uses := range map[string][]catalog.VariantComponent{
		"the same component twice": {{ComponentID: k.dough.ID, UnitsPerItem: 1}, {ComponentID: k.dough.ID, UnitsPerItem: 2}},
		"zero units":               {{ComponentID: k.dough.ID, UnitsPerItem: 0}},
	} {
		_, err := svc.SetVariantComponents(ctx, k.variant.ID, uses)
		if fields, _ := validationFields(t, err); fields["components"] == "" {
			t.Errorf("%s: fields = %v, want components", name, fields)
		}
	}
	if _, err := svc.SetVariantComponents(ctx, uuid.New(), nil); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("unknown variant: error = %v, want ErrNotFound", err)
	}

	got, err = svc.SetVariantComponents(ctx, k.variant.ID, nil)
	noErr(t, err)
	if len(got) != 0 {
		t.Errorf("after clearing = %v, want none", got)
	}
}

func TestRecipes_StayInTheTenant(t *testing.T) {
	d := dbtest.New(t)
	mine := seedKitchen(t, owner(dbtest.DefaultTenantID).service(d))
	otherOwner := owner(dbtest.CreateTenant(t, d, "Toko Lain"))
	theirs := seedKitchen(t, otherOwner.service(d))
	other := otherOwner.service(d)
	ctx := t.Context()
	affine := catalog.RecipeLineInput{ModelType: recipe.Affine, Params: json.RawMessage(`{"a":1,"b":1}`)}

	if _, err := other.SetRecipeLine(ctx, mine.dough.ID, theirs.flour.ID, affine); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("recipe line on another tenant's component: error = %v, want ErrNotFound", err)
	}
	_, err := other.SetRecipeLine(ctx, theirs.dough.ID, mine.flour.ID, affine)
	if fields, _ := validationFields(t, err); fields["ingredient_id"] == "" {
		t.Errorf("another tenant's ingredient: fields = %v, want ingredient_id", fields)
	}
	if err := other.RemoveRecipeLine(ctx, mine.dough.ID, mine.flour.ID); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("removing from another tenant's component: error = %v, want ErrNotFound", err)
	}
	if _, err := other.SetVariantComponents(ctx, mine.variant.ID, nil); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("another tenant's variant: error = %v, want ErrNotFound", err)
	}
	_, err = other.SetVariantComponents(ctx, theirs.variant.ID, []catalog.VariantComponent{{ComponentID: mine.dough.ID, UnitsPerItem: 1}})
	if fields, _ := validationFields(t, err); fields["components"] == "" {
		t.Errorf("another tenant's component: fields = %v, want components", fields)
	}
}

func TestRecipes_Authorization(t *testing.T) {
	d := dbtest.New(t)
	tenant := dbtest.DefaultTenantID
	k := seedKitchen(t, owner(tenant).service(d))
	affine := catalog.RecipeLineInput{ModelType: recipe.Affine, Params: json.RawMessage(`{"a":1,"b":1}`)}

	ops := map[string]func(*catalog.Service) error{
		"list variant components": func(c *catalog.Service) error {
			_, err := c.ListVariantComponents(t.Context(), k.variant.ID)
			return err
		},
		"set variant components": func(c *catalog.Service) error {
			_, err := c.SetVariantComponents(t.Context(), k.variant.ID, []catalog.VariantComponent{{ComponentID: k.dough.ID, UnitsPerItem: 1}})
			return err
		},
		"list recipe lines": func(c *catalog.Service) error { _, err := c.ListRecipeLines(t.Context(), k.dough.ID); return err },
		"set a recipe line": func(c *catalog.Service) error {
			_, err := c.SetRecipeLine(t.Context(), k.dough.ID, k.flour.ID, affine)
			return err
		},
		"fit preview": func(c *catalog.Service) error {
			_, err := c.FitPreview(t.Context(), []recipe.Point{{U: 1, Amount: 1}})
			return err
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			for _, c := range []caller{owner(tenant), kitchen(tenant)} {
				if err := op(c.service(d)); err != nil {
					t.Errorf("%v: error = %v, want allowed", c.roles, err)
				}
			}
			if err := op(customer(tenant).service(d)); !errors.Is(err, catalog.ErrForbidden) {
				t.Errorf("customer: error = %v, want ErrForbidden", err)
			}
			if err := op(anonymous(d)); !errors.Is(err, identity.ErrUnauthenticated) {
				t.Errorf("anonymous: error = %v, want ErrUnauthenticated", err)
			}
		})
	}
	if err := customer(tenant).service(d).RemoveRecipeLine(t.Context(), k.dough.ID, k.flour.ID); !errors.Is(err, catalog.ErrForbidden) {
		t.Errorf("customer removing a recipe line: error = %v, want ErrForbidden", err)
	}
	if err := kitchen(tenant).service(d).RemoveRecipeLine(t.Context(), k.dough.ID, k.flour.ID); err != nil {
		t.Errorf("kitchen removing a recipe line: error = %v, want allowed", err)
	}
}

func TestFitPreview_ComparesAllFittableModels(t *testing.T) {
	d := dbtest.New(t)

	results, err := kitchen(dbtest.DefaultTenantID).service(d).FitPreview(t.Context(), []recipe.Point{{U: 1, Amount: 100}, {U: 2, Amount: 190}, {U: 4, Amount: 360}})

	noErr(t, err)
	if len(results) != 3 {
		t.Fatalf("FitPreview() returned %d results, want affine, power, piecewise", len(results))
	}
	for _, r := range results {
		if r.Err != nil || r.Report.R2 < 0.99 {
			t.Errorf("%s: err = %v, R2 = %v", r.Type, r.Err, r.Report.R2)
		}
	}
}

// §11: 6 chocolate and 6 cheese donuts share one dough. The Reader hands the
// batch job everything it needs, in a stable order.
func TestReader_DonutShop(t *testing.T) {
	d := dbtest.New(t)
	tenant := dbtest.DefaultTenantID
	svc := owner(tenant).service(d)
	k := seedKitchen(t, svc)
	ctx := t.Context()
	cheese, err := svc.CreateVariant(ctx, k.product.ID, catalog.VariantInput{SKU: "DONUT-KEJU", Name: "Donut Keju", ProductionMinutes: 90, MinNoticeHours: 24}, 8500)
	noErr(t, err)
	for _, v := range []uuid.UUID{k.variant.ID, cheese.ID} {
		_, err := svc.SetVariantComponents(ctx, v, []catalog.VariantComponent{{ComponentID: k.dough.ID, UnitsPerItem: 1}, {ComponentID: k.topping.ID, UnitsPerItem: 1}})
		noErr(t, err)
	}
	_, err = svc.SetRecipeLine(ctx, k.dough.ID, k.flour.ID, catalog.RecipeLineInput{ModelType: recipe.Power, Params: json.RawMessage(`{"a":100,"b":0.926}`), WasteFactor: 1.02})
	noErr(t, err)
	_, err = svc.SetRecipeLine(ctx, k.dough.ID, k.egg.ID, catalog.RecipeLineInput{ModelType: recipe.Affine, Params: json.RawMessage(`{"a":0,"b":0.4}`)})
	noErr(t, err)
	_, err = svc.SetDefaultPack(ctx, k.bag.ID)
	noErr(t, err)
	reader := catalog.NewReader(postgres.NewRepository(d))

	uses, err := reader.ComponentUses(ctx, tenant, []uuid.UUID{k.variant.ID, cheese.ID})
	noErr(t, err)
	if len(uses) != 4 {
		t.Errorf("ComponentUses() = %v, want 2 components for each of 2 variants", uses)
	}

	models, err := reader.RecipeModels(ctx, tenant, []uuid.UUID{k.dough.ID})
	noErr(t, err)
	need := map[uuid.UUID]int64{}
	for _, m := range models {
		q, err := recipe.Quantity(m.Model, 12, m.WasteFactor, m.Rounding)
		noErr(t, err)
		need[m.IngredientID] = q
	}
	if need[k.egg.ID] != 5 || need[k.flour.ID] < 1000 || need[k.flour.ID] > 1100 {
		t.Errorf("12 portions of dough need %v, want 5 eggs (4.8 rounded up) and about 1016 g flour", need)
	}

	packs, err := reader.DefaultPacks(ctx, tenant, []uuid.UUID{k.flour.ID, k.egg.ID})
	noErr(t, err)
	if len(packs) != 1 || packs[0].Size != 1000 || *packs[0].PriceIDR != 15000 {
		t.Errorf("DefaultPacks() = %+v, want only flour's 1 kg bag", packs)
	}

	variants, err := reader.Variants(ctx, tenant, []uuid.UUID{k.variant.ID, cheese.ID, uuid.New()})
	noErr(t, err)
	byID := map[uuid.UUID]catalog.VariantSummary{}
	for _, v := range variants {
		byID[v.ID] = v
	}
	if len(variants) != 2 || byID[cheese.ID].PriceIDR != 8500 || byID[cheese.ID].Active || byID[k.variant.ID].MinNoticeHours != 24 {
		t.Errorf("Variants() = %+v, want both known variants, inactive ones included", variants)
	}

	// Another tenant reads nothing of this shop.
	otherTenant := dbtest.CreateTenant(t, d, "Toko Lain")
	if got, err := reader.ComponentUses(ctx, otherTenant, []uuid.UUID{k.variant.ID}); err != nil || len(got) != 0 {
		t.Errorf("another tenant's read = %v, %v; want nothing", got, err)
	}
}

func TestReader_BrokenRecipeNamesTheLine(t *testing.T) {
	d := dbtest.New(t)
	tenant := dbtest.DefaultTenantID
	svc := owner(tenant).service(d)
	k := seedKitchen(t, svc)
	_, err := svc.SetRecipeLine(t.Context(), k.dough.ID, k.flour.ID, catalog.RecipeLineInput{ModelType: recipe.Affine, Params: json.RawMessage(`{"a":1,"b":1}`)})
	noErr(t, err)
	// Only a manual edit of the database can store params the engine refuses.
	if _, err := d.Pool().Exec(t.Context(), `update component_ingredients set params = '{"a": -1, "b": 1}'`); err != nil {
		t.Fatal(err)
	}

	_, err = catalog.NewReader(postgres.NewRepository(d)).RecipeModels(t.Context(), tenant, []uuid.UUID{k.dough.ID})

	if !errors.Is(err, catalog.ErrBrokenRecipe) || !errors.Is(err, recipe.ErrInvalidParams) {
		t.Fatalf("RecipeModels() error = %v, want ErrBrokenRecipe wrapping the engine error", err)
	}
	if !strings.Contains(err.Error(), k.dough.ID.String()) || !strings.Contains(err.Error(), k.flour.ID.String()) {
		t.Errorf("error = %q, want it to name the component and the ingredient", err)
	}
}
