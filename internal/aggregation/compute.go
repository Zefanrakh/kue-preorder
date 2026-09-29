// Package aggregation turns the orders of a production date into that
// batch's shopping list (docs/architecture.md §11): the components' total
// units (level 1, linear), the ingredients those need (level 2, through the
// non-linear recipe engine), and what to buy after stock and what was
// already ordered, in packs (level 3).
//
// It depends only on recipe, platform, and the interfaces of other modules.
package aggregation

import (
	"cmp"
	"fmt"
	"math"
	"slices"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/orders"
	"github.com/Zefanrakh/kue-preorder/internal/recipe"
)

// ComponentTotal is U_c: how many units of a component a batch makes.
type ComponentTotal struct {
	ComponentID uuid.UUID
	Units       float64
}

// Need is how much of an ingredient a batch uses, in whole units of its
// base unit (g, ml, pcs), before stock and packs.
type Need struct {
	IngredientID uuid.UUID
	Qty          int64
}

// RecipeError names the recipe line a batch could not be computed with. The
// batch is marked failed and nothing is guessed (§11).
type RecipeError struct {
	ComponentID  uuid.UUID
	IngredientID uuid.UUID
	Err          error
}

func (e *RecipeError) Error() string {
	return fmt.Sprintf("recipe of component %s, ingredient %s: %v", e.ComponentID, e.IngredientID, e.Err)
}

func (e *RecipeError) Unwrap() error { return e.Err }

// Totals computes levels 1 and 2 of §11. Level 1 sums each component's
// units over the batch's items; level 2 then evaluates every recipe once, on
// the component's total units: the non-linear saving of a shared dough is
// taken on the whole batch, never per order or per variant. Each component's
// amount is rounded on its own and the amounts are summed as int64 (§10).
// Items and uses may come in any order; the result is sorted by id and does
// not depend on it.
func Totals(items []orders.CommittedItem, uses []catalog.ComponentUse, models []catalog.RecipeModel) ([]ComponentTotal, []Need, error) {
	quantity := map[uuid.UUID]int64{}
	for _, it := range items {
		quantity[it.VariantID] += it.Quantity
	}
	sortedUses := slices.Clone(uses)
	slices.SortFunc(sortedUses, func(a, b catalog.ComponentUse) int {
		return cmp.Or(compareIDs(a.ComponentID, b.ComponentID), compareIDs(a.VariantID, b.VariantID))
	})
	units := map[uuid.UUID]float64{}
	var components []ComponentTotal
	for _, u := range sortedUses {
		q, ok := quantity[u.VariantID]
		if !ok || q <= 0 {
			continue
		}
		if _, seen := units[u.ComponentID]; !seen {
			components = append(components, ComponentTotal{ComponentID: u.ComponentID})
		}
		units[u.ComponentID] += float64(q) * u.UnitsPerItem
	}
	for i := range components {
		components[i].Units = units[components[i].ComponentID]
	}

	sortedModels := slices.Clone(models)
	slices.SortFunc(sortedModels, func(a, b catalog.RecipeModel) int {
		return cmp.Or(compareIDs(a.ComponentID, b.ComponentID), compareIDs(a.IngredientID, b.IngredientID))
	})
	needed := map[uuid.UUID]int64{}
	for _, m := range sortedModels {
		u, ok := units[m.ComponentID]
		if !ok {
			continue
		}
		q, err := recipe.Quantity(m.Model, u, m.WasteFactor, m.Rounding)
		if err != nil {
			return nil, nil, &RecipeError{ComponentID: m.ComponentID, IngredientID: m.IngredientID, Err: err}
		}
		needed[m.IngredientID] += q
	}
	needs := make([]Need, 0, len(needed))
	for id, q := range needed {
		if q > 0 {
			needs = append(needs, Need{IngredientID: id, Qty: q})
		}
	}
	slices.SortFunc(needs, func(a, b Need) int { return compareIDs(a.IngredientID, b.IngredientID) })
	return components, needs, nil
}

// LineStatus is where a shopping list line is in procurement (M4).
type LineStatus string

// Line statuses. Only procurement moves a line past LineNeeded.
const (
	LineNeeded   LineStatus = "needed"
	LineOrdered  LineStatus = "ordered"
	LineReceived LineStatus = "received"
)

// Line is one ingredient of a shopping list, in its base unit.
type Line struct {
	IngredientID uuid.UUID
	Needed       int64
	// UsableStock is the stock this batch counts on (§12), at most Needed.
	UsableStock int64
	// Ordered is what procurement has ordered already; a computation never
	// changes it.
	Ordered int64
	// ToBuy is Needed - UsableStock - Ordered, at least 0.
	ToBuy int64
	// Pack is the ingredient's default pack; nil when it has none, and the
	// line is bought in base units.
	Pack *catalog.DefaultPack
	// Packs is ToBuy rounded up to whole packs; 0 without a Pack.
	Packs  int64
	Status LineStatus
}

// CostIDR is what buying the line's packs costs, when its pack has a price.
func (l Line) CostIDR() (int64, bool) {
	if l.Pack == nil || l.Pack.PriceIDR == nil {
		return 0, false
	}
	return l.Packs * *l.Pack.PriceIDR, true
}

// Shop computes level 3 of §11: what each ingredient still needs after the
// usable stock and what procurement already ordered, rounded up to its
// default pack last (§10). A line procurement has touched (ordered or
// received) stays even when nothing is needed any more, so an order placed
// with a supplier is never silently dropped; when the need grows after
// ordering, the difference shows as more to buy. Lines come sorted by
// ingredient id.
func Shop(needs []Need, stock map[uuid.UUID]int64, existing []Line, packs map[uuid.UUID]catalog.DefaultPack) []Line {
	lines := map[uuid.UUID]*Line{}
	for _, e := range existing {
		if e.Status != LineNeeded {
			lines[e.IngredientID] = &Line{IngredientID: e.IngredientID, Ordered: e.Ordered, Status: e.Status}
		}
	}
	for _, n := range needs {
		l, ok := lines[n.IngredientID]
		if !ok {
			l = &Line{IngredientID: n.IngredientID, Status: LineNeeded}
			lines[n.IngredientID] = l
		}
		l.Needed = n.Qty
	}
	out := make([]Line, 0, len(lines))
	for _, l := range lines {
		l.UsableStock = min(max(stock[l.IngredientID], 0), l.Needed)
		l.ToBuy = max(l.Needed-l.UsableStock-l.Ordered, 0)
		if p, ok := packs[l.IngredientID]; ok && p.Size > 0 {
			l.Pack = &p
			l.Packs = packsFor(l.ToBuy, p.Size)
		}
		out = append(out, *l)
	}
	slices.SortFunc(out, func(a, b Line) int { return compareIDs(a.IngredientID, b.IngredientID) })
	return out
}

// packsFor rounds qty up to whole packs of size. Float division can put 3
// packs a hair above 3; that is still 3.
func packsFor(qty int64, size float64) int64 {
	if qty <= 0 {
		return 0
	}
	return int64(math.Ceil(float64(qty)/size - 1e-9))
}

func compareIDs(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) }
