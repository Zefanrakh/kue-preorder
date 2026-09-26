// Package postgres implements orders.Repository on the sqlc queries.
package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/orders"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
)

// Repository implements orders.Repository.
type Repository struct {
	q *Queries
}

var _ orders.Repository = (*Repository)(nil)

// NewRepository returns a repository over d.
func NewRepository(d *db.DB) *Repository {
	return &Repository{q: New(d.Pool())}
}

// BatchCutoffs implements orders.Repository.
func (r *Repository) BatchCutoffs(ctx context.Context, tenantID uuid.UUID, from, to clock.Date, statuses []orders.Status) (map[clock.Date]time.Time, error) {
	names := make([]string, len(statuses))
	for i, s := range statuses {
		names[i] = string(s)
	}
	rows, err := r.q.BatchCutoffs(ctx, BatchCutoffsParams{TenantID: tenantID, FromDate: db.Date(from), ToDate: db.Date(to), Statuses: names})
	if err != nil {
		return nil, err
	}
	out := make(map[clock.Date]time.Time, len(rows))
	for _, row := range rows {
		out[db.FromDate(row.ProductionDate)] = row.Cutoff
	}
	return out, nil
}
