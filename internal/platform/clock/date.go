package clock

import (
	"fmt"
	"time"
)

// Date is a calendar day in Jakarta: a production date, a pickup date, or a
// day the shop is closed. It is comparable, so it works as a map key.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// DateOf returns the Jakarta calendar day of t, wherever t was created.
func DateOf(t time.Time) Date {
	y, m, d := t.In(Jakarta).Date()
	return Date{y, m, d}
}

// ParseDate reads a date written as 2006-01-02.
func ParseDate(s string) (Date, error) {
	t, err := time.ParseInLocation(time.DateOnly, s, Jakarta)
	if err != nil {
		return Date{}, fmt.Errorf("parse date %q: %w", s, err)
	}
	return DateOf(t), nil
}

// String writes the date as 2006-01-02.
func (d Date) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
}

// At returns the instant of the given minutes after midnight, Jakarta time,
// on the date.
func (d Date) At(minutes int) time.Time {
	return time.Date(d.Year, d.Month, d.Day, 0, minutes, 0, 0, Jakarta)
}

// AddDays returns the date n days later (earlier when n < 0).
func (d Date) AddDays(n int) Date {
	return DateOf(d.At(12*60).AddDate(0, 0, n))
}

// Compare returns -1, 0, or +1 as d is before, the same as, or after o.
func (d Date) Compare(o Date) int {
	return d.At(0).Compare(o.At(0))
}

// Before reports whether d is before o.
func (d Date) Before(o Date) bool { return d.Compare(o) < 0 }

// After reports whether d is after o.
func (d Date) After(o Date) bool { return d.Compare(o) > 0 }
