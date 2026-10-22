package procurement_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/aggregation"
	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/inventory"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/audit"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
	"github.com/Zefanrakh/kue-preorder/internal/procurement"
)

var tenant = uuid.MustParse("00000000-0000-0000-0000-000000000001")

func wib(day, hour, minute int) time.Time {
	return time.Date(2026, time.October, day, hour, minute, 0, 0, clock.Jakarta)
}

var thursday = clock.Date{Year: 2026, Month: time.October, Day: 8}

// memOrders keeps orders in memory.
type memOrders struct {
	orders map[uuid.UUID]procurement.Order
	audits []audit.Entry
	events []outbox.Event
}

func (m *memOrders) InsertOrder(_ context.Context, _ uuid.UUID, o procurement.Order, _ time.Time) error {
	m.orders[o.ID] = o
	return nil
}

func (m *memOrders) LockOrder(ctx context.Context, tenantID, id uuid.UUID) (procurement.Order, error) {
	return m.GetOrder(ctx, tenantID, id)
}

func (m *memOrders) GetOrder(_ context.Context, _, id uuid.UUID) (procurement.Order, error) {
	o, ok := m.orders[id]
	if !ok {
		return procurement.Order{}, apperr.ErrNotFound
	}
	o.Items = slices.Clone(o.Items)
	return o, nil
}

func (m *memOrders) ListOrders(_ context.Context, _ uuid.UUID, date clock.Date) ([]procurement.Order, error) {
	var out []procurement.Order
	for _, o := range m.orders {
		if o.BatchDate == date {
			out = append(out, o)
		}
	}
	return out, nil
}

func (m *memOrders) item(id uuid.UUID, change func(*procurement.Item) bool) bool {
	for oid, o := range m.orders {
		for i := range o.Items {
			if o.Items[i].ID == id {
				ok := change(&o.Items[i])
				m.orders[oid] = o
				return ok
			}
		}
	}
	return false
}

func (m *memOrders) SetItemReceived(_ context.Context, _, itemID uuid.UUID, qty int64, at time.Time) (bool, error) {
	return m.item(itemID, func(it *procurement.Item) bool {
		if it.Status != procurement.ItemOrdered {
			return false
		}
		it.Status, it.QtyReceived, it.ReceivedAt = procurement.ItemReceived, &qty, &at
		return true
	}), nil
}

func (m *memOrders) SetItemCancelled(_ context.Context, _, itemID uuid.UUID) (bool, error) {
	return m.item(itemID, func(it *procurement.Item) bool {
		if it.Status != procurement.ItemOrdered {
			return false
		}
		it.Status = procurement.ItemCancelled
		return true
	}), nil
}

func (m *memOrders) SetOrderStatus(_ context.Context, _, id uuid.UUID, s procurement.OrderStatus, _ time.Time) error {
	o := m.orders[id]
	o.Status = s
	m.orders[id] = o
	return nil
}

func (m *memOrders) Audit(_ context.Context, e audit.Entry) error {
	m.audits = append(m.audits, e)
	return nil
}

func (m *memOrders) Publish(_ context.Context, e outbox.Event) error {
	m.events = append(m.events, e)
	return nil
}

// list is Thursday's shopping list, recording what procurement tells it.
type list struct {
	batch   aggregation.Batch
	lines   []aggregation.Line
	changes []aggregation.Procured
}

func (l *list) LockShoppingList(_ context.Context, _ uuid.UUID, date clock.Date) (aggregation.Batch, []aggregation.Line, error) {
	if date != l.batch.Date {
		return aggregation.Batch{}, nil, apperr.ErrNotFound
	}
	return l.batch, l.lines, nil
}

func (l *list) Record(_ context.Context, _ uuid.UUID, _ clock.Date, changes []aggregation.Procured) error {
	l.changes = append(l.changes, changes...)
	return nil
}

// shelf records the deliveries put into the stock.
type shelf struct{ deliveries []inventory.Delivery }

func (s *shelf) ReceiveOrdered(_ context.Context, _ uuid.UUID, d inventory.Delivery, _ uuid.UUID, _ time.Time) (inventory.Lot, error) {
	s.deliveries = append(s.deliveries, d)
	return inventory.Lot{ID: uuid.New(), IngredientID: d.IngredientID, Balance: d.Qty}, nil
}

type labels struct{ l catalog.Labels }

func (c labels) Labels(context.Context, uuid.UUID) (catalog.Labels, error) { return c.l, nil }

type caller struct{ roles []identity.Role }

func (c *caller) Principal(context.Context) (identity.Principal, error) {
	return identity.Principal{AuthUserID: uuid.New(), TenantID: tenant, Roles: c.roles}, nil
}

type directTx struct{}

func (directTx) Tx(ctx context.Context, fn func(ctx context.Context) error) error { return fn(ctx) }

type kitchen struct {
	repo                *memOrders
	list                *list
	shelf               *shelf
	caller              *caller
	clock               *clock.Fake
	svc                 *procurement.Service
	sinar               uuid.UUID
	flour, sugar, cocoa uuid.UUID
}

// newKitchen has Thursday's list: 1,800 g of flour (2 packs of 1 kg at
// Rp14.000) and 400 g of sugar (1 pack of 1 kg) from Toko Sinar, and 53 g of
// cocoa without a supplier.
func newKitchen() *kitchen {
	k := &kitchen{
		repo: &memOrders{orders: map[uuid.UUID]procurement.Order{}}, shelf: &shelf{}, caller: &caller{roles: []identity.Role{identity.RoleKitchen}},
		clock: clock.NewFake(wib(7, 16, 0)), sinar: uuid.New(), flour: uuid.New(), sugar: uuid.New(), cocoa: uuid.New(),
	}
	price := int64(14000)
	kg := func(ing uuid.UUID, p *int64) *catalog.DefaultPack {
		return &catalog.DefaultPack{IngredientID: ing, SupplierID: k.sinar, Size: 1000, Unit: "kg", PriceIDR: p}
	}
	k.list = &list{
		batch: aggregation.Batch{ID: uuid.New(), Date: thursday, Status: aggregation.BatchLocked},
		lines: []aggregation.Line{
			{IngredientID: k.flour, Needed: 1800, ToBuy: 1800, Pack: kg(k.flour, &price), Packs: 2, Status: aggregation.LineNeeded},
			{IngredientID: k.sugar, Needed: 400, ToBuy: 400, Pack: kg(k.sugar, nil), Packs: 1, Status: aggregation.LineNeeded},
			{IngredientID: k.cocoa, Needed: 53, ToBuy: 53, Status: aggregation.LineNeeded},
		},
	}
	cat := labels{l: catalog.Labels{
		Ingredients: map[uuid.UUID]catalog.Ingredient{
			k.flour: {Name: "Tepung", BaseUnit: catalog.Gram}, k.sugar: {Name: "Gula", BaseUnit: catalog.Gram}, k.cocoa: {Name: "Coklat bubuk", BaseUnit: catalog.Gram},
		},
		Suppliers: map[uuid.UUID]catalog.Supplier{k.sinar: {Name: "Toko Sinar"}},
	}}
	k.svc = procurement.NewService(procurement.ServiceDeps{
		Repo: k.repo, Shopping: k.list, Stock: k.shelf, Catalog: cat, Adapter: procurement.ManualAdapter{},
		Principals: k.caller, Tx: directTx{}, Clock: k.clock,
	})
	return k
}

func (k *kitchen) order(t *testing.T) procurement.View {
	t.Helper()
	v, err := k.svc.Create(t.Context(), thursday, &k.sinar, "")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return v
}

func reason(err error) string {
	var p *apperr.PreconditionError
	if errors.As(err, &p) {
		return p.Reason
	}
	return ""
}

func itemOf(v procurement.View, name string) procurement.ItemView {
	for _, it := range v.Items {
		if it.IngredientName == name {
			return it
		}
	}
	return procurement.ItemView{}
}

// An order to Toko Sinar takes its lines in whole packs; the cocoa without
// a supplier stays for "Belanja sendiri".
func TestCreate(t *testing.T) {
	k := newKitchen()

	v := k.order(t)

	if v.SupplierName != "Toko Sinar" || v.Status != procurement.OrderOrdered || v.Adapter != "manual" || v.BatchID != k.list.batch.ID || len(v.Items) != 2 {
		t.Fatalf("order = %+v, want flour and sugar from Toko Sinar", v)
	}
	if f := itemOf(v, "Tepung"); f.Qty != 2000 || f.Pack == nil || f.Pack.Count != 2 || f.Status != procurement.ItemOrdered {
		t.Errorf("flour = %+v, want 2 packs of 1 kg", f)
	}
	if v.CostIDR != 28000 {
		t.Errorf("cost = %d, want 28,000 (sugar has no price)", v.CostIDR)
	}
	want := []aggregation.Procured{{IngredientID: k.flour, Ordered: 2000}, {IngredientID: k.sugar, Ordered: 1000}}
	if !slices.Equal(k.list.changes, want) {
		t.Errorf("shopping list told %+v, want %+v", k.list.changes, want)
	}
	if a := k.repo.audits; len(a) != 1 || a[0].Action != "procurement.order.created" || a[0].Reason != "Pesan ke Toko Sinar" {
		t.Errorf("audits = %+v", a)
	}

	own, err := k.svc.Create(t.Context(), thursday, nil, "Beli di pasar")
	if err != nil || own.SupplierName != "Belanja sendiri" || len(own.Items) != 1 || own.Items[0].Qty != 53 || own.Items[0].Pack != nil {
		t.Errorf("Belanja sendiri = %+v, %v; want the 53 g of cocoa", own, err)
	}
}

func TestCreate_Refuses(t *testing.T) {
	k := newKitchen()
	for i := range k.list.lines {
		k.list.lines[i].ToBuy = 0 // already ordered
	}
	if _, err := k.svc.Create(t.Context(), thursday, &k.sinar, ""); reason(err) != "nothing_to_order" {
		t.Errorf("nothing left: error = %v, want nothing_to_order", err)
	}
	k.list.batch.Status = aggregation.BatchDone
	if _, err := k.svc.Create(t.Context(), thursday, &k.sinar, ""); reason(err) != "batch_done" {
		t.Errorf("done batch: error = %v, want batch_done", err)
	}
	stranger := uuid.New()
	var v *apperr.ValidationError
	if _, err := k.svc.Create(t.Context(), thursday, &stranger, ""); !errors.As(err, &v) || v.Fields["supplier_id"] == "" {
		t.Errorf("unknown supplier: error = %v", err)
	}
	if _, err := k.svc.Create(t.Context(), thursday.AddDays(1), &k.sinar, ""); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("a day without a batch: error = %v, want ErrNotFound", err)
	}
	if len(k.repo.orders) != 0 || len(k.list.changes) != 0 {
		t.Error("a refusal stored an order")
	}
}

// 1.8 kg of the 2 kg of flour arrive: they go into the stock linked to the
// item, the order closes the flour, and the list moves 2 kg out of ordered
// and 1.8 kg into received, so the 200 g short is to buy again. The sugar
// still waits.
func TestReceive(t *testing.T) {
	k := newKitchen()
	v := k.order(t)
	flour, sugar := itemOf(v, "Tepung"), itemOf(v, "Gula")
	k.list.changes = nil

	got, err := k.svc.Receive(t.Context(), v.ID, procurement.ReceiveInput{Items: []procurement.Received{{ItemID: flour.ID, Qty: 1800}}})

	if err != nil {
		t.Fatal(err)
	}
	if d := k.shelf.deliveries; len(d) != 1 || d[0].IngredientID != k.flour || d[0].Qty != 1800 || d[0].ProcurementItemID != flour.ID || !d[0].ReceivedAt.Equal(wib(7, 16, 0)) {
		t.Errorf("deliveries = %+v, want 1,800 g of flour for the item", d)
	}
	if want := []aggregation.Procured{{IngredientID: k.flour, Ordered: -2000, Received: 1800}}; !slices.Equal(k.list.changes, want) {
		t.Errorf("shopping list told %+v, want %+v", k.list.changes, want)
	}
	if f := itemOf(got, "Tepung"); f.Status != procurement.ItemReceived || *f.QtyReceived != 1800 || got.Status != procurement.OrderOrdered {
		t.Errorf("after the flour: %+v (order %s), want the flour closed, the order still waiting for sugar", f, got.Status)
	}

	got, err = k.svc.Receive(t.Context(), v.ID, procurement.ReceiveInput{Items: []procurement.Received{{ItemID: sugar.ID, Qty: 0}}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != procurement.OrderReceived || len(k.shelf.deliveries) != 1 {
		t.Errorf("after no sugar came: order %s, %d deliveries; want the order closed and nothing stocked", got.Status, len(k.shelf.deliveries))
	}
	if _, err := k.svc.Receive(t.Context(), v.ID, procurement.ReceiveInput{Items: []procurement.Received{{ItemID: sugar.ID, Qty: 1000}}}); reason(err) != "order_closed" {
		t.Errorf("receiving into a closed order: error = %v, want order_closed", err)
	}
}

func TestReceive_Refuses(t *testing.T) {
	k := newKitchen()
	v := k.order(t)
	flour := itemOf(v, "Tepung")
	var ve *apperr.ValidationError
	for name, in := range map[string]procurement.ReceiveInput{
		"nothing ticked": {},
		"negative":       {Items: []procurement.Received{{ItemID: flour.ID, Qty: -1}}},
		"twice":          {Items: []procurement.Received{{ItemID: flour.ID, Qty: 1}, {ItemID: flour.ID, Qty: 1}}},
		"future":         {Items: []procurement.Received{{ItemID: flour.ID, Qty: 1}}, ReceivedAt: wib(8, 0, 0)},
		"unknown item":   {Items: []procurement.Received{{ItemID: uuid.New(), Qty: 1}}},
	} {
		if _, err := k.svc.Receive(t.Context(), v.ID, in); !errors.As(err, &ve) {
			t.Errorf("%s: error = %v, want a validation error", name, err)
		}
	}
	if _, err := k.svc.Receive(t.Context(), v.ID, procurement.ReceiveInput{Items: []procurement.Received{{ItemID: flour.ID, Qty: 2000}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.svc.Receive(t.Context(), v.ID, procurement.ReceiveInput{Items: []procurement.Received{{ItemID: flour.ID, Qty: 2000}}}); reason(err) != "item_closed" {
		t.Errorf("receiving the flour twice: error = %v, want item_closed", err)
	}
	if len(k.shelf.deliveries) != 1 {
		t.Errorf("%d deliveries, want the one", len(k.shelf.deliveries))
	}
}

// Cancelling gives what still waits back to the list; what arrived stays.
func TestCancel(t *testing.T) {
	k := newKitchen()
	v := k.order(t)
	if _, err := k.svc.Receive(t.Context(), v.ID, procurement.ReceiveInput{Items: []procurement.Received{{ItemID: itemOf(v, "Tepung").ID, Qty: 2000}}}); err != nil {
		t.Fatal(err)
	}
	k.list.changes = nil

	got, err := k.svc.Cancel(t.Context(), v.ID, "Gula kosong di toko")

	if err != nil {
		t.Fatal(err)
	}
	if want := []aggregation.Procured{{IngredientID: k.sugar, Ordered: -1000}}; !slices.Equal(k.list.changes, want) {
		t.Errorf("shopping list told %+v, want the sugar back", k.list.changes)
	}
	if got.Status != procurement.OrderReceived || itemOf(got, "Gula").Status != procurement.ItemCancelled || itemOf(got, "Tepung").Status != procurement.ItemReceived {
		t.Errorf("order = %+v, want the flour kept, the sugar cancelled, the order closed as received", got)
	}
	if a := k.repo.audits[len(k.repo.audits)-1]; a.Action != "procurement.order.cancelled" || a.Reason != "Gula kosong di toko" {
		t.Errorf("audit = %+v", a)
	}

	fresh := newKitchen()
	fv := fresh.order(t)
	if _, err := fresh.svc.Cancel(t.Context(), fv.ID, " "); err == nil {
		t.Error("a cancel without a reason passed")
	}
	if got, _ := fresh.svc.Cancel(t.Context(), fv.ID, "Salah pesan"); got.Status != procurement.OrderCancelled {
		t.Errorf("cancelling everything: %s, want cancelled", got.Status)
	}
}

func TestRoles(t *testing.T) {
	k := newKitchen()
	k.caller.roles = nil
	if _, err := k.svc.Create(t.Context(), thursday, &k.sinar, ""); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("customer Create() error = %v, want ErrForbidden", err)
	}
	if _, err := k.svc.List(t.Context(), thursday); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("customer List() error = %v, want ErrForbidden", err)
	}
	k.caller.roles = []identity.Role{identity.RoleOwner}
	if _, err := k.svc.List(t.Context(), thursday); err != nil {
		t.Errorf("owner List() error = %v", err)
	}
}
