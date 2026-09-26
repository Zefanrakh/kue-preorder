package db_test

import (
	"testing"
	"time"

	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
)

func TestDate_RoundTrips(t *testing.T) {
	d := clock.Date{Year: 2026, Month: time.December, Day: 31}
	if got := db.FromDate(db.Date(d)); got != d {
		t.Errorf("FromDate(Date(%v)) = %v", d, got)
	}
}
