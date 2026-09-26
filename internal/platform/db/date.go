package db

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

// Date converts a Jakarta calendar day to a Postgres date. A date column has
// no zone; pgx reads and writes it at UTC midnight.
func Date(d clock.Date) pgtype.Date {
	return pgtype.Date{Time: time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC), Valid: true}
}

// FromDate converts a Postgres date to a Jakarta calendar day.
func FromDate(d pgtype.Date) clock.Date {
	y, m, day := d.Time.Date()
	return clock.Date{Year: y, Month: m, Day: day}
}
