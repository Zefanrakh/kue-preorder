package scheduling_test

import (
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/scheduling"
)

func TestSuggest(t *testing.T) {
	tests := []struct {
		name   string
		change func(*scheduling.Request)
		want   time.Time
	}{
		{"closed day moves to the next", func(r *scheduling.Request) {
			r.PickupAt = wib(7, 9, 0)
			r.Closed = map[clock.Date]string{date(7): "Libur"}
		}, wib(8, 9, 0)},
		{"two closed days", func(r *scheduling.Request) {
			r.PickupAt = wib(7, 14, 30)
			r.Closed = map[clock.Date]string{date(7): "Libur", date(8): "Libur"}
		}, wib(9, 14, 30)},
		{"too soon stays the same day, as early as the notice allows", func(r *scheduling.Request) {
			r.PickupAt = wib(6, 9, 0)
		}, wib(6, 10, 0)},
		{"earliest rounds up to a quarter hour", func(r *scheduling.Request) {
			r.Now = wib(5, 10, 7)
			r.PickupAt = wib(6, 9, 0)
		}, wib(6, 10, 15)},
		{"after pickup hours comes back to the last minute", func(r *scheduling.Request) {
			r.PickupAt = wib(7, 18, 0)
		}, wib(7, 17, 0)},
		{"shopping closed by the batch moves on", func(r *scheduling.Request) {
			r.PickupAt = wib(7, 9, 0)
			r.BatchCutoffs = map[clock.Date]time.Time{date(7): monday10.Add(10 * time.Minute)}
		}, wib(8, 9, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := request(time.Time{})
			tt.change(&r)
			p, ok := scheduling.DefaultSettings().Suggest(r)
			if !ok || !p.PickupAt.Equal(tt.want) {
				t.Errorf("Suggest() = %v, %v; want %v", p.PickupAt.In(clock.Jakarta), ok, tt.want)
			}
		})
	}
}

func TestSuggest_NothingFits(t *testing.T) {
	r := request(wib(7, 9, 0))
	r.Closed = map[clock.Date]string{}
	for d := date(1); !d.After(date(7).AddDays(scheduling.SuggestDays + 1)); d = d.AddDays(1) {
		r.Closed[d] = "Tutup sementara"
	}

	if p, ok := scheduling.DefaultSettings().Suggest(r); ok {
		t.Errorf("Suggest() = %v, want nothing: every day is closed", p.PickupAt)
	}
}

// Whatever was refused, a suggestion is a pickup Plan accepts, on or after
// the requested day.
func TestProperty_SuggestionsAreAccepted(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := scheduling.DefaultSettings()
		now := monday10.Add(time.Duration(rapid.Int64Range(0, 7*24*60).Draw(t, "now")) * time.Minute)
		r := scheduling.Request{
			Now:                   now,
			PickupAt:              now.Add(time.Duration(rapid.Int64Range(0, 20*24*60).Draw(t, "ahead")) * time.Minute),
			Items:                 []scheduling.Item{{ProductionMinutes: rapid.Int32Range(1, 240).Draw(t, "production"), MinNoticeHours: rapid.Int32Range(0, 72).Draw(t, "notice")}},
			Closed:                map[clock.Date]string{},
			BalanceDueHoursBefore: 12,
			DPValidFor:            3 * time.Hour,
		}
		for range rapid.IntRange(0, 10).Draw(t, "closed days") {
			r.Closed[clock.DateOf(r.PickupAt).AddDays(rapid.IntRange(-1, 15).Draw(t, "closed"))] = "Libur"
		}

		p, ok := s.Suggest(r)
		if !ok {
			return
		}
		again := r
		again.PickupAt = p.PickupAt
		if _, err := s.Plan(again); err != nil {
			t.Fatalf("suggested %v, but Plan refuses it: %v", p.PickupAt, err)
		}
		if clock.DateOf(p.PickupAt).Before(clock.DateOf(r.PickupAt)) {
			t.Fatalf("suggested %v, before the requested day %v", p.PickupAt, r.PickupAt)
		}
	})
}
