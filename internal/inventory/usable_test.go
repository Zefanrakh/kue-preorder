package inventory_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/inventory"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

func wib(day, hour, minute int) time.Time {
	return time.Date(2026, time.October, day, hour, minute, 0, 0, clock.Jakarta)
}

func at(t time.Time) *time.Time { return &t }

// Every row of §12's table, at its edges. The batch is made on Wednesday
// 7 October; now is Tuesday 6 October, 18.00.
func TestCountable(t *testing.T) {
	wednesday, now := clock.Date{Year: 2026, Month: time.October, Day: 7}, wib(6, 18, 0)
	lot := func(mut func(*inventory.Lot)) inventory.Lot {
		l := inventory.Lot{ID: uuid.New(), IngredientID: uuid.New(), ReceivedAt: wib(1, 9, 0), Status: inventory.LotAvailable, Balance: 500}
		if mut != nil {
			mut(&l)
		}
		return l
	}
	tests := []struct {
		name   string
		lot    inventory.Lot
		policy catalog.LeftoverPolicy
		want   bool
	}{
		{"auto, no expiry", lot(nil), catalog.LeftoverAuto, true},
		{"auto, good through the batch's day", lot(func(l *inventory.Lot) { l.ExpiresAt = at(wib(8, 0, 0)) }), catalog.LeftoverAuto, true},
		{"auto, expires during the batch's day", lot(func(l *inventory.Lot) { l.ExpiresAt = at(wib(7, 23, 59)) }), catalog.LeftoverAuto, false},
		{"auto, already expired", lot(func(l *inventory.Lot) { l.ExpiresAt = at(wib(5, 0, 0)) }), catalog.LeftoverAuto, false},
		{"confirm, checked 23h59m ago", lot(func(l *inventory.Lot) { l.LastOKAt = at(now.Add(-23*time.Hour - 59*time.Minute)) }), catalog.LeftoverConfirm, true},
		{"confirm, checked exactly 24h ago", lot(func(l *inventory.Lot) { l.LastOKAt = at(now.Add(-24 * time.Hour)) }), catalog.LeftoverConfirm, true},
		{"confirm, checked 24h01m ago", lot(func(l *inventory.Lot) { l.LastOKAt = at(now.Add(-24*time.Hour - time.Minute)) }), catalog.LeftoverConfirm, false},
		{"confirm, never checked", lot(nil), catalog.LeftoverConfirm, false},
		{"confirm, checked 'in the future'", lot(func(l *inventory.Lot) { l.LastOKAt = at(now.Add(time.Minute)) }), catalog.LeftoverConfirm, false},
		{"confirm, checked but expiring", lot(func(l *inventory.Lot) {
			l.LastOKAt, l.ExpiresAt = at(now.Add(-time.Hour)), at(wib(7, 12, 0))
		}), catalog.LeftoverConfirm, false},
		{"never", lot(func(l *inventory.Lot) { l.LastOKAt = at(now.Add(-time.Hour)) }), catalog.LeftoverNever, false},
		{"expired lot", lot(func(l *inventory.Lot) { l.Status = inventory.LotExpired }), catalog.LeftoverAuto, false},
		{"discarded lot", lot(func(l *inventory.Lot) { l.Status = inventory.LotDiscarded }), catalog.LeftoverAuto, false},
		{"nothing left", lot(func(l *inventory.Lot) { l.Balance = 0 }), catalog.LeftoverAuto, false},
		{"unknown policy", lot(nil), "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inventory.Countable(tt.lot, tt.policy, wednesday, now); got != tt.want {
				t.Errorf("Countable() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestUsable_SumsPerIngredient(t *testing.T) {
	flour, egg, unknown := uuid.New(), uuid.New(), uuid.New()
	now := wib(6, 18, 0)
	lots := []inventory.Lot{
		{IngredientID: flour, Status: inventory.LotAvailable, Balance: 300},
		{IngredientID: flour, Status: inventory.LotAvailable, Balance: 700},
		{IngredientID: egg, Status: inventory.LotAvailable, Balance: 6}, // unchecked
		{IngredientID: egg, Status: inventory.LotAvailable, Balance: 4, LastOKAt: at(now.Add(-time.Hour))},
		{IngredientID: unknown, Status: inventory.LotAvailable, Balance: 50},
	}
	policies := map[uuid.UUID]catalog.LeftoverPolicy{flour: catalog.LeftoverAuto, egg: catalog.LeftoverConfirm}

	got := inventory.Usable(lots, policies, clock.Date{Year: 2026, Month: time.October, Day: 7}, now)

	if got[flour] != 1000 || got[egg] != 4 || got[unknown] != 0 {
		t.Errorf("Usable() = %v, want 1,000 g of flour, 4 checked eggs, nothing unknown", got)
	}
}
