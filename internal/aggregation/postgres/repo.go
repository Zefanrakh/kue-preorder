// Package postgres implements aggregation.Repository on the sqlc queries.
// Every query runs through db.Conn, so it joins the caller's transaction when
// there is one (platform/db.Tx).
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Zefanrakh/kue-preorder/internal/aggregation"
	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/audit"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
)

// Repository implements aggregation.Repository.
type Repository struct {
	db *db.DB
}

var _ aggregation.Repository = (*Repository)(nil)

// NewRepository returns a repository over d.
func NewRepository(d *db.DB) *Repository {
	return &Repository{db: d}
}

func (r *Repository) q(ctx context.Context) *Queries {
	return New(r.db.Conn(ctx))
}

// EnsureBatch implements aggregation.Repository.
func (r *Repository) EnsureBatch(ctx context.Context, tenantID uuid.UUID, date clock.Date, at time.Time) error {
	return r.q(ctx).EnsureBatch(ctx, EnsureBatchParams{TenantID: tenantID, BatchDate: db.Date(date), Now: at})
}

// LockBatch implements aggregation.Repository.
func (r *Repository) LockBatch(ctx context.Context, tenantID uuid.UUID, date clock.Date) (aggregation.Batch, error) {
	row, err := r.q(ctx).LockBatch(ctx, LockBatchParams{TenantID: tenantID, BatchDate: db.Date(date)})
	return toBatch(row, err)
}

// GetBatch implements aggregation.Repository.
func (r *Repository) GetBatch(ctx context.Context, tenantID uuid.UUID, date clock.Date) (aggregation.Batch, error) {
	row, err := r.q(ctx).GetBatch(ctx, GetBatchParams{TenantID: tenantID, BatchDate: db.Date(date)})
	return toBatch(row, err)
}

func toBatch(row ProductionBatch, err error) (aggregation.Batch, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return aggregation.Batch{}, apperr.ErrNotFound
	}
	if err != nil {
		return aggregation.Batch{}, err
	}
	return aggregation.Batch{
		ID: row.ID, Date: db.FromDate(row.BatchDate), Status: aggregation.BatchStatus(row.Status),
		ComputedAt: row.ComputedAt, Error: deref(row.Error), UpdatedAt: row.UpdatedAt,
	}, nil
}

// ListBatches implements aggregation.Repository.
func (r *Repository) ListBatches(ctx context.Context, tenantID uuid.UUID, from, to clock.Date) ([]aggregation.Summary, error) {
	rows, err := r.q(ctx).ListBatches(ctx, ListBatchesParams{TenantID: tenantID, FromDate: db.Date(from), ToDate: db.Date(to)})
	if err != nil {
		return nil, err
	}
	out := make([]aggregation.Summary, len(rows))
	for i, row := range rows {
		out[i] = aggregation.Summary{
			Batch: aggregation.Batch{
				ID: row.ID, Date: db.FromDate(row.BatchDate), Status: aggregation.BatchStatus(row.Status),
				ComputedAt: row.ComputedAt, Error: deref(row.Error),
			},
			LinesToBuy: row.ItemsToBuy, CostIDR: row.CostIdr,
		}
	}
	return out, nil
}

// UpcomingDates implements aggregation.Repository.
func (r *Repository) UpcomingDates(ctx context.Context, tenantID uuid.UUID, from clock.Date) ([]clock.Date, error) {
	rows, err := r.q(ctx).UpcomingBatchDates(ctx, UpcomingBatchDatesParams{TenantID: tenantID, FromDate: db.Date(from)})
	if err != nil {
		return nil, err
	}
	out := make([]clock.Date, len(rows))
	for i, d := range rows {
		out[i] = db.FromDate(d)
	}
	return out, nil
}

// ClaimedBefore implements aggregation.Repository.
func (r *Repository) ClaimedBefore(ctx context.Context, tenantID uuid.UUID, date clock.Date, ingredientIDs []uuid.UUID) (map[uuid.UUID]int64, error) {
	rows, err := r.q(ctx).ClaimedBefore(ctx, ClaimedBeforeParams{TenantID: tenantID, BatchDate: db.Date(date), IngredientIds: ingredientIDs})
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]int64, len(rows))
	for _, row := range rows {
		out[row.IngredientID] = row.Claimed
	}
	return out, nil
}

// Components implements aggregation.Repository.
func (r *Repository) Components(ctx context.Context, tenantID, batchID uuid.UUID) ([]aggregation.ComponentTotal, error) {
	rows, err := r.q(ctx).BatchComponentTotals(ctx, BatchComponentTotalsParams{TenantID: tenantID, BatchID: batchID})
	if err != nil {
		return nil, err
	}
	out := make([]aggregation.ComponentTotal, len(rows))
	for i, row := range rows {
		out[i] = aggregation.ComponentTotal{ComponentID: row.ComponentID, Units: row.Units}
	}
	return out, nil
}

// Lines implements aggregation.Repository.
func (r *Repository) Lines(ctx context.Context, tenantID, batchID uuid.UUID) ([]aggregation.Line, error) {
	rows, err := r.q(ctx).BatchRequirements(ctx, BatchRequirementsParams{TenantID: tenantID, BatchID: batchID})
	if err != nil {
		return nil, err
	}
	out := make([]aggregation.Line, len(rows))
	for i, row := range rows {
		l := aggregation.Line{
			IngredientID: row.IngredientID, Needed: row.QtyNeeded, UsableStock: row.QtyUsableStock,
			Ordered: row.QtyOrdered, ToBuy: row.QtyToBuy, Status: aggregation.LineStatus(row.Status),
		}
		if row.SupplierID != nil && row.PackSize != nil {
			l.Pack = &catalog.DefaultPack{
				IngredientID: row.IngredientID, SupplierID: *row.SupplierID, Size: *row.PackSize,
				Unit: deref(row.PackUnit), PriceIDR: row.PackPriceIdr,
			}
			if row.PacksToBuy != nil {
				l.Packs = *row.PacksToBuy
			}
		}
		out[i] = l
	}
	return out, nil
}

// SaveResult implements aggregation.Repository.
func (r *Repository) SaveResult(ctx context.Context, tenantID, batchID uuid.UUID, components []aggregation.ComponentTotal, lines []aggregation.Line, at time.Time) error {
	q := r.q(ctx)
	if err := q.DeleteComponentTotals(ctx, DeleteComponentTotalsParams{TenantID: tenantID, BatchID: batchID}); err != nil {
		return err
	}
	for _, c := range components {
		if err := q.InsertComponentTotal(ctx, InsertComponentTotalParams{
			TenantID: tenantID, BatchID: batchID, ComponentID: c.ComponentID, Units: c.Units,
		}); err != nil {
			return err
		}
	}
	keep := make([]uuid.UUID, len(lines))
	for i, l := range lines {
		keep[i] = l.IngredientID
		p := UpsertRequirementParams{
			TenantID: tenantID, BatchID: batchID, IngredientID: l.IngredientID, QtyNeeded: l.Needed,
			QtyUsableStock: l.UsableStock, QtyToBuy: l.ToBuy, Now: at,
		}
		if l.Pack != nil {
			packs, unit := l.Packs, l.Pack.Unit
			p.SupplierID, p.PackSize, p.PackUnit, p.PackPriceIdr, p.PacksToBuy = &l.Pack.SupplierID, &l.Pack.Size, &unit, l.Pack.PriceIDR, &packs
		}
		if err := q.UpsertRequirement(ctx, p); err != nil {
			return err
		}
	}
	if err := q.DeleteStaleRequirements(ctx, DeleteStaleRequirementsParams{TenantID: tenantID, BatchID: batchID, Keep: keep}); err != nil {
		return err
	}
	return q.MarkBatchComputed(ctx, MarkBatchComputedParams{Now: &at, TenantID: tenantID, ID: batchID})
}

// MarkFailed implements aggregation.Repository.
func (r *Repository) MarkFailed(ctx context.Context, tenantID uuid.UUID, date clock.Date, reason string, at time.Time) error {
	return r.q(ctx).MarkBatchFailed(ctx, MarkBatchFailedParams{TenantID: tenantID, BatchDate: db.Date(date), Error: &reason, Now: at})
}

// SetStatus implements aggregation.Repository.
func (r *Repository) SetStatus(ctx context.Context, tenantID uuid.UUID, date clock.Date, from []aggregation.BatchStatus, to aggregation.BatchStatus, at time.Time) (bool, error) {
	names := make([]string, len(from))
	for i, s := range from {
		names[i] = string(s)
	}
	n, err := r.q(ctx).SetBatchStatus(ctx, SetBatchStatusParams{
		Status: string(to), Now: at, TenantID: tenantID, BatchDate: db.Date(date), FromStatuses: names,
	})
	return n > 0, err
}

// OpenDates implements aggregation.Repository.
func (r *Repository) OpenDates(ctx context.Context, tenantID uuid.UUID) ([]clock.Date, error) {
	rows, err := r.q(ctx).OpenBatchDates(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := make([]clock.Date, len(rows))
	for i, d := range rows {
		out[i] = db.FromDate(d)
	}
	return out, nil
}

// Audit implements aggregation.Repository.
func (r *Repository) Audit(ctx context.Context, e audit.Entry) error {
	return audit.Record(ctx, r.db.Conn(ctx), e)
}

// Publish implements aggregation.Repository.
func (r *Repository) Publish(ctx context.Context, e outbox.Event) error {
	return outbox.Append(ctx, r.db.Conn(ctx), e)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
