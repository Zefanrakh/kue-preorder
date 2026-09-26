package clock_test

import (
	"testing"
	"time"

	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

func TestDateOf_UsesJakarta(t *testing.T) {
	// 18:30 UTC on 30 September is 01:30 WIB on 1 October.
	late := time.Date(2026, 9, 30, 18, 30, 0, 0, time.UTC)
	if got, want := clock.DateOf(late), (clock.Date{Year: 2026, Month: time.October, Day: 1}); got != want {
		t.Errorf("DateOf(%v) = %v, want %v", late, got, want)
	}
	// The same instant expressed in another zone is the same WIB day.
	if got := clock.DateOf(late.In(time.FixedZone("PST", -8*3600))); got.Day != 1 {
		t.Errorf("DateOf(PST) = %v, want 1 October", got)
	}
}

func TestDate_RoundTripsAndArithmetic(t *testing.T) {
	d, err := clock.ParseDate("2026-12-31")
	if err != nil {
		t.Fatal(err)
	}
	if d.String() != "2026-12-31" || d.AddDays(1).String() != "2027-01-01" || d.AddDays(-365).String() != "2025-12-31" {
		t.Errorf("date arithmetic: %v, %v, %v", d, d.AddDays(1), d.AddDays(-365))
	}
	if at := d.At(9 * 60); !at.Equal(time.Date(2026, 12, 31, 2, 0, 0, 0, time.UTC)) {
		t.Errorf("At(09:00) = %v, want 02:00 UTC", at)
	}
	if !d.Before(d.AddDays(1)) || !d.AddDays(1).After(d) || d.Compare(d) != 0 {
		t.Error("Before/After/Compare disagree")
	}
	if _, err := clock.ParseDate("31-12-2026"); err == nil {
		t.Error("ParseDate(31-12-2026) succeeded, want an error")
	}
}
