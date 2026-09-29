// Package procurement turns a batch's shopping list into purchase orders,
// one per supplier, and receives what they bring into the stock
// (docs/architecture.md §19). How an order reaches its supplier is an
// Adapter's business: in M4.1 only the manual one, which sends nothing
// while the kitchen orders or shops itself and ticks what arrived.
package procurement

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

// OrderStatus is where a purchase order is.
type OrderStatus string

// Order statuses. An order is closed once none of its items waits.
const (
	OrderOrdered   OrderStatus = "ordered"
	OrderReceived  OrderStatus = "received"  // closed, something arrived
	OrderCancelled OrderStatus = "cancelled" // closed, nothing arrived
)

// ItemStatus is where one line of a purchase order is.
type ItemStatus string

// Item statuses.
const (
	ItemOrdered   ItemStatus = "ordered"
	ItemReceived  ItemStatus = "received"
	ItemCancelled ItemStatus = "cancelled"
)

// Pack is what an item is bought in.
type Pack struct {
	Count    int64
	Size     float64 // in the ingredient's base unit
	Unit     string  // such as "kg"
	PriceIDR *int64
}

// Item is one ingredient of a purchase order.
type Item struct {
	ID           uuid.UUID
	IngredientID uuid.UUID
	// Qty is what was ordered, in the ingredient's base unit.
	Qty int64
	// Pack is nil for an ingredient bought without one.
	Pack   *Pack
	Status ItemStatus
	// QtyReceived and ReceivedAt are set once received; what arrived may be
	// more or less than Qty.
	QtyReceived *int64
	ReceivedAt  *time.Time
}

// CostIDR is what the item's packs cost, when their price is known.
func (i Item) CostIDR() (int64, bool) {
	if i.Pack == nil || i.Pack.PriceIDR == nil {
		return 0, false
	}
	return i.Pack.Count * *i.Pack.PriceIDR, true
}

// Order is a purchase order for one batch from one supplier.
type Order struct {
	ID        uuid.UUID
	BatchID   uuid.UUID
	BatchDate clock.Date
	// SupplierID is nil for "Belanja sendiri": ingredients without a
	// supplier, bought by the kitchen.
	SupplierID  *uuid.UUID
	Adapter     string
	ExternalRef string
	Status      OrderStatus
	Note        string
	CreatedBy   uuid.UUID
	CreatedAt   time.Time
	Items       []Item
}

// Adapter gets a purchase order to its supplier (§19). Receiving stays a
// checklist the kitchen ticks whatever the adapter; the WhatsApp adapter
// (M4.2) adds reading the supplier's replies.
type Adapter interface {
	Key() string
	Send(ctx context.Context, o Order) (SendResult, error)
}

// SendResult is what an adapter learned sending an order.
type SendResult struct {
	ExternalRef string // such as a message id; empty when nothing was sent
}

// ManualAdapter sends nothing: the kitchen orders or shops itself.
type ManualAdapter struct{}

// Key implements Adapter.
func (ManualAdapter) Key() string { return "manual" }

// Send implements Adapter.
func (ManualAdapter) Send(context.Context, Order) (SendResult, error) { return SendResult{}, nil }

// Errors of purchase orders, in words the PWA shows as they are.
var (
	ErrNothingToOrder = &apperr.PreconditionError{
		Reason:  "nothing_to_order",
		Message: "Tidak ada yang perlu dipesan dari supplier ini untuk tanggal itu.",
	}
	ErrBatchDone = &apperr.PreconditionError{
		Reason:  "batch_done",
		Message: "Produksi hari itu sudah selesai.",
	}
	ErrOrderClosed = &apperr.PreconditionError{
		Reason:  "order_closed",
		Message: "Pesanan ini sudah diterima atau dibatalkan.",
	}
	ErrItemClosed = &apperr.PreconditionError{
		Reason:  "item_closed",
		Message: "Bahan ini sudah diterima atau dibatalkan.",
	}
)
