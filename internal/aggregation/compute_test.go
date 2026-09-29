package aggregation_test

import (
	"errors"
	"math"
	"reflect"
	"slices"
	"testing"

	"github.com/google/uuid"
	"pgregory.net/rapid"

	"github.com/Zefanrakh/kue-preorder/internal/aggregation"
	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/orders"
	"github.com/Zefanrakh/kue-preorder/internal/recipe"
)

// The kitchen of §11's example: chocolate and cheese donuts share one
// dough, each with its own topping.
var (
	chocolate, cheese         = uuid.MustParse("10000000-0000-0000-0000-000000000001"), uuid.MustParse("10000000-0000-0000-0000-000000000002")
	dough, chocTop, cheeseTop = uuid.MustParse("20000000-0000-0000-0000-000000000001"), uuid.MustParse("20000000-0000-0000-0000-000000000002"), uuid.MustParse("20000000-0000-0000-0000-000000000003")
	flour, egg, cocoa, grated = uuid.MustParse("30000000-0000-0000-0000-000000000001"), uuid.MustParse("30000000-0000-0000-0000-000000000002"), uuid.MustParse("30000000-0000-0000-0000-000000000003"), uuid.MustParse("30000000-0000-0000-0000-000000000004")
)

func power(a, b float64) recipe.Model {
	m, err := recipe.NewPower(a, b)
	if err != nil {
		panic(err)
	}
	return m
}

func affine(a, b float64) recipe.Model {
	m, err := recipe.NewAffine(a, b)
	if err != nil {
		panic(err)
	}
	return m
}

func uses() []catalog.ComponentUse {
	return []catalog.ComponentUse{
		{VariantID: chocolate, ComponentID: dough, UnitsPerItem: 1},
		{VariantID: chocolate, ComponentID: chocTop, UnitsPerItem: 1},
		{VariantID: cheese, ComponentID: dough, UnitsPerItem: 1},
		{VariantID: cheese, ComponentID: cheeseTop, UnitsPerItem: 1},
	}
}

func models() []catalog.RecipeModel {
	return []catalog.RecipeModel{
		// Flour grows slower than the dough: the saving of a larger batch.
		{ComponentID: dough, IngredientID: flour, Model: power(100, 0.9), WasteFactor: 1.05, Rounding: recipe.RoundNearest},
		// Half an egg per portion of dough, a fifth per topping: pieces round up.
		{ComponentID: dough, IngredientID: egg, Model: affine(0, 0.5), WasteFactor: 1, Rounding: recipe.RoundUp},
		{ComponentID: chocTop, IngredientID: egg, Model: affine(0, 0.2), WasteFactor: 1, Rounding: recipe.RoundUp},
		{ComponentID: chocTop, IngredientID: cocoa, Model: affine(5, 8), WasteFactor: 1, Rounding: recipe.RoundNearest},
		{ComponentID: cheeseTop, IngredientID: grated, Model: affine(0, 15), WasteFactor: 1, Rounding: recipe.RoundNearest},
	}
}

func needOf(needs []aggregation.Need, id uuid.UUID) int64 {
	for _, n := range needs {
		if n.IngredientID == id {
			return n.Qty
		}
	}
	return 0
}

// §11's example: 6 chocolate and 6 cheese donuts make 12 portions of dough,
// evaluated once at u = 12, never twice at u = 6.
func TestTotals_SharedComponentUsesCombinedUnits(t *testing.T) {
	items := []orders.CommittedItem{{VariantID: chocolate, Quantity: 6}, {VariantID: cheese, Quantity: 6}}

	components, needs, err := aggregation.Totals(items, uses(), models())

	if err != nil {
		t.Fatal(err)
	}
	want := []aggregation.ComponentTotal{{ComponentID: dough, Units: 12}, {ComponentID: chocTop, Units: 6}, {ComponentID: cheeseTop, Units: 6}}
	if !reflect.DeepEqual(components, want) {
		t.Errorf("components = %+v, want %+v", components, want)
	}
	combined := int64(math.Round(100 * math.Pow(12, 0.9) * 1.05))
	separate := 2 * int64(math.Round(100*math.Pow(6, 0.9)*1.05))
	if got := needOf(needs, flour); got != combined || got >= separate {
		t.Errorf("flour = %d, want %d from one dough of 12 (not %d from two of 6)", got, combined, separate)
	}
	// Each component rounds on its own: 6 eggs for the dough and ceil(1.2) = 2
	// for the chocolate topping.
	if got := needOf(needs, egg); got != 8 {
		t.Errorf("eggs = %d, want 6 for the dough plus 2 for the topping", got)
	}
	if got, want := needOf(needs, cocoa), int64(5+8*6); got != want {
		t.Errorf("cocoa = %d, want %d", got, want)
	}
	if got := needOf(needs, grated); got != 90 {
		t.Errorf("grated cheese = %d, want 90", got)
	}
}

// Rounding per component, then summing, is §10's rule: two toppings of 1.2
// eggs each need 2 + 2 = 4 eggs, not ceil(2.4) = 3.
func TestTotals_RoundsPerComponentThenSums(t *testing.T) {
	topA, topB := uuid.New(), uuid.New()
	us := []catalog.ComponentUse{{VariantID: chocolate, ComponentID: topA, UnitsPerItem: 1}, {VariantID: chocolate, ComponentID: topB, UnitsPerItem: 1}}
	ms := []catalog.RecipeModel{
		{ComponentID: topA, IngredientID: egg, Model: affine(0, 0.2), WasteFactor: 1, Rounding: recipe.RoundUp},
		{ComponentID: topB, IngredientID: egg, Model: affine(0, 0.2), WasteFactor: 1, Rounding: recipe.RoundUp},
	}

	_, needs, err := aggregation.Totals([]orders.CommittedItem{{VariantID: chocolate, Quantity: 6}}, us, ms)

	if err != nil || needOf(needs, egg) != 4 {
		t.Errorf("eggs = %d, %v; want 4", needOf(needs, egg), err)
	}
}

// The order of the inputs never changes the result.
func TestTotals_Deterministic(t *testing.T) {
	items := []orders.CommittedItem{{VariantID: chocolate, Quantity: 7}, {VariantID: cheese, Quantity: 5}}
	wantC, wantN, err := aggregation.Totals(items, uses(), models())
	if err != nil {
		t.Fatal(err)
	}
	rapid.Check(t, func(rt *rapid.T) {
		its := rapid.Permutation(items).Draw(rt, "items")
		us := rapid.Permutation(uses()).Draw(rt, "uses")
		ms := rapid.Permutation(models()).Draw(rt, "models")
		c, n, err := aggregation.Totals(its, us, ms)
		if err != nil || !reflect.DeepEqual(c, wantC) || !reflect.DeepEqual(n, wantN) {
			rt.Fatalf("shuffled inputs gave %+v / %+v, want %+v / %+v", c, n, wantC, wantN)
		}
	})
}

func TestTotals_NothingToMake(t *testing.T) {
	if c, n, err := aggregation.Totals(nil, uses(), models()); err != nil || len(c) != 0 || len(n) != 0 {
		t.Errorf("no items: %+v, %+v, %v", c, n, err)
	}
	// A variant without components and a component without recipe lines
	// make nothing to buy.
	plain, bare := uuid.New(), uuid.New()
	c, n, err := aggregation.Totals(
		[]orders.CommittedItem{{VariantID: plain, Quantity: 3}, {VariantID: chocolate, Quantity: 2}},
		[]catalog.ComponentUse{{VariantID: chocolate, ComponentID: bare, UnitsPerItem: 2}},
		models(),
	)
	if err != nil || len(c) != 1 || c[0].Units != 4 || len(n) != 0 {
		t.Errorf("bare component: %+v, %+v, %v; want 4 units and nothing to buy", c, n, err)
	}
}

type failing struct{}

func (failing) Resolve(float64) (float64, error) { return 0, recipe.ErrInvalidResult }
func (failing) Type() recipe.ModelType           { return recipe.Formula }

// A recipe that cannot be evaluated fails the whole batch, naming the line.
func TestTotals_BrokenRecipe(t *testing.T) {
	ms := append(models(), catalog.RecipeModel{ComponentID: cheeseTop, IngredientID: egg, Model: failing{}, WasteFactor: 1})

	_, _, err := aggregation.Totals([]orders.CommittedItem{{VariantID: cheese, Quantity: 1}}, uses(), ms)

	var re *aggregation.RecipeError
	if !errors.As(err, &re) || re.ComponentID != cheeseTop || re.IngredientID != egg || !errors.Is(err, recipe.ErrInvalidResult) {
		t.Errorf("error = %v, want a RecipeError naming the cheese topping and the egg", err)
	}
}

func price(p int64) *int64 { return &p }

func lineOf(lines []aggregation.Line, id uuid.UUID) aggregation.Line {
	for _, l := range lines {
		if l.IngredientID == id {
			return l
		}
	}
	return aggregation.Line{}
}

func TestShop(t *testing.T) {
	supplier := uuid.New()
	packs := map[uuid.UUID]catalog.DefaultPack{
		flour: {IngredientID: flour, SupplierID: supplier, Size: 1000, Unit: "kg", PriceIDR: price(14000)},
		egg:   {IngredientID: egg, SupplierID: supplier, Size: 10, Unit: "pack"},
	}
	needs := []aggregation.Need{{IngredientID: flour, Qty: 2500}, {IngredientID: egg, Qty: 30}, {IngredientID: cocoa, Qty: 53}}

	lines := aggregation.Shop(needs, map[uuid.UUID]int64{flour: 700, cocoa: -5}, nil, packs)

	f := lineOf(lines, flour)
	if f.Needed != 2500 || f.UsableStock != 700 || f.ToBuy != 1800 || f.Packs != 2 || f.Status != aggregation.LineNeeded {
		t.Errorf("flour = %+v, want 1,800 g to buy as 2 packs of 1 kg", f)
	}
	if cost, ok := f.CostIDR(); !ok || cost != 28000 {
		t.Errorf("flour cost = %d, %t; want 28,000", cost, ok)
	}
	if e := lineOf(lines, egg); e.ToBuy != 30 || e.Packs != 3 {
		t.Errorf("eggs = %+v, want exactly 3 packs of 10", e)
	}
	if e := lineOf(lines, egg); func() bool { _, ok := e.CostIDR(); return ok }() {
		t.Error("eggs have a cost without a pack price")
	}
	if c := lineOf(lines, cocoa); c.UsableStock != 0 || c.ToBuy != 53 || c.Pack != nil || c.Packs != 0 {
		t.Errorf("cocoa = %+v, want 53 g in base units, no stock counted", c)
	}
	if got := []uuid.UUID{lines[0].IngredientID, lines[1].IngredientID, lines[2].IngredientID}; !slices.IsSortedFunc(got, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) }) {
		t.Errorf("lines not sorted by ingredient: %v", got)
	}
}

// Stock counts only up to the need.
func TestShop_StockCappedAtTheNeed(t *testing.T) {
	lines := aggregation.Shop([]aggregation.Need{{IngredientID: flour, Qty: 400}}, map[uuid.UUID]int64{flour: 5000}, nil, nil)

	if l := lines[0]; l.UsableStock != 400 || l.ToBuy != 0 {
		t.Errorf("flour = %+v, want 400 from stock and nothing to buy", l)
	}
}

// What procurement ordered is never dropped or overwritten: a grown need
// shows the difference to buy, and a need that vanished keeps the line.
func TestShop_OrderedLinesStay(t *testing.T) {
	existing := []aggregation.Line{
		{IngredientID: flour, Needed: 2000, Ordered: 2000, Status: aggregation.LineOrdered},
		{IngredientID: cocoa, Needed: 50, Ordered: 50, Status: aggregation.LineReceived},
		{IngredientID: egg, Needed: 20, Status: aggregation.LineNeeded},
	}

	lines := aggregation.Shop([]aggregation.Need{{IngredientID: flour, Qty: 2600}}, nil, existing, nil)

	if f := lineOf(lines, flour); f.Needed != 2600 || f.Ordered != 2000 || f.ToBuy != 600 || f.Status != aggregation.LineOrdered {
		t.Errorf("flour = %+v, want 600 more to buy on top of the 2,000 ordered", f)
	}
	if c := lineOf(lines, cocoa); c.IngredientID != cocoa || c.Needed != 0 || c.Ordered != 50 || c.ToBuy != 0 || c.Status != aggregation.LineReceived {
		t.Errorf("cocoa = %+v, want the received line kept with nothing needed", c)
	}
	if e := lineOf(lines, egg); e.IngredientID == egg {
		t.Errorf("eggs = %+v, want the unordered line gone", e)
	}
}
