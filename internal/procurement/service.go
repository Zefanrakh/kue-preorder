package procurement

import (
	"cmp"
	"context"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/aggregation"
	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/inventory"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/audit"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
)

// Repository is the persistence port of procurement. It works in the
// caller's transaction.
type Repository interface {
	// InsertOrder stores an order and its items.
	InsertOrder(ctx context.Context, tenantID uuid.UUID, o Order, at time.Time) error
	// LockOrder returns an order with its items and holds its row until the
	// transaction ends; GetOrder reads it without a lock. Both return
	// apperr.ErrNotFound.
	LockOrder(ctx context.Context, tenantID, id uuid.UUID) (Order, error)
	GetOrder(ctx context.Context, tenantID, id uuid.UUID) (Order, error)
	// ListOrders returns the orders of a batch date with their items,
	// oldest first.
	ListOrders(ctx context.Context, tenantID uuid.UUID, date clock.Date) ([]Order, error)
	// SetItemReceived and SetItemCancelled close an item still ordered;
	// they report whether it was.
	SetItemReceived(ctx context.Context, tenantID, itemID uuid.UUID, qty int64, at time.Time) (bool, error)
	SetItemCancelled(ctx context.Context, tenantID, itemID uuid.UUID) (bool, error)
	SetOrderStatus(ctx context.Context, tenantID, id uuid.UUID, s OrderStatus, at time.Time) error
	Audit(ctx context.Context, e audit.Entry) error
	Publish(ctx context.Context, e outbox.Event) error
}

// ShoppingLists is what procurement needs from aggregation;
// aggregation.Purchasing implements it.
type ShoppingLists interface {
	LockShoppingList(ctx context.Context, tenant uuid.UUID, date clock.Date) (aggregation.Batch, []aggregation.Line, error)
	Record(ctx context.Context, tenant uuid.UUID, date clock.Date, changes []aggregation.Procured) error
}

// Stock is what procurement needs from inventory; inventory.Receiver
// implements it.
type Stock interface {
	ReceiveOrdered(ctx context.Context, tenant uuid.UUID, d inventory.Delivery, actor uuid.UUID, at time.Time) (inventory.Lot, error)
}

// Catalog is what procurement reads from the catalog; catalog.Reader
// implements it.
type Catalog interface {
	Labels(ctx context.Context, tenantID uuid.UUID) (catalog.Labels, error)
}

// Principals tells who is calling; identity.Service implements it.
type Principals interface {
	Principal(ctx context.Context) (identity.Principal, error)
}

// Transactor runs a unit of work in one transaction; *db.DB implements it.
type Transactor interface {
	Tx(ctx context.Context, fn func(ctx context.Context) error) error
}

// staff buy for the kitchen (§8).
var staff = []identity.Role{identity.RoleOwner, identity.RoleKitchen}

const (
	maxNote        = 500
	maxQty         = 100_000_000
	receivedWithin = 30 * 24 * time.Hour
)

// ServiceDeps are what Service works with.
type ServiceDeps struct {
	Repo       Repository
	Shopping   ShoppingLists
	Stock      Stock
	Catalog    Catalog
	Adapter    Adapter
	Principals Principals
	Tx         Transactor
	Clock      clock.Clock
}

// Service is the kitchen's purchasing in the PWA (§19): orders made from a
// batch's shopping list, one per supplier, and the checklist of what
// arrived. Every change runs in one transaction with the batch or the order
// locked, updates the shopping list at once, and is audited (§22).
type Service struct {
	repo       Repository
	shopping   ShoppingLists
	stock      Stock
	catalog    Catalog
	adapter    Adapter
	principals Principals
	tx         Transactor
	clock      clock.Clock
}

// NewService returns a Service over d.
func NewService(d ServiceDeps) *Service {
	return &Service{
		repo: d.Repo, shopping: d.Shopping, stock: d.Stock, catalog: d.Catalog, adapter: d.Adapter,
		principals: d.Principals, tx: d.Tx, clock: d.Clock,
	}
}

func (s *Service) authorize(ctx context.Context) (identity.Principal, error) {
	p, err := s.principals.Principal(ctx)
	if err != nil {
		return identity.Principal{}, err
	}
	if !slices.ContainsFunc(staff, p.HasRole) {
		return identity.Principal{}, apperr.ErrForbidden
	}
	return p, nil
}

// View is an order as the PWA shows it, with names and its cost.
type View struct {
	Order
	SupplierName string // "Belanja sendiri" without a supplier
	Items        []ItemView
	// CostIDR sums the items whose pack has a price.
	CostIDR int64
}

// ItemView is an item with its ingredient's name and unit.
type ItemView struct {
	Item
	IngredientName string
	BaseUnit       catalog.BaseUnit
}

// ownShopping names an order without a supplier.
const ownShopping = "Belanja sendiri"

// Create orders from the shopping list of date what is still to buy from
// one supplier, in its packs; a nil supplier orders the ingredients without
// one ("Belanja sendiri"). The lines turn ordered and what to buy drops at
// once. Ordering again finds nothing left and is refused, so a double tap
// orders nothing twice. Staff only.
func (s *Service) Create(ctx context.Context, date clock.Date, supplierID *uuid.UUID, note string) (View, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return View{}, err
	}
	labels, err := s.catalog.Labels(ctx, p.TenantID)
	if err != nil {
		return View{}, err
	}
	note = strings.TrimSpace(note)
	f := apperr.Fields{}
	if supplierID != nil {
		_, known := labels.Suppliers[*supplierID]
		f.Check(known, "supplier_id", "Supplier tidak dikenal.")
	}
	f.Check(utf8.RuneCountInString(note) <= maxNote, "note", "Catatan paling panjang 500 karakter.")
	if err := f.Err(); err != nil {
		return View{}, err
	}
	now := s.clock.Now()
	order := Order{
		ID: uuid.New(), BatchDate: date, SupplierID: supplierID, Adapter: s.adapter.Key(), Status: OrderOrdered,
		Note: note, CreatedBy: p.AuthUserID, CreatedAt: now,
	}
	err = s.tx.Tx(ctx, func(ctx context.Context) error {
		batch, lines, err := s.shopping.LockShoppingList(ctx, p.TenantID, date)
		if err != nil {
			return err
		}
		if batch.Status == aggregation.BatchDone {
			return ErrBatchDone
		}
		order.BatchID = batch.ID
		var changes []aggregation.Procured
		for _, l := range lines {
			if l.ToBuy <= 0 || !sameSupplier(l, supplierID) {
				continue
			}
			item := Item{ID: uuid.New(), IngredientID: l.IngredientID, Qty: l.ToBuy, Status: ItemOrdered}
			if l.Pack != nil {
				item.Qty = int64(math.Ceil(float64(l.Packs)*l.Pack.Size - 1e-9))
				item.Pack = &Pack{Count: l.Packs, Size: l.Pack.Size, Unit: l.Pack.Unit, PriceIDR: l.Pack.PriceIDR}
			}
			order.Items = append(order.Items, item)
			changes = append(changes, aggregation.Procured{IngredientID: l.IngredientID, Ordered: item.Qty})
		}
		if len(order.Items) == 0 {
			return ErrNothingToOrder
		}
		// The manual adapter sends nothing, so it may run in the
		// transaction. An adapter that reaches a supplier (WhatsApp, M4.2)
		// sends after the commit instead, through the outbox
		// (send-procurement-order, §21), so a rollback never leaves a
		// message sent for an order that does not exist.
		sent, err := s.adapter.Send(ctx, order)
		if err != nil {
			return err
		}
		order.ExternalRef = sent.ExternalRef
		if err := s.repo.InsertOrder(ctx, p.TenantID, order, now); err != nil {
			return err
		}
		if err := s.shopping.Record(ctx, p.TenantID, date, changes); err != nil {
			return err
		}
		reason := note
		if reason == "" {
			reason = "Pesan ke " + supplierName(labels, supplierID)
		}
		cost, _ := costOf(order.Items)
		if err := s.repo.Audit(ctx, audit.Entry{
			TenantID: p.TenantID, ActorID: p.AuthUserID, Action: "procurement.order.created", Entity: "procurement_order", EntityID: order.ID,
			After:  map[string]any{"date": date.String(), "supplier_id": supplierID, "items": len(order.Items), "cost_idr": cost, "adapter": order.Adapter},
			Reason: reason, At: now,
		}); err != nil {
			return err
		}
		return s.repo.Publish(ctx, orderEvent(p.TenantID, order, "procurement.order_created", now))
	})
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, p.TenantID, labels, order.ID)
}

func sameSupplier(l aggregation.Line, supplierID *uuid.UUID) bool {
	if l.Pack == nil {
		return supplierID == nil
	}
	return supplierID != nil && l.Pack.SupplierID == *supplierID
}

// Received is what arrived for one item.
type Received struct {
	ItemID uuid.UUID
	// Qty is what arrived, in the base unit; 0 when nothing came. It may be
	// more or less than ordered.
	Qty int64
	// ExpiresAt defaults to the ingredient's shelf life after arrival.
	ExpiresAt *time.Time
}

// ReceiveInput is the kitchen's checklist of one delivery.
type ReceiveInput struct {
	Items []Received
	// ReceivedAt defaults to now; it may be up to 30 days back.
	ReceivedAt time.Time
}

// Receive ticks what arrived (§19): each item that came goes into the
// stock as a lot linked to it and is closed; the shopping list moves it from
// ordered to received, so what arrived short is to buy again. Items not in
// the checklist keep waiting. The order closes once none waits. Staff only.
func (s *Service) Receive(ctx context.Context, orderID uuid.UUID, in ReceiveInput) (View, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return View{}, err
	}
	now := s.clock.Now()
	if in.ReceivedAt.IsZero() {
		in.ReceivedAt = now
	}
	f := apperr.Fields{}
	f.Check(len(in.Items) > 0, "items", "Centang bahan yang datang.")
	f.Check(!in.ReceivedAt.After(now.Add(5*time.Minute)), "received_at", "Tanggal terima tidak boleh di masa depan.")
	f.Check(!in.ReceivedAt.Before(now.Add(-receivedWithin)), "received_at", "Tanggal terima paling lama 30 hari yang lalu.")
	seen := map[uuid.UUID]bool{}
	for _, it := range in.Items {
		f.Check(it.Qty >= 0 && it.Qty <= maxQty, "items", "Jumlah yang datang tidak valid.")
		f.Check(!seen[it.ItemID], "items", "Satu bahan dicentang dua kali.")
		seen[it.ItemID] = true
	}
	if err := f.Err(); err != nil {
		return View{}, err
	}
	labels, err := s.catalog.Labels(ctx, p.TenantID)
	if err != nil {
		return View{}, err
	}
	err = s.tx.Tx(ctx, func(ctx context.Context) error {
		o, err := s.repo.LockOrder(ctx, p.TenantID, orderID)
		if err != nil {
			return err
		}
		if o.Status != OrderOrdered {
			return ErrOrderClosed
		}
		items := make(map[uuid.UUID]Item, len(o.Items))
		for _, it := range o.Items {
			items[it.ID] = it
		}
		changes := make([]aggregation.Procured, 0, len(in.Items))
		for _, r := range in.Items {
			item, ok := items[r.ItemID]
			if !ok {
				return &apperr.ValidationError{Fields: map[string]string{"items": "Bahan ini tidak ada di pesanan."}}
			}
			if item.Status != ItemOrdered {
				return ErrItemClosed
			}
			if r.Qty > 0 {
				if _, err := s.stock.ReceiveOrdered(ctx, p.TenantID, inventory.Delivery{
					IngredientID: item.IngredientID, Qty: r.Qty, ReceivedAt: in.ReceivedAt, ExpiresAt: r.ExpiresAt, ProcurementItemID: item.ID,
				}, p.AuthUserID, now); err != nil {
					return err
				}
			}
			if _, err := s.repo.SetItemReceived(ctx, p.TenantID, item.ID, r.Qty, in.ReceivedAt); err != nil {
				return err
			}
			received := r.Qty
			item.Status, item.QtyReceived = ItemReceived, &received
			items[item.ID] = item
			changes = append(changes, aggregation.Procured{IngredientID: item.IngredientID, Ordered: -item.Qty, Received: r.Qty})
		}
		if err := s.shopping.Record(ctx, p.TenantID, o.BatchDate, changes); err != nil {
			return err
		}
		if err := s.closeIfDone(ctx, p.TenantID, o.ID, items, now); err != nil {
			return err
		}
		if err := s.repo.Audit(ctx, audit.Entry{
			TenantID: p.TenantID, ActorID: p.AuthUserID, Action: "procurement.order.received", Entity: "procurement_order", EntityID: o.ID,
			After:  map[string]any{"items": receivedAudit(in.Items, items), "received_at": in.ReceivedAt},
			Reason: "Barang diterima", At: now,
		}); err != nil {
			return err
		}
		return s.repo.Publish(ctx, orderEvent(p.TenantID, o, "procurement.order_received", now))
	})
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, p.TenantID, labels, orderID)
}

// Cancel closes what is still ordered: the shopping list takes it back as
// to buy. What already arrived stays. A reason is required. Staff only.
func (s *Service) Cancel(ctx context.Context, orderID uuid.UUID, reason string) (View, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return View{}, err
	}
	reason = strings.TrimSpace(reason)
	f := apperr.Fields{}
	f.Check(reason != "", "reason", "Isi alasan pembatalan.")
	f.Check(utf8.RuneCountInString(reason) <= maxNote, "reason", "Alasan paling panjang 500 karakter.")
	if err := f.Err(); err != nil {
		return View{}, err
	}
	labels, err := s.catalog.Labels(ctx, p.TenantID)
	if err != nil {
		return View{}, err
	}
	now := s.clock.Now()
	err = s.tx.Tx(ctx, func(ctx context.Context) error {
		o, err := s.repo.LockOrder(ctx, p.TenantID, orderID)
		if err != nil {
			return err
		}
		if o.Status != OrderOrdered {
			return ErrOrderClosed
		}
		items := make(map[uuid.UUID]Item, len(o.Items))
		var changes []aggregation.Procured
		for _, it := range o.Items {
			if it.Status == ItemOrdered {
				if _, err := s.repo.SetItemCancelled(ctx, p.TenantID, it.ID); err != nil {
					return err
				}
				it.Status = ItemCancelled
				changes = append(changes, aggregation.Procured{IngredientID: it.IngredientID, Ordered: -it.Qty})
			}
			items[it.ID] = it
		}
		if err := s.shopping.Record(ctx, p.TenantID, o.BatchDate, changes); err != nil {
			return err
		}
		if err := s.closeIfDone(ctx, p.TenantID, o.ID, items, now); err != nil {
			return err
		}
		if err := s.repo.Audit(ctx, audit.Entry{
			TenantID: p.TenantID, ActorID: p.AuthUserID, Action: "procurement.order.cancelled", Entity: "procurement_order", EntityID: o.ID,
			After: map[string]any{"cancelled_items": len(changes)}, Reason: reason, At: now,
		}); err != nil {
			return err
		}
		return s.repo.Publish(ctx, orderEvent(p.TenantID, o, "procurement.order_cancelled", now))
	})
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, p.TenantID, labels, orderID)
}

// closeIfDone closes the order once none of its items waits: received when
// something arrived, cancelled otherwise.
func (s *Service) closeIfDone(ctx context.Context, tenant, orderID uuid.UUID, items map[uuid.UUID]Item, at time.Time) error {
	status := OrderCancelled
	for _, it := range items {
		switch it.Status {
		case ItemOrdered:
			return nil
		case ItemReceived:
			status = OrderReceived
		}
	}
	return s.repo.SetOrderStatus(ctx, tenant, orderID, status, at)
}

// List returns the orders of a batch date, oldest first. Staff only.
func (s *Service) List(ctx context.Context, date clock.Date) ([]View, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return nil, err
	}
	labels, err := s.catalog.Labels(ctx, p.TenantID)
	if err != nil {
		return nil, err
	}
	orders, err := s.repo.ListOrders(ctx, p.TenantID, date)
	if err != nil {
		return nil, err
	}
	out := make([]View, len(orders))
	for i, o := range orders {
		out[i] = viewOf(labels, o)
	}
	return out, nil
}

// Get returns an order; apperr.ErrNotFound for another tenant's or none.
// Staff only.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (View, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return View{}, err
	}
	labels, err := s.catalog.Labels(ctx, p.TenantID)
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, p.TenantID, labels, id)
}

func (s *Service) view(ctx context.Context, tenant uuid.UUID, labels catalog.Labels, id uuid.UUID) (View, error) {
	o, err := s.repo.GetOrder(ctx, tenant, id)
	if err != nil {
		return View{}, err
	}
	return viewOf(labels, o), nil
}

func viewOf(labels catalog.Labels, o Order) View {
	v := View{Order: o, SupplierName: supplierName(labels, o.SupplierID)}
	v.CostIDR, _ = costOf(o.Items)
	for _, it := range o.Items {
		ing := labels.Ingredients[it.IngredientID]
		v.Items = append(v.Items, ItemView{Item: it, IngredientName: ing.Name, BaseUnit: ing.BaseUnit})
	}
	slices.SortFunc(v.Items, func(a, b ItemView) int { return cmp.Compare(a.IngredientName, b.IngredientName) })
	return v
}

func supplierName(labels catalog.Labels, id *uuid.UUID) string {
	if id == nil {
		return ownShopping
	}
	return labels.Suppliers[*id].Name
}

// costOf sums the items whose pack has a price, and reports whether every
// item had one.
func costOf(items []Item) (int64, bool) {
	var sum int64
	all := true
	for _, it := range items {
		if c, ok := it.CostIDR(); ok {
			sum += c
		} else {
			all = false
		}
	}
	return sum, all
}

func receivedAudit(in []Received, items map[uuid.UUID]Item) []map[string]any {
	out := make([]map[string]any, len(in))
	for i, r := range in {
		it := items[r.ItemID]
		out[i] = map[string]any{"ingredient_id": it.IngredientID, "ordered": it.Qty, "received": r.Qty}
	}
	return out
}

func orderEvent(tenant uuid.UUID, o Order, typ string, at time.Time) outbox.Event {
	return outbox.Event{
		TenantID: tenant, Aggregate: "procurement_order", Type: typ, At: at,
		Payload: map[string]any{"order_id": o.ID, "date": o.BatchDate.String(), "supplier_id": o.SupplierID},
	}
}
