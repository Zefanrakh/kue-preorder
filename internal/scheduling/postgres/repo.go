// Package postgres implements scheduling.Repository on the sqlc queries.
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/scheduling"
)

// Repository implements scheduling.Repository.
type Repository struct {
	q *Queries
}

var _ scheduling.Repository = (*Repository)(nil)

// NewRepository returns a repository over d.
func NewRepository(d *db.DB) *Repository {
	return &Repository{q: New(d.Pool())}
}

// Settings implements scheduling.Repository.
func (r *Repository) Settings(ctx context.Context, tenantID uuid.UUID) (scheduling.Settings, error) {
	row, err := r.q.GetScheduleSettings(ctx, tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return scheduling.Settings{}, scheduling.ErrNotFound
	}
	if err != nil {
		return scheduling.Settings{}, err
	}
	return toSettings(row), nil
}

// SaveSettings implements scheduling.Repository.
func (r *Repository) SaveSettings(ctx context.Context, tenantID uuid.UUID, s scheduling.Settings, at time.Time) (scheduling.Settings, error) {
	row, err := r.q.UpsertScheduleSettings(ctx, UpsertScheduleSettingsParams{
		TenantID: tenantID, ShoppingBufferHours: s.ShoppingBufferHours, DailyCapacityMinutes: s.DailyCapacityMinutes,
		PickupWindowStart: fromTimeOfDay(s.PickupStart), PickupWindowEnd: fromTimeOfDay(s.PickupEnd), Now: at,
	})
	if err != nil {
		return scheduling.Settings{}, err
	}
	return toSettings(row), nil
}

// ClosedDates implements scheduling.Repository.
func (r *Repository) ClosedDates(ctx context.Context, tenantID uuid.UUID, from, to clock.Date) ([]scheduling.ClosedDate, error) {
	rows, err := r.q.ListClosedDates(ctx, ListClosedDatesParams{TenantID: tenantID, FromDate: fromDate(from), ToDate: fromDate(to)})
	if err != nil {
		return nil, err
	}
	out := make([]scheduling.ClosedDate, len(rows))
	for i, row := range rows {
		out[i] = toClosedDate(row)
	}
	return out, nil
}

// AddClosedDate implements scheduling.Repository.
func (r *Repository) AddClosedDate(ctx context.Context, tenantID uuid.UUID, d clock.Date, reason string, at time.Time) (scheduling.ClosedDate, error) {
	row, err := r.q.AddClosedDate(ctx, AddClosedDateParams{TenantID: tenantID, Date: fromDate(d), Reason: reason, Now: at})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation on (tenant_id, date)
		return scheduling.ClosedDate{}, &scheduling.ValidationError{Fields: map[string]string{"date": "Tanggal ini sudah diliburkan."}, Conflict: true}
	}
	if err != nil {
		return scheduling.ClosedDate{}, err
	}
	return toClosedDate(row), nil
}

// RemoveClosedDate implements scheduling.Repository.
func (r *Repository) RemoveClosedDate(ctx context.Context, tenantID uuid.UUID, d clock.Date) error {
	n, err := r.q.RemoveClosedDate(ctx, RemoveClosedDateParams{TenantID: tenantID, Date: fromDate(d)})
	if err != nil {
		return err
	}
	if n == 0 {
		return scheduling.ErrNotFound
	}
	return nil
}

func toSettings(row ScheduleSetting) scheduling.Settings {
	return scheduling.Settings{
		ShoppingBufferHours: row.ShoppingBufferHours, DailyCapacityMinutes: row.DailyCapacityMinutes,
		PickupStart: toTimeOfDay(row.PickupWindowStart), PickupEnd: toTimeOfDay(row.PickupWindowEnd), UpdatedAt: row.UpdatedAt,
	}
}

func toClosedDate(row ClosedDate) scheduling.ClosedDate {
	return scheduling.ClosedDate{Date: toDate(row.Date), Reason: row.Reason, CreatedAt: row.CreatedAt}
}

const microsPerMinute = int64(time.Minute / time.Microsecond)

func toTimeOfDay(t pgtype.Time) scheduling.TimeOfDay {
	return scheduling.TimeOfDay(t.Microseconds / microsPerMinute)
}

func fromTimeOfDay(t scheduling.TimeOfDay) pgtype.Time {
	return pgtype.Time{Microseconds: int64(t) * microsPerMinute, Valid: true}
}

// A Postgres date has no zone; pgx reads and writes it at UTC midnight.
func toDate(d pgtype.Date) clock.Date {
	y, m, day := d.Time.Date()
	return clock.Date{Year: y, Month: m, Day: day}
}

func fromDate(d clock.Date) pgtype.Date {
	return pgtype.Date{Time: time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC), Valid: true}
}
