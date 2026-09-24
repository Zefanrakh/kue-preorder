package clock_test

import (
	"sync"
	"testing"
	"time"

	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

func TestFake_AdvanceMovesNow(t *testing.T) {
	start := time.Date(2026, 9, 23, 1, 0, 0, 0, time.UTC)
	c := clock.NewFake(start)

	c.Advance(90 * time.Minute)

	want := start.Add(90 * time.Minute)
	if got := c.Now(); !got.Equal(want) {
		t.Fatalf("Now() = %v, want %v", got, want)
	}
}

func TestFake_SetReplacesNow(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	want := time.Date(2026, 12, 31, 23, 59, 0, 0, time.UTC)

	c.Set(want)

	if got := c.Now(); !got.Equal(want) {
		t.Fatalf("Now() = %v, want %v", got, want)
	}
}

func TestFake_ConcurrentAdvanceIsSafe(t *testing.T) {
	start := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	c := clock.NewFake(start)

	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			c.Advance(time.Minute)
			_ = c.Now()
		})
	}
	wg.Wait()

	if got, want := c.Now(), start.Add(50*time.Minute); !got.Equal(want) {
		t.Fatalf("Now() = %v, want %v", got, want)
	}
}

func TestReal_ReturnsCurrentTime(t *testing.T) {
	before := time.Now()
	got := clock.Real{}.Now()
	after := time.Now()

	if got.Before(before) || got.After(after) {
		t.Fatalf("Now() = %v, want between %v and %v", got, before, after)
	}
}

func TestJakarta_IsUTCPlus7WithoutDST(t *testing.T) {
	tests := []struct {
		name string
		utc  time.Time
	}{
		{"january", time.Date(2026, 1, 15, 17, 0, 0, 0, time.UTC)},
		{"july", time.Date(2026, 7, 15, 17, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local := tt.utc.In(clock.Jakarta)

			name, offset := local.Zone()
			if offset != 7*60*60 {
				t.Errorf("offset = %ds, want %ds", offset, 7*60*60)
			}
			if name != "WIB" {
				t.Errorf("zone name = %q, want %q", name, "WIB")
			}
			// 17:00 UTC is midnight in Jakarta: the production date rolls over.
			if local.Day() != tt.utc.Day()+1 || local.Hour() != 0 {
				t.Errorf("local = %v, want 00:00 on the next day", local)
			}
		})
	}
}
