// Package postgres implements inventory.Repository on the sqlc queries.
// Every query runs through db.Conn, so it joins the caller's transaction when
// there is one (platform/db.Tx).
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Zefanrakh/kue-preorder/internal/inventory"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/audit"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
)

// Repository implements inventory.Repository.
type Repository struct {
	db *db.DB
}

var _ inventory.Repository = (*Repository)(nil)

// NewRepository returns a repository over d.
func NewRepository(d *db.DB) *Repository {
	return &Repository{db: d}
}

func (r *Repository) q(ctx context.Context) *Queries {
	return New(r.db.Conn(ctx))
}

// LockIngredient implements inventory.Repository.
func (r *Repository) LockIngredient(ctx context.Context, ingredientID uuid.UUID) error {
	return r.q(ctx).LockIngredientStock(ctx, ingredientID.String())
}

// InsertLot implements inventory.Repository.
func (r *Repository) InsertLot(ctx context.Context, tenantID uuid.UUID, l inventory.Lot, createdBy uuid.UUID, at time.Time) error {
	return r.q(ctx).InsertLot(ctx, InsertLotParams{
		ID: l.ID, TenantID: tenantID, IngredientID: l.IngredientID, ReceivedAt: l.ReceivedAt, ExpiresAt: l.ExpiresAt,
		Source: string(l.Source), Note: optional(l.Note), CreatedBy: createdBy, Now: at,
	})
}

// InsertMovement implements inventory.Repository.
func (r *Repository) InsertMovement(ctx context.Context, tenantID uuid.UUID, m inventory.Movement) error {
	return r.q(ctx).InsertMovement(ctx, InsertMovementParams{
		TenantID: tenantID, LotID: m.LotID, IngredientID: m.IngredientID, Kind: string(m.Kind), Qty: m.Qty,
		BatchID: m.BatchID, Reason: optional(m.Reason), ActorID: m.ActorID, Now: m.At,
	})
}

// InsertCheck implements inventory.Repository.
func (r *Repository) InsertCheck(ctx context.Context, tenantID uuid.UUID, c inventory.Check) error {
	p := InsertCheckParams{TenantID: tenantID, LotID: c.LotID, Result: "ok", Note: optional(c.Note), CheckedBy: c.CheckedBy, Now: c.At}
	if !c.OK {
		reason := string(c.Reason)
		p.Result, p.Reason = "discard", &reason
	}
	return r.q(ctx).InsertCheck(ctx, p)
}

// SetLotStatus implements inventory.Repository.
func (r *Repository) SetLotStatus(ctx context.Context, tenantID, lotID uuid.UUID, s inventory.LotStatus, at time.Time) error {
	return r.q(ctx).SetLotStatus(ctx, SetLotStatusParams{Status: string(s), Now: at, TenantID: tenantID, ID: lotID})
}

// Lots implements inventory.Repository.
func (r *Repository) Lots(ctx context.Context, tenantID uuid.UUID, ingredientIDs []uuid.UUID, statuses []inventory.LotStatus) ([]inventory.Lot, error) {
	names := make([]string, len(statuses))
	for i, s := range statuses {
		names[i] = string(s)
	}
	if ingredientIDs == nil {
		ingredientIDs = []uuid.UUID{} // NULL would match nothing
	}
	rows, err := r.q(ctx).Lots(ctx, LotsParams{TenantID: tenantID, IngredientIds: ingredientIDs, Statuses: names})
	if err != nil {
		return nil, err
	}
	lots := make([]inventory.Lot, len(rows))
	ids := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		lots[i] = inventory.Lot{
			ID: row.ID, IngredientID: row.IngredientID, ReceivedAt: row.ReceivedAt, ExpiresAt: row.ExpiresAt,
			Status: inventory.LotStatus(row.Status), Source: inventory.Source(row.Source), Note: deref(row.Note),
			Balance: row.Balance, CreatedAt: row.CreatedAt,
		}
		ids[i] = row.ID
	}
	return lots, r.withChecks(ctx, tenantID, lots, ids)
}

// GetLot implements inventory.Repository.
func (r *Repository) GetLot(ctx context.Context, tenantID, lotID uuid.UUID) (inventory.Lot, error) {
	row, err := r.q(ctx).GetLot(ctx, GetLotParams{TenantID: tenantID, ID: lotID})
	if errors.Is(err, pgx.ErrNoRows) {
		return inventory.Lot{}, apperr.ErrNotFound
	}
	if err != nil {
		return inventory.Lot{}, err
	}
	lots := []inventory.Lot{{
		ID: row.ID, IngredientID: row.IngredientID, ReceivedAt: row.ReceivedAt, ExpiresAt: row.ExpiresAt,
		Status: inventory.LotStatus(row.Status), Source: inventory.Source(row.Source), Note: deref(row.Note),
		Balance: row.Balance, CreatedAt: row.CreatedAt,
	}}
	if err := r.withChecks(ctx, tenantID, lots, []uuid.UUID{row.ID}); err != nil {
		return inventory.Lot{}, err
	}
	return lots[0], nil
}

// withChecks fills in each lot's latest ok check.
func (r *Repository) withChecks(ctx context.Context, tenantID uuid.UUID, lots []inventory.Lot, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := r.q(ctx).LastOKChecks(ctx, LastOKChecksParams{TenantID: tenantID, LotIds: ids})
	if err != nil {
		return err
	}
	last := make(map[uuid.UUID]time.Time, len(rows))
	for _, row := range rows {
		last[row.LotID] = row.CheckedAt
	}
	for i := range lots {
		if t, ok := last[lots[i].ID]; ok {
			lots[i].LastOKAt = &t
		}
	}
	return nil
}

// ExpireLots implements inventory.Repository.
func (r *Repository) ExpireLots(ctx context.Context, tenantID uuid.UUID, now time.Time) (int64, error) {
	return r.q(ctx).ExpireLots(ctx, ExpireLotsParams{Now: now, TenantID: tenantID})
}

// Audit implements inventory.Repository.
func (r *Repository) Audit(ctx context.Context, e audit.Entry) error {
	return audit.Record(ctx, r.db.Conn(ctx), e)
}

// Publish implements inventory.Repository.
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
