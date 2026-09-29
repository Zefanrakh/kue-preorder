// Package postgres implements procurement.Repository on the sqlc queries.
// Every query runs through db.Conn, so it joins the caller's transaction when
// there is one (platform/db.Tx).
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/audit"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
	"github.com/Zefanrakh/kue-preorder/internal/procurement"
)

// Repository implements procurement.Repository.
type Repository struct {
	db *db.DB
}

var _ procurement.Repository = (*Repository)(nil)

// NewRepository returns a repository over d.
func NewRepository(d *db.DB) *Repository {
	return &Repository{db: d}
}

func (r *Repository) q(ctx context.Context) *Queries {
	return New(r.db.Conn(ctx))
}

// InsertOrder implements procurement.Repository.
func (r *Repository) InsertOrder(ctx context.Context, tenantID uuid.UUID, o procurement.Order, at time.Time) error {
	q := r.q(ctx)
	if err := q.InsertOrder(ctx, InsertOrderParams{
		ID: o.ID, TenantID: tenantID, BatchID: o.BatchID, BatchDate: db.Date(o.BatchDate), SupplierID: o.SupplierID,
		AdapterKey: o.Adapter, ExternalRef: optional(o.ExternalRef), Note: optional(o.Note), CreatedBy: o.CreatedBy, Now: at,
	}); err != nil {
		return err
	}
	for _, it := range o.Items {
		p := InsertItemParams{ID: it.ID, TenantID: tenantID, OrderID: o.ID, IngredientID: it.IngredientID, Qty: it.Qty}
		if it.Pack != nil {
			count, size, unit := it.Pack.Count, it.Pack.Size, it.Pack.Unit
			p.Packs, p.PackSize, p.PackUnit, p.PackPriceIdr = &count, &size, &unit, it.Pack.PriceIDR
		}
		if err := q.InsertItem(ctx, p); err != nil {
			return fmt.Errorf("insert item %s of order %s: %w", it.IngredientID, o.ID, err)
		}
	}
	return nil
}

// LockOrder implements procurement.Repository.
func (r *Repository) LockOrder(ctx context.Context, tenantID, id uuid.UUID) (procurement.Order, error) {
	row, err := r.q(ctx).LockOrder(ctx, LockOrderParams{TenantID: tenantID, ID: id})
	return r.one(ctx, tenantID, row, err)
}

// GetOrder implements procurement.Repository.
func (r *Repository) GetOrder(ctx context.Context, tenantID, id uuid.UUID) (procurement.Order, error) {
	row, err := r.q(ctx).GetOrder(ctx, GetOrderParams{TenantID: tenantID, ID: id})
	return r.one(ctx, tenantID, row, err)
}

func (r *Repository) one(ctx context.Context, tenantID uuid.UUID, row ProcurementOrder, err error) (procurement.Order, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return procurement.Order{}, apperr.ErrNotFound
	}
	if err != nil {
		return procurement.Order{}, err
	}
	orders, err := r.withItems(ctx, tenantID, []ProcurementOrder{row})
	if err != nil {
		return procurement.Order{}, err
	}
	return orders[0], nil
}

// ListOrders implements procurement.Repository.
func (r *Repository) ListOrders(ctx context.Context, tenantID uuid.UUID, date clock.Date) ([]procurement.Order, error) {
	rows, err := r.q(ctx).ListOrders(ctx, ListOrdersParams{TenantID: tenantID, BatchDate: db.Date(date)})
	if err != nil {
		return nil, err
	}
	return r.withItems(ctx, tenantID, rows)
}

func (r *Repository) withItems(ctx context.Context, tenantID uuid.UUID, rows []ProcurementOrder) ([]procurement.Order, error) {
	out := make([]procurement.Order, len(rows))
	index := make(map[uuid.UUID]int, len(rows))
	ids := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		out[i] = procurement.Order{
			ID: row.ID, BatchID: row.BatchID, BatchDate: db.FromDate(row.BatchDate), SupplierID: row.SupplierID,
			Adapter: row.AdapterKey, ExternalRef: deref(row.ExternalRef), Status: procurement.OrderStatus(row.Status),
			Note: deref(row.Note), CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt,
		}
		index[row.ID], ids[i] = i, row.ID
	}
	if len(ids) == 0 {
		return out, nil
	}
	items, err := r.q(ctx).OrderItems(ctx, OrderItemsParams{TenantID: tenantID, OrderIds: ids})
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		item := procurement.Item{
			ID: it.ID, IngredientID: it.IngredientID, Qty: it.Qty, Status: procurement.ItemStatus(it.Status),
			QtyReceived: it.QtyReceived, ReceivedAt: it.ReceivedAt,
		}
		if it.Packs != nil && it.PackSize != nil {
			item.Pack = &procurement.Pack{Count: *it.Packs, Size: *it.PackSize, Unit: deref(it.PackUnit), PriceIDR: it.PackPriceIdr}
		}
		o := &out[index[it.OrderID]]
		o.Items = append(o.Items, item)
	}
	return out, nil
}

// SetItemReceived implements procurement.Repository.
func (r *Repository) SetItemReceived(ctx context.Context, tenantID, itemID uuid.UUID, qty int64, at time.Time) (bool, error) {
	n, err := r.q(ctx).SetItemReceived(ctx, SetItemReceivedParams{QtyReceived: &qty, ReceivedAt: &at, TenantID: tenantID, ID: itemID})
	return n > 0, err
}

// SetItemCancelled implements procurement.Repository.
func (r *Repository) SetItemCancelled(ctx context.Context, tenantID, itemID uuid.UUID) (bool, error) {
	n, err := r.q(ctx).SetItemCancelled(ctx, SetItemCancelledParams{TenantID: tenantID, ID: itemID})
	return n > 0, err
}

// SetOrderStatus implements procurement.Repository.
func (r *Repository) SetOrderStatus(ctx context.Context, tenantID, id uuid.UUID, s procurement.OrderStatus, at time.Time) error {
	return r.q(ctx).SetOrderStatus(ctx, SetOrderStatusParams{Status: string(s), Now: at, TenantID: tenantID, ID: id})
}

// Audit implements procurement.Repository.
func (r *Repository) Audit(ctx context.Context, e audit.Entry) error {
	return audit.Record(ctx, r.db.Conn(ctx), e)
}

// Publish implements procurement.Repository.
func (r *Repository) Publish(ctx context.Context, e outbox.Event) error {
	return outbox.Append(ctx, r.db.Conn(ctx), e)
}

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
