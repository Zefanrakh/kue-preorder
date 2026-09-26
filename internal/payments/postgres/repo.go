// Package postgres implements payments.Repository on the sqlc queries.
package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Zefanrakh/kue-preorder/internal/payments"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
)

// Repository implements payments.Repository.
type Repository struct {
	q *Queries
}

var _ payments.Repository = (*Repository)(nil)

// NewRepository returns a repository over d.
func NewRepository(d *db.DB) *Repository {
	return &Repository{q: New(d.Pool())}
}

// Policy implements payments.Repository.
func (r *Repository) Policy(ctx context.Context, tenantID uuid.UUID) (payments.Policy, error) {
	row, err := r.q.GetPaymentPolicy(ctx, tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return payments.Policy{}, apperr.ErrNotFound
	}
	if err != nil {
		return payments.Policy{}, err
	}
	return payments.Policy{
		DPMinPercent: row.DpMinPercent, DPCoversIngredientCost: row.DpCoversIngredientCost,
		BalanceDueHoursBefore: row.BalanceDueHoursBefore, DPInvoiceValidMinutes: row.DpInvoiceValidMinutes,
		UpdatedAt: row.UpdatedAt,
	}, nil
}
