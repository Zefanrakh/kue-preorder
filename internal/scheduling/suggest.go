package scheduling

import (
	"time"

	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

const (
	// SuggestDays is how many days after the requested pickup Suggest tries.
	SuggestDays = 60
	// suggestStep rounds a suggested time up to a time a person would pick.
	suggestStep = 15 * time.Minute
)

// Suggest finds the nearest pickup at or after a refused one that Plan
// accepts, for the storefront to offer: a closed date moves an order to
// another day, the customer decides (§15). It tries the requested time of
// day, kept within pickup hours, on the requested day and the next
// SuggestDays days, never sooner than the items' notice allows.
//
// r.Closed and r.BatchCutoffs must cover the days tried and the day before
// each. It returns false when nothing fits.
func (s Settings) Suggest(r Request) (Plan, bool) {
	var notice int32
	for _, it := range r.Items {
		notice = max(notice, it.MinNoticeHours)
	}
	earliest := roundUp(r.Now.Add(time.Duration(notice)*time.Hour), suggestStep)
	at := min(max(timeOfDay(r.PickupAt), s.PickupStart), s.PickupEnd)

	start := clock.DateOf(r.PickupAt)
	if d := clock.DateOf(earliest); d.After(start) {
		start = d
	}
	for k := range SuggestDays + 1 {
		candidate := start.AddDays(k).At(int(at))
		if candidate.Before(earliest) {
			candidate = earliest
		}
		if timeOfDay(candidate) > s.PickupEnd || clock.DateOf(candidate) != start.AddDays(k) {
			continue
		}
		try := r
		try.PickupAt = candidate
		if p, err := s.Plan(try); err == nil {
			return p, true
		}
	}
	return Plan{}, false
}

// roundUp rounds t up to a multiple of step. Jakarta is a whole number of
// hours from UTC, so a multiple of 15 minutes is one in Jakarta too.
func roundUp(t time.Time, step time.Duration) time.Time {
	if r := t.Truncate(step); !r.Equal(t) {
		return r.Add(step)
	}
	return t
}
