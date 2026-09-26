// Package scheduling decides when an order can be picked up and when the
// kitchen must shop, produce, and be paid (docs/architecture.md §9.5, §15).
// Every rule works on Asia/Jakarta wall-clock time through platform/clock,
// whatever the server's time zone.
//
// Plan is pure: checkout (M2.3) gives it the settings, the closed dates, and
// what the production date already holds, and gets back the locked times of
// the order or the reason the pickup time is refused.
package scheduling

import (
	"fmt"
	"time"

	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

var (
	// ErrNotFound means the record does not exist in the caller's tenant.
	ErrNotFound = apperr.ErrNotFound
	// ErrForbidden means the caller's roles do not allow the action.
	ErrForbidden = apperr.ErrForbidden
)

// ValidationError lists invalid fields with messages for the person editing.
type ValidationError = apperr.ValidationError

// TimeOfDay is a wall-clock time in Asia/Jakarta, in minutes after midnight.
type TimeOfDay int

// ParseTimeOfDay reads a time written as 15:04.
func ParseTimeOfDay(s string) (TimeOfDay, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, fmt.Errorf("parse time of day %q: %w", s, err)
	}
	return TimeOfDay(t.Hour()*60 + t.Minute()), nil
}

// String writes the time as 15:04.
func (t TimeOfDay) String() string {
	return fmt.Sprintf("%02d:%02d", t/60, t%60)
}

// indonesian writes the time the Indonesian way, 09.00.
func (t TimeOfDay) indonesian() string {
	return fmt.Sprintf("%02d.%02d", t/60, t%60)
}

// timeOfDay returns the Jakarta wall-clock time of instant t.
func timeOfDay(t time.Time) TimeOfDay {
	j := t.In(clock.Jakarta)
	return TimeOfDay(j.Hour()*60 + j.Minute())
}

// Settings are a tenant's scheduling rules (§9.5), edited in the CMS.
type Settings struct {
	// ShoppingBufferHours is how long before the first production of a day
	// the kitchen shops for it: the batch's shopping cutoff (§15).
	ShoppingBufferHours int32
	// DailyCapacityMinutes limits the production of one day; nil means no
	// limit, the default. Checkout does not enforce it: a model of pans and
	// ovens replaces it after M3 (§27).
	DailyCapacityMinutes *int32
	// Customers pick up between PickupStart and PickupEnd, both included.
	PickupStart, PickupEnd TimeOfDay
	// UpdatedAt is zero while the defaults are in use.
	UpdatedAt time.Time
}

// DefaultSettings are the rules of a tenant that has not changed any (§27):
// shop 12 hours before production, pick up 09.00 to 17.00, no daily limit.
func DefaultSettings() Settings {
	return Settings{ShoppingBufferHours: 12, PickupStart: 9 * 60, PickupEnd: 17 * 60}
}

func (s Settings) validate() error {
	f := apperr.Fields{}
	f.Check(s.ShoppingBufferHours >= 0 && s.ShoppingBufferHours <= 168, "shopping_buffer_hours", "Waktu belanja 0 sampai 168 jam sebelum produksi.")
	f.Check(s.DailyCapacityMinutes == nil || (*s.DailyCapacityMinutes >= 1 && *s.DailyCapacityMinutes <= 1440), "daily_capacity_minutes", "Kapasitas harian 1 sampai 1440 menit, atau kosongkan kalau tidak dibatasi.")
	f.Check(s.PickupStart >= 0 && s.PickupStart < 24*60, "pickup_window_start", "Jam mulai pengambilan tidak valid.")
	f.Check(s.PickupEnd >= 0 && s.PickupEnd < 24*60, "pickup_window_end", "Jam akhir pengambilan tidak valid.")
	f.Check(s.PickupStart < s.PickupEnd, "pickup_window_end", "Jam akhir pengambilan harus setelah jam mulai.")
	return f.Err()
}

// ClosedDate is a day nothing is picked up or produced.
type ClosedDate struct {
	Date      clock.Date
	Reason    string // shown to customers, such as "Libur Lebaran"
	CreatedAt time.Time
}

var (
	dayNames   = [...]string{"Minggu", "Senin", "Selasa", "Rabu", "Kamis", "Jumat", "Sabtu"}
	monthNames = [...]string{"", "Januari", "Februari", "Maret", "April", "Mei", "Juni", "Juli", "Agustus", "September", "Oktober", "November", "Desember"}
)

// formatDate writes a date for customers: Senin, 5 Oktober 2026.
func formatDate(d clock.Date) string {
	return fmt.Sprintf("%s, %d %s %d", dayNames[d.At(0).Weekday()], d.Day, monthNames[d.Month], d.Year)
}

// formatInstant writes an instant for customers: Senin, 5 Oktober 2026 pukul 09.00 WIB.
func formatInstant(t time.Time) string {
	return formatDate(clock.DateOf(t)) + " pukul " + timeOfDay(t).indonesian() + " WIB"
}
