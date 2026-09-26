// Package postgres implements orders.Repository on the sqlc queries. Every
// query runs through db.Conn, so it joins the caller's transaction when
// there is one (platform/db.Tx).
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Zefanrakh/kue-preorder/internal/orders"
	"github.com/Zefanrakh/kue-preorder/internal/payments"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
)

// Repository implements orders.Repository.
type Repository struct {
	db *db.DB
}

var _ orders.Repository = (*Repository)(nil)

// NewRepository returns a repository over d.
func NewRepository(d *db.DB) *Repository {
	return &Repository{db: d}
}

func (r *Repository) q(ctx context.Context) *Queries {
	return New(r.db.Conn(ctx))
}

func statusNames(statuses []orders.Status) []string {
	names := make([]string, len(statuses))
	for i, s := range statuses {
		names[i] = string(s)
	}
	return names
}

// BatchCutoffs implements orders.Repository.
func (r *Repository) BatchCutoffs(ctx context.Context, tenantID uuid.UUID, from, to clock.Date, statuses []orders.Status) (map[clock.Date]time.Time, error) {
	rows, err := r.q(ctx).BatchCutoffs(ctx, BatchCutoffsParams{TenantID: tenantID, FromDate: db.Date(from), ToDate: db.Date(to), Statuses: statusNames(statuses)})
	if err != nil {
		return nil, err
	}
	out := make(map[clock.Date]time.Time, len(rows))
	for _, row := range rows {
		out[db.FromDate(row.ProductionDate)] = row.Cutoff
	}
	return out, nil
}

// LockCustomer implements orders.Repository.
func (r *Repository) LockCustomer(ctx context.Context, customerID uuid.UUID) error {
	return r.q(ctx).LockCustomerOrders(ctx, customerID.String())
}

// FindByIdempotencyKey implements orders.Repository.
func (r *Repository) FindByIdempotencyKey(ctx context.Context, tenantID, customerID, key uuid.UUID) (uuid.UUID, error) {
	id, err := r.q(ctx).FindOrderByIdempotencyKey(ctx, FindOrderByIdempotencyKeyParams{TenantID: tenantID, CustomerID: customerID, IdempotencyKey: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, apperr.ErrNotFound
	}
	return id, err
}

// CountByStatus implements orders.Repository.
func (r *Repository) CountByStatus(ctx context.Context, tenantID, customerID uuid.UUID, status orders.Status) (int, error) {
	n, err := r.q(ctx).CountCustomerOrders(ctx, CountCustomerOrdersParams{TenantID: tenantID, CustomerID: customerID, Status: string(status)})
	return int(n), err
}

// Insert implements orders.Repository.
func (r *Repository) Insert(ctx context.Context, tenantID uuid.UUID, o orders.Order, idempotencyKey uuid.UUID, at time.Time) (bool, error) {
	var email *string
	if o.CustomerEmail != "" {
		email = &o.CustomerEmail
	}
	q := r.q(ctx)
	_, err := q.InsertOrder(ctx, InsertOrderParams{
		ID: o.ID, TenantID: tenantID, CustomerID: o.CustomerID, ChannelKey: "web", Code: o.Code,
		Status: string(o.Status), PaymentStatus: string(o.Payment), FulfillmentType: string(o.Fulfillment),
		PickupAt: o.PickupAt, ProductionStartAt: o.ProductionStart, ProductionDate: db.Date(o.ProductionDate),
		ShoppingCutoffAt: o.ShoppingCutoffAt, DpDueAt: o.DPDueAt, BalanceDueAt: o.BalanceDueAt,
		SubtotalIdr: o.SubtotalIDR, TaxIdr: o.TaxIDR, ShippingIdr: o.ShippingIDR, TotalIdr: o.TotalIDR,
		DpRequiredIdr: o.DPRequiredIDR, FullPaymentRequired: o.FullPaymentRequired, TermsVersion: o.TermsVersion,
		IdempotencyKey: idempotencyKey, Notes: o.Notes, CustomerName: o.CustomerName, CustomerPhone: o.CustomerPhone,
		CustomerEmail: email, Now: at,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // the code is taken
	}
	if err != nil {
		return false, err
	}
	for _, it := range o.Items {
		if err := q.InsertOrderItem(ctx, InsertOrderItemParams{
			TenantID: tenantID, OrderID: o.ID, VariantID: it.VariantID, ProductName: it.ProductName, VariantName: it.VariantName,
			Quantity: it.Quantity, UnitPriceIdr: it.UnitPriceIDR, ProductionMinutes: it.ProductionMinutes, MinNoticeHours: it.MinNoticeHours,
		}); err != nil {
			return false, fmt.Errorf("insert item %s of order %s: %w", it.VariantID, o.Code, err)
		}
	}
	return true, outbox.Append(ctx, r.db.Conn(ctx), outbox.Event{
		TenantID: tenantID, Aggregate: "order", Type: "order.placed", At: at,
		Payload: map[string]any{"order_id": o.ID, "code": o.Code, "production_date": o.ProductionDate.String()},
	})
}

// Get implements orders.Repository.
func (r *Repository) Get(ctx context.Context, tenantID, id uuid.UUID) (orders.Order, error) {
	row, err := r.q(ctx).GetOrder(ctx, GetOrderParams{TenantID: tenantID, ID: id})
	return r.withItems(ctx, tenantID, row, err)
}

// FindCustomerOrder implements orders.Repository.
func (r *Repository) FindCustomerOrder(ctx context.Context, tenantID, customerID uuid.UUID, code string) (orders.Order, error) {
	row, err := r.q(ctx).GetCustomerOrderByCode(ctx, GetCustomerOrderByCodeParams{TenantID: tenantID, CustomerID: customerID, Code: code})
	return r.withItems(ctx, tenantID, row, err)
}

func (r *Repository) withItems(ctx context.Context, tenantID uuid.UUID, row Order, err error) (orders.Order, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return orders.Order{}, apperr.ErrNotFound
	}
	if err != nil {
		return orders.Order{}, err
	}
	items, err := r.q(ctx).OrderItems(ctx, OrderItemsParams{TenantID: tenantID, OrderID: row.ID})
	if err != nil {
		return orders.Order{}, err
	}
	o := orders.Order{
		ID: row.ID, CustomerID: row.CustomerID, Code: row.Code, Status: orders.Status(row.Status),
		Payment: payments.Status(row.PaymentStatus), Fulfillment: orders.Fulfillment(row.FulfillmentType),
		SubtotalIDR: row.SubtotalIdr, TaxIDR: row.TaxIdr, ShippingIDR: row.ShippingIdr, TotalIDR: row.TotalIdr,
		DPRequiredIDR: row.DpRequiredIdr, FullPaymentRequired: row.FullPaymentRequired,
		PickupAt: row.PickupAt, ProductionStart: row.ProductionStartAt, ProductionDate: db.FromDate(row.ProductionDate),
		ShoppingCutoffAt: row.ShoppingCutoffAt, DPDueAt: row.DpDueAt, BalanceDueAt: row.BalanceDueAt,
		Notes: row.Notes, CustomerName: row.CustomerName, CustomerPhone: row.CustomerPhone,
		TermsVersion: row.TermsVersion, TermsAcceptedAt: row.TermsAcceptedAt, CreatedAt: row.CreatedAt,
	}
	if row.CustomerEmail != nil {
		o.CustomerEmail = *row.CustomerEmail
	}
	for _, it := range items {
		o.Items = append(o.Items, orders.QuotedItem{
			VariantID: it.VariantID, ProductName: it.ProductName, VariantName: it.VariantName, Quantity: it.Quantity,
			UnitPriceIDR: it.UnitPriceIdr, LineTotalIDR: int64(it.Quantity) * it.UnitPriceIdr,
			ProductionMinutes: it.ProductionMinutes, MinNoticeHours: it.MinNoticeHours,
		})
	}
	return o, nil
}

// ListCustomerOrders implements orders.Repository.
func (r *Repository) ListCustomerOrders(ctx context.Context, tenantID, customerID uuid.UUID, limit int32) ([]orders.Summary, error) {
	rows, err := r.q(ctx).ListCustomerOrders(ctx, ListCustomerOrdersParams{TenantID: tenantID, CustomerID: customerID, MaxRows: limit})
	if err != nil {
		return nil, err
	}
	out := make([]orders.Summary, len(rows))
	for i, row := range rows {
		out[i] = orders.Summary{
			ID: row.ID, Code: row.Code, Status: orders.Status(row.Status), Payment: payments.Status(row.PaymentStatus),
			PickupAt: row.PickupAt, TotalIDR: row.TotalIdr, FirstItem: row.FirstItem, ItemCount: row.ItemCount, CreatedAt: row.CreatedAt,
		}
	}
	return out, nil
}

// ActiveOrderDays implements orders.Repository.
func (r *Repository) ActiveOrderDays(ctx context.Context, tenantID uuid.UUID, from, to clock.Date, statuses []orders.Status) (map[clock.Date]int, error) {
	rows, err := r.q(ctx).ActiveOrderDays(ctx, ActiveOrderDaysParams{TenantID: tenantID, FromDate: db.Date(from), ToDate: db.Date(to), Statuses: statusNames(statuses)})
	if err != nil {
		return nil, err
	}
	out := map[clock.Date]int{}
	for _, row := range rows {
		days := []clock.Date{db.FromDate(row.ProductionDate)}
		if pickup := db.FromDate(row.PickupDate); pickup != days[0] {
			days = append(days, pickup) // an order counts once on each of its days
		}
		for _, d := range days {
			if !d.Before(from) && !d.After(to) {
				out[d]++
			}
		}
	}
	return out, nil
}
