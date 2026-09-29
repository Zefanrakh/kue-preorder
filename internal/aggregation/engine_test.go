package aggregation_test

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/aggregation"
	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/inventory"
	"github.com/Zefanrakh/kue-preorder/internal/orders"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/audit"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
	"github.com/Zefanrakh/kue-preorder/internal/recipe"
)

var tenant = uuid.MustParse("00000000-0000-0000-0000-000000000001")

func day(d int) clock.Date { return clock.Date{Year: 2026, Month: time.October, Day: d} }

// memBatches keeps batches in memory.
type memBatches struct {
	batches    map[clock.Date]*aggregation.Batch
	components map[uuid.UUID][]aggregation.ComponentTotal
	lines      map[uuid.UUID]map[uuid.UUID]aggregation.Line
	saves      int
	audits     []audit.Entry
	events     []outbox.Event
}

func newMemBatches() *memBatches {
	return &memBatches{batches: map[clock.Date]*aggregation.Batch{}, components: map[uuid.UUID][]aggregation.ComponentTotal{}, lines: map[uuid.UUID]map[uuid.UUID]aggregation.Line{}}
}

func (m *memBatches) EnsureBatch(_ context.Context, _ uuid.UUID, date clock.Date, at time.Time) error {
	if _, ok := m.batches[date]; !ok {
		m.batches[date] = &aggregation.Batch{ID: uuid.New(), Date: date, Status: aggregation.BatchOpen, UpdatedAt: at}
	}
	return nil
}

func (m *memBatches) LockBatch(ctx context.Context, tenantID uuid.UUID, date clock.Date) (aggregation.Batch, error) {
	return m.GetBatch(ctx, tenantID, date)
}

func (m *memBatches) GetBatch(_ context.Context, _ uuid.UUID, date clock.Date) (aggregation.Batch, error) {
	b, ok := m.batches[date]
	if !ok {
		return aggregation.Batch{}, apperr.ErrNotFound
	}
	return *b, nil
}

func (m *memBatches) ListBatches(_ context.Context, _ uuid.UUID, from, to clock.Date) ([]aggregation.Summary, error) {
	var out []aggregation.Summary
	for d, b := range m.batches {
		if !d.Before(from) && !d.After(to) {
			out = append(out, aggregation.Summary{Batch: *b})
		}
	}
	return out, nil
}

func (m *memBatches) UpcomingDates(_ context.Context, _ uuid.UUID, from clock.Date) ([]clock.Date, error) {
	var out []clock.Date
	for d, b := range m.batches {
		if !d.Before(from) && b.Status != aggregation.BatchDone {
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(a, b clock.Date) int { return a.Compare(b) })
	return out, nil
}

func (m *memBatches) ClaimedBefore(_ context.Context, _ uuid.UUID, date clock.Date, ids []uuid.UUID) (map[uuid.UUID]int64, error) {
	out := map[uuid.UUID]int64{}
	for d, b := range m.batches {
		if !d.Before(date) || b.Status == aggregation.BatchDone {
			continue
		}
		for _, l := range m.lines[b.ID] {
			if slices.Contains(ids, l.IngredientID) {
				out[l.IngredientID] += l.UsableStock
			}
		}
	}
	return out, nil
}

func (m *memBatches) Components(_ context.Context, _, batchID uuid.UUID) ([]aggregation.ComponentTotal, error) {
	return m.components[batchID], nil
}

func (m *memBatches) Lines(_ context.Context, _, batchID uuid.UUID) ([]aggregation.Line, error) {
	out := slices.Collect(maps.Values(m.lines[batchID]))
	slices.SortFunc(out, func(a, b aggregation.Line) int { return slices.Compare(a.IngredientID[:], b.IngredientID[:]) })
	return out, nil
}

func (m *memBatches) SaveResult(_ context.Context, _, batchID uuid.UUID, components []aggregation.ComponentTotal, lines []aggregation.Line, at time.Time) error {
	m.saves++
	m.components[batchID] = slices.Clone(components)
	old := m.lines[batchID]
	next := map[uuid.UUID]aggregation.Line{}
	for _, l := range lines {
		if prev, ok := old[l.IngredientID]; ok { // procurement's columns are kept
			l.Ordered, l.Received, l.Status = prev.Ordered, prev.Received, prev.Status
		} else {
			l.Ordered, l.Received, l.Status = 0, 0, aggregation.LineNeeded
		}
		next[l.IngredientID] = l
	}
	for id, l := range old {
		if _, ok := next[id]; !ok && l.Status != aggregation.LineNeeded {
			next[id] = l
		}
	}
	m.lines[batchID] = next
	for _, b := range m.batches {
		if b.ID == batchID {
			b.ComputedAt, b.Error = &at, ""
		}
	}
	return nil
}

func (m *memBatches) SetStatus(_ context.Context, _ uuid.UUID, date clock.Date, from []aggregation.BatchStatus, to aggregation.BatchStatus, at time.Time) (bool, error) {
	b, ok := m.batches[date]
	if !ok || !slices.Contains(from, b.Status) {
		return false, nil
	}
	b.Status, b.UpdatedAt = to, at
	return true, nil
}

func (m *memBatches) AddProcured(_ context.Context, _, batchID, ingredientID uuid.UUID, ordered, received int64, _ time.Time) error {
	l, ok := m.lines[batchID][ingredientID]
	if !ok {
		return apperr.ErrNotFound
	}
	l.Ordered += ordered
	l.Received += received
	switch {
	case l.Ordered > 0:
		l.Status = aggregation.LineOrdered
	case l.Received > 0:
		l.Status = aggregation.LineReceived
	default:
		l.Status = aggregation.LineNeeded
	}
	m.lines[batchID][ingredientID] = l
	return nil
}

func (m *memBatches) OpenDates(context.Context, uuid.UUID) ([]clock.Date, error) {
	var out []clock.Date
	for d, b := range m.batches {
		if b.Status == aggregation.BatchOpen {
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(a, b clock.Date) int { return a.Compare(b) })
	return out, nil
}

func (m *memBatches) Audit(_ context.Context, e audit.Entry) error {
	m.audits = append(m.audits, e)
	return nil
}

func (m *memBatches) Publish(_ context.Context, e outbox.Event) error {
	m.events = append(m.events, e)
	return nil
}

func (m *memBatches) MarkFailed(ctx context.Context, tenantID uuid.UUID, date clock.Date, reason string, at time.Time) error {
	if err := m.EnsureBatch(ctx, tenantID, date, at); err != nil {
		return err
	}
	m.batches[date].Error = reason
	return nil
}

// kitchen is the orders and the catalog of §11's example.
type kitchen struct {
	items   map[clock.Date][]orders.CommittedItem
	cutoffs map[clock.Date]time.Time
	models  []catalog.RecipeModel
	broken  error // what RecipeModels fails with
	packs   []catalog.DefaultPack
	labels  catalog.Labels
	failFor map[clock.Date]bool // CommittedItems fails for these dates
}

func newKitchen() *kitchen {
	supplier := uuid.New()
	return &kitchen{
		items:   map[clock.Date][]orders.CommittedItem{},
		cutoffs: map[clock.Date]time.Time{},
		models:  models(),
		packs:   []catalog.DefaultPack{{IngredientID: flour, SupplierID: supplier, Size: 1000, Unit: "kg", PriceIDR: price(14000)}},
		labels: catalog.Labels{
			Components:  map[uuid.UUID]catalog.Component{dough: {Name: "Adonan donut", UnitLabel: "porsi"}, chocTop: {Name: "Topping coklat"}, cheeseTop: {Name: "Topping keju"}},
			Ingredients: map[uuid.UUID]catalog.Ingredient{flour: {Name: "Tepung", BaseUnit: catalog.Gram}, egg: {Name: "Telur", BaseUnit: catalog.Piece}, cocoa: {Name: "Coklat bubuk"}, grated: {Name: "Keju parut"}},
			Suppliers:   map[uuid.UUID]catalog.Supplier{supplier: {Name: "Toko Sinar"}},
		},
		failFor: map[clock.Date]bool{},
	}
}

func (k *kitchen) CommittedItems(_ context.Context, _ uuid.UUID, date clock.Date) ([]orders.CommittedItem, error) {
	if k.failFor[date] {
		return nil, errors.New("connection reset")
	}
	return k.items[date], nil
}

func (k *kitchen) BatchCutoffs(_ context.Context, _ uuid.UUID, from, to clock.Date) (map[clock.Date]time.Time, error) {
	out := map[clock.Date]time.Time{}
	for d, c := range k.cutoffs {
		if !d.Before(from) && !d.After(to) {
			out[d] = c
		}
	}
	return out, nil
}

func (k *kitchen) ComponentUses(context.Context, uuid.UUID, []uuid.UUID) ([]catalog.ComponentUse, error) {
	return uses(), nil
}

func (k *kitchen) RecipeModels(context.Context, uuid.UUID, []uuid.UUID) ([]catalog.RecipeModel, error) {
	if k.broken != nil {
		return nil, k.broken
	}
	return k.models, nil
}

func (k *kitchen) DefaultPacks(context.Context, uuid.UUID, []uuid.UUID) ([]catalog.DefaultPack, error) {
	return k.packs, nil
}

func (k *kitchen) Labels(context.Context, uuid.UUID) (catalog.Labels, error) { return k.labels, nil }

// shelf is the stock inventory would report as usable, whatever the date.
type shelf map[uuid.UUID]int64

func (s shelf) Usable(_ context.Context, _ uuid.UUID, _ clock.Date, ids []uuid.UUID) (map[uuid.UUID]int64, error) {
	out := map[uuid.UUID]int64{}
	for _, id := range ids {
		if q, ok := s[id]; ok {
			out[id] = q
		}
	}
	return out, nil
}

// pantry records what batches consumed; each ingredient has at most
// available of it in the ledger, when set.
type pantry struct {
	calls     int
	uses      []inventory.Use
	available map[uuid.UUID]int64
}

func (p *pantry) Consume(_ context.Context, _, _ uuid.UUID, uses []inventory.Use, _ uuid.UUID, _ time.Time) ([]inventory.Used, error) {
	p.calls++
	p.uses = append(p.uses, uses...)
	var out []inventory.Used
	for _, u := range uses {
		took := u.Qty
		if a, ok := p.available[u.IngredientID]; ok {
			took = min(a, u.Qty)
		}
		out = append(out, inventory.Used{IngredientID: u.IngredientID, Consumed: took, Missing: u.Qty - took})
	}
	return out, nil
}

type directTx struct{}

func (directTx) Tx(ctx context.Context, fn func(ctx context.Context) error) error { return fn(ctx) }

type caller struct{ roles []identity.Role }

func (c caller) Principal(context.Context) (identity.Principal, error) {
	return identity.Principal{AuthUserID: uuid.New(), TenantID: tenant, Roles: c.roles}, nil
}

type bakery struct {
	repo    *memBatches
	kitchen *kitchen
	shelf   shelf
	pantry  *pantry
	clock   *clock.Fake
	engine  *aggregation.Engine
	service *aggregation.Service
}

func newBakery(roles ...identity.Role) *bakery {
	b := &bakery{repo: newMemBatches(), kitchen: newKitchen(), shelf: shelf{}, clock: clock.NewFake(time.Date(2026, 10, 5, 10, 0, 0, 0, clock.Jakarta))}
	b.engine = aggregation.NewEngine(aggregation.EngineDeps{
		Repo: b.repo, Orders: b.kitchen, Catalog: b.kitchen, Stock: b.shelf, Tx: directTx{}, Clock: b.clock,
		Logger: slog.New(slog.DiscardHandler),
	})
	b.pantry = &pantry{}
	b.service = aggregation.NewService(aggregation.ServiceDeps{
		Engine: b.engine, Repo: b.repo, Catalog: b.kitchen, Stock: b.pantry, Principals: caller{roles: roles}, Tx: directTx{}, Clock: b.clock,
	})
	return b
}

func (b *bakery) recompute(t *testing.T, date clock.Date) {
	t.Helper()
	if err := b.engine.Recompute(t.Context(), tenant, date); err != nil {
		t.Fatalf("Recompute(%s) error = %v", date, err)
	}
}

func (b *bakery) linesOf(date clock.Date) []aggregation.Line {
	lines, _ := b.repo.Lines(context.Background(), tenant, b.repo.batches[date].ID)
	return lines
}

// Computing twice leaves the same shopping list: nothing doubles.
func TestEngine_RecomputeIsIdempotent(t *testing.T) {
	b := newBakery()
	b.kitchen.items[day(7)] = []orders.CommittedItem{{VariantID: chocolate, Quantity: 6}, {VariantID: cheese, Quantity: 6}}

	b.recompute(t, day(7))
	first := b.linesOf(day(7))
	b.recompute(t, day(7))

	if got := b.linesOf(day(7)); !reflect.DeepEqual(got, first) || len(first) != 4 {
		t.Errorf("second run = %+v, want the same 4 lines as the first %+v", got, first)
	}
	batch := b.repo.batches[day(7)]
	if batch.ComputedAt == nil || batch.Error != "" || len(b.repo.components[batch.ID]) != 3 {
		t.Errorf("batch = %+v, want computed with 3 components", batch)
	}
	if f := lineOf(first, flour); f.Pack == nil || f.Packs != 1 || f.ToBuy != f.Needed {
		t.Errorf("flour = %+v, want everything bought (no stock yet) in 1 pack", f)
	}
}

// Orders cancelled: the list shrinks, and empties with the last one.
func TestEngine_FollowsTheOrders(t *testing.T) {
	b := newBakery()
	b.kitchen.items[day(7)] = []orders.CommittedItem{{VariantID: chocolate, Quantity: 6}, {VariantID: cheese, Quantity: 6}}
	b.recompute(t, day(7))

	b.kitchen.items[day(7)] = []orders.CommittedItem{{VariantID: chocolate, Quantity: 6}}
	b.recompute(t, day(7))
	if got := b.linesOf(day(7)); lineOf(got, grated).IngredientID == grated || len(got) != 3 {
		t.Errorf("after the cheese order left: %+v, want no cheese", got)
	}

	b.kitchen.items[day(7)] = nil
	b.recompute(t, day(7))
	if got := b.linesOf(day(7)); len(got) != 0 || len(b.repo.components[b.repo.batches[day(7)].ID]) != 0 {
		t.Errorf("after the last order left: %+v, want nothing to buy", got)
	}
}

// A broken recipe marks the batch failed with names, keeps its last list,
// and returns the RecipeError.
func TestEngine_BrokenRecipeMarksTheBatch(t *testing.T) {
	b := newBakery()
	b.kitchen.items[day(7)] = []orders.CommittedItem{{VariantID: cheese, Quantity: 4}}
	b.recompute(t, day(7))
	before := b.linesOf(day(7))
	b.kitchen.broken = &catalog.BrokenRecipeError{ComponentID: dough, IngredientID: egg, Err: recipe.ErrInvalidParams}

	err := b.engine.Recompute(t.Context(), tenant, day(7))

	var re *aggregation.RecipeError
	if !errors.As(err, &re) || re.ComponentID != dough || re.IngredientID != egg {
		t.Fatalf("Recompute() error = %v, want a RecipeError naming the dough and the egg", err)
	}
	if reason := b.repo.batches[day(7)].Error; !strings.Contains(reason, "Adonan donut") || !strings.Contains(reason, "Telur") {
		t.Errorf("batch error = %q, want the recipe named", reason)
	}
	if got := b.linesOf(day(7)); !reflect.DeepEqual(got, before) {
		t.Errorf("lines = %+v, want the last list kept", got)
	}

	b.kitchen.broken = nil
	b.recompute(t, day(7))
	if b.repo.batches[day(7)].Error != "" {
		t.Error("a successful computation left the error")
	}
}

// A recipe change computes every batch from today on again; one failing
// batch does not stop the others, and past or done batches are left alone.
func TestEngine_RecomputeUpcoming(t *testing.T) {
	b := newBakery()
	for _, d := range []int{4, 5, 7, 9} {
		b.kitchen.items[day(d)] = []orders.CommittedItem{{VariantID: chocolate, Quantity: 2}}
		b.recompute(t, day(d))
	}
	b.repo.batches[day(9)].Status = aggregation.BatchDone
	b.kitchen.failFor[day(7)] = true
	saves := b.repo.saves

	n, err := b.engine.RecomputeUpcoming(t.Context(), tenant)

	if n != 1 || err == nil || !strings.Contains(err.Error(), "2026-10-07") {
		t.Errorf("RecomputeUpcoming() = %d, %v; want the 5th computed and the 7th's failure", n, err)
	}
	if b.repo.saves != saves+1 {
		t.Errorf("%d computations saved, want 1 (the 4th is past and the 9th done)", b.repo.saves-saves)
	}
}

func TestService_Roles(t *testing.T) {
	for _, tc := range []struct {
		roles []identity.Role
		ok    bool
	}{
		{[]identity.Role{identity.RoleKitchen}, true},
		{[]identity.Role{identity.RoleOwner}, true},
		{nil, false},
	} {
		b := newBakery(tc.roles...)
		b.kitchen.items[day(7)] = []orders.CommittedItem{{VariantID: chocolate, Quantity: 1}}
		_, listErr := b.service.List(t.Context(), day(1), day(31))
		_, recErr := b.service.Recompute(t.Context(), day(7))
		_, getErr := b.service.Get(t.Context(), day(7))
		for name, err := range map[string]error{"List": listErr, "Recompute": recErr, "Get": getErr} {
			if tc.ok != (err == nil) || (!tc.ok && !errors.Is(err, apperr.ErrForbidden)) {
				t.Errorf("roles %v %s: error = %v, want allowed %t", tc.roles, name, err, tc.ok)
			}
		}
	}
}

// The CMS sees names, lines by supplier then ingredient (no supplier last),
// the cost of what has a price, and how many lines have none.
func TestService_View(t *testing.T) {
	b := newBakery(identity.RoleKitchen)
	b.kitchen.items[day(7)] = []orders.CommittedItem{{VariantID: chocolate, Quantity: 6}, {VariantID: cheese, Quantity: 6}}

	v, err := b.service.Recompute(t.Context(), day(7))

	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, l := range v.Lines {
		names = append(names, l.IngredientName)
	}
	if want := []string{"Tepung", "Coklat bubuk", "Keju parut", "Telur"}; !slices.Equal(names, want) {
		t.Errorf("lines = %v, want %v", names, want)
	}
	if v.Lines[0].SupplierName != "Toko Sinar" || v.Lines[0].BaseUnit != catalog.Gram || v.CostIDR != 14000 || v.Unpriced != 3 {
		t.Errorf("view = %+v, want flour from Toko Sinar at 14,000 and 3 lines without a price", v)
	}
	if v.Components[0].Name != "Adonan donut" || v.Components[0].UnitLabel != "porsi" || v.Components[0].Units != 12 {
		t.Errorf("components = %+v", v.Components)
	}
	if _, err := b.service.Get(t.Context(), day(8)); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("Get(a day without orders) error = %v, want ErrNotFound", err)
	}
	if _, err := b.service.List(t.Context(), day(10), day(1)); err == nil {
		t.Error("List() accepted a range ending before it starts")
	}

	b.kitchen.broken = &catalog.BrokenRecipeError{ComponentID: dough, IngredientID: flour, Err: recipe.ErrInvalidParams}
	var p *apperr.PreconditionError
	if _, err := b.service.Recompute(t.Context(), day(7)); !errors.As(err, &p) || p.Reason != "broken_recipe" {
		t.Errorf("Recompute() with a broken recipe error = %v, want broken_recipe", err)
	}
}

// Stock is allocated by date: the earlier batch takes what it needs, the
// later one gets what is left and buys the rest. No gram counts twice.
func TestEngine_AllocatesStockByDate(t *testing.T) {
	b := newBakery()
	perDonut := int64(math.Round(100 * math.Pow(12, 0.9) * 1.05)) // flour for 12 portions of dough
	b.shelf[flour] = perDonut + 200
	b.kitchen.items[day(7)] = []orders.CommittedItem{{VariantID: chocolate, Quantity: 6}, {VariantID: cheese, Quantity: 6}}
	b.kitchen.items[day(8)] = []orders.CommittedItem{{VariantID: chocolate, Quantity: 6}, {VariantID: cheese, Quantity: 6}}
	b.recompute(t, day(8)) // the 8th's orders came first and took the whole stock

	if _, err := b.engine.RecomputeFrom(t.Context(), tenant, day(7)); err != nil {
		t.Fatal(err)
	}

	first, second := lineOf(b.linesOf(day(7)), flour), lineOf(b.linesOf(day(8)), flour)
	if first.UsableStock != perDonut || first.ToBuy != 0 {
		t.Errorf("7th = %+v, want all its flour from stock", first)
	}
	if second.UsableStock != 200 || second.ToBuy != perDonut-200 {
		t.Errorf("8th = %+v, want the 200 g left and the rest to buy", second)
	}
	if total := first.UsableStock + second.UsableStock; total > b.shelf[flour] {
		t.Errorf("the batches count %d g of a %d g stock", total, b.shelf[flour])
	}
}

// When an earlier batch grows, RecomputeFrom takes its stock back from the
// later ones.
func TestEngine_RecomputeFromRebalancesLaterBatches(t *testing.T) {
	b := newBakery()
	b.shelf[egg] = 10
	b.kitchen.items[day(7)] = []orders.CommittedItem{{VariantID: cheese, Quantity: 4}}  // 2 eggs
	b.kitchen.items[day(8)] = []orders.CommittedItem{{VariantID: cheese, Quantity: 10}} // 5 eggs
	b.recompute(t, day(8))
	if _, err := b.engine.RecomputeFrom(t.Context(), tenant, day(7)); err != nil {
		t.Fatal(err)
	}
	if e := lineOf(b.linesOf(day(8)), egg); e.UsableStock != 5 {
		t.Fatalf("8th = %+v, want its 5 eggs from stock", e)
	}

	b.kitchen.items[day(7)] = []orders.CommittedItem{{VariantID: cheese, Quantity: 14}} // 7 eggs now
	n, err := b.engine.RecomputeFrom(t.Context(), tenant, day(7))

	if err != nil || n != 2 {
		t.Fatalf("RecomputeFrom() = %d, %v; want both batches", n, err)
	}
	first, second := lineOf(b.linesOf(day(7)), egg), lineOf(b.linesOf(day(8)), egg)
	if first.UsableStock != 7 || second.UsableStock != 3 || second.ToBuy != 2 {
		t.Errorf("7th %+v, 8th %+v; want 7 and 3 from stock, 2 eggs to buy for the 8th", first, second)
	}
}
