// Package postgres implements the payments repositories on the sqlc
// queries. Every query runs through db.Conn, so it joins the caller's
// transaction when there is one (platform/db.Tx).
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Zefanrakh/kue-preorder/internal/payments"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
)

// Repository implements payments.Repository and payments.LedgerRepository.
type Repository struct {
	db *db.DB
}

var (
	_ payments.Repository       = (*Repository)(nil)
	_ payments.LedgerRepository = (*Repository)(nil)
)

// NewRepository returns a repository over d.
func NewRepository(d *db.DB) *Repository {
	return &Repository{db: d}
}

func (r *Repository) q(ctx context.Context) *Queries {
	return New(r.db.Conn(ctx))
}

// Policy implements payments.Repository.
func (r *Repository) Policy(ctx context.Context, tenantID uuid.UUID) (payments.Policy, error) {
	row, err := r.q(ctx).GetPaymentPolicy(ctx, tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return payments.Policy{}, apperr.ErrNotFound
	}
	if err != nil {
		return payments.Policy{}, err
	}
	return payments.Policy{
		DPMinPercent: row.DpMinPercent, DPCoversIngredientCost: row.DpCoversIngredientCost,
		BalanceDueHoursBefore: row.BalanceDueHoursBefore, DPInvoiceValidMinutes: row.DpInvoiceValidMinutes,
		DPMinTotalIDR: row.DpMinTotalIdr, MinOrderIDR: row.MinOrderIdr, UpdatedAt: row.UpdatedAt,
	}, nil
}

// FeeRules implements payments.Repository.
func (r *Repository) FeeRules(ctx context.Context, tenantID uuid.UUID) (map[payments.Method]payments.FeeRule, error) {
	rows, err := r.q(ctx).ListPaymentMethodFees(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := make(map[payments.Method]payments.FeeRule, len(rows))
	for _, row := range rows {
		m := payments.Method(row.Method)
		out[m] = payments.FeeRule{Method: m, FixedIDR: row.FixedIdr, RateBPS: row.RateBps, VATIncluded: row.VatIncluded, Enabled: row.Enabled}
	}
	return out, nil
}

// AddPayment implements payments.LedgerRepository.
func (r *Repository) AddPayment(ctx context.Context, tenantID uuid.UUID, p payments.Payment, at time.Time) error {
	var method *string
	if p.Method != "" {
		m := string(p.Method)
		method = &m
	}
	var raw []byte
	if p.Manual != nil {
		b, err := json.Marshal(p.Manual)
		if err != nil {
			return fmt.Errorf("encode proof of payment %s: %w", p.ID, err)
		}
		raw = b
	}
	return r.q(ctx).AddPayment(ctx, AddPaymentParams{
		ID: p.ID, TenantID: tenantID, OrderID: p.OrderID, Kind: string(p.Kind), Provider: p.Provider, Method: method,
		AmountIdr: p.AmountIDR, FeeIdr: p.FeeIDR, Status: string(p.State), ExpiresAt: p.ExpiresAt, PaidAt: p.PaidAt,
		Raw: raw, Now: at,
	})
}

// ExpirePending implements payments.LedgerRepository.
func (r *Repository) ExpirePending(ctx context.Context, tenantID, orderID uuid.UUID, at time.Time) (int64, error) {
	return r.q(ctx).ExpirePendingPayments(ctx, ExpirePendingPaymentsParams{Now: at, TenantID: tenantID, OrderID: orderID})
}

// SetInvoice implements payments.LedgerRepository.
func (r *Repository) SetInvoice(ctx context.Context, tenantID, paymentID uuid.UUID, inv payments.Invoice, at time.Time) error {
	n, err := r.q(ctx).SetPaymentInvoice(ctx, SetPaymentInvoiceParams{
		ExternalID: &inv.ExternalID, CheckoutUrl: &inv.URL, Now: at, TenantID: tenantID, ID: paymentID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return apperr.ErrNotFound
	}
	return nil
}

// OrderPayments implements payments.LedgerRepository.
func (r *Repository) OrderPayments(ctx context.Context, tenantID, orderID uuid.UUID) ([]payments.Payment, error) {
	rows, err := r.q(ctx).OrderPayments(ctx, OrderPaymentsParams{TenantID: tenantID, OrderID: orderID})
	if err != nil {
		return nil, err
	}
	out := make([]payments.Payment, len(rows))
	for i, row := range rows {
		out[i] = payments.Payment{
			ID: row.ID, OrderID: row.OrderID, Kind: payments.Kind(row.Kind), Provider: row.Provider, Method: payments.Method(deref(row.Method)),
			ExternalID: deref(row.ExternalID), AmountIDR: row.AmountIdr, FeeIDR: row.FeeIdr,
			State: payments.State(row.Status), CheckoutURL: deref(row.CheckoutUrl),
			ExpiresAt: row.ExpiresAt, PaidAt: row.PaidAt, CreatedAt: row.CreatedAt,
		}
		// Only manual rows keep a proof in raw; a provider's raw is its own.
		if row.Provider == payments.ProviderManual && row.Raw != nil {
			var proof payments.ManualProof
			if err := json.Unmarshal(row.Raw, &proof); err != nil {
				return nil, fmt.Errorf("decode proof of payment %s: %w", row.ID, err)
			}
			out[i].Manual = &proof
		}
	}
	return out, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
