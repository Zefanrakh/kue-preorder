package scheduling

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

const (
	// maxAdvance is how far ahead a pickup may be; further is surely a typo.
	maxAdvance = 365 * 24 * time.Hour
	// minPaymentWindow is the least time a customer gets to pay the DP before
	// the batch's shopping cutoff. Less than this cannot work in practice.
	minPaymentWindow = 30 * time.Minute
)

// Why a pickup time is refused. Checkout shows the Rejection's message on
// the pickup field; the reason lets the storefront react, such as suggesting
// another date.
var (
	ErrTooFar        = errors.New("pickup is too far ahead")
	ErrTooSoon       = errors.New("pickup is sooner than the notice the items need")
	ErrOutsideWindow = errors.New("pickup is outside the pickup hours")
	ErrClosed        = errors.New("the shop is closed that day")
	ErrCutoffPassed  = errors.New("shopping for that day has closed")
	ErrFull          = errors.New("that day's production is full")
)

// Rejection refuses a pickup time with a message for the customer, in
// Indonesian. errors.Is matches its reason.
type Rejection struct {
	Reason  error
	Message string
}

func (r *Rejection) Error() string { return r.Reason.Error() + ": " + r.Message }

// Unwrap returns the reason.
func (r *Rejection) Unwrap() error { return r.Reason }

// Item is what scheduling needs of one variant in the cart.
type Item struct {
	ProductionMinutes int32 // 1 to 240
	MinNoticeHours    int32 // order to pickup, shopping included
}

// Request asks whether an order can be picked up at PickupAt.
type Request struct {
	Now      time.Time
	PickupAt time.Time
	Items    []Item
	// Closed holds the closed dates that could matter (at least the pickup
	// date and the day before it), with their reasons.
	Closed map[clock.Date]string
	// BatchCutoffs holds, for each production date that already has
	// committed orders, the earliest shopping cutoff among them.
	BatchCutoffs map[clock.Date]time.Time
	// LoadMinutes is the production date's committed load and OrderMinutes
	// what this order adds; checkout (M2.3) decides how they are counted.
	LoadMinutes, OrderMinutes int32
	// BalanceDueHoursBefore is the payment policy's gap between the balance
	// deadline and the start of production (§14).
	BalanceDueHoursBefore int32
	// DPValidFor is the longest a DP invoice stays open, at least 30 minutes.
	DPValidFor time.Duration
}

// Plan is the schedule of one order, locked at checkout. Always
//
//	Now < DPDeadline <= Cutoff <= ProductionStart <= PickupAt
//	BalanceDue <= ProductionStart
type Plan struct {
	PickupAt time.Time
	// ProductionStart is the pickup minus the longest production time among
	// the items: several cakes are made side by side.
	ProductionStart time.Time
	// ProductionDate is the Jakarta date of ProductionStart: the batch key.
	ProductionDate clock.Date
	// Cutoff is the batch's shopping cutoff with this order in it.
	Cutoff time.Time
	// DPDeadline is when the DP invoice expires: never after the cutoff, so
	// no order joins a batch whose shopping is done.
	DPDeadline time.Time
	// BalanceDue is when the balance must be paid (§14).
	BalanceDue time.Time
	// FullPaymentRequired is set when the balance would fall due before the
	// DP deadline: the first payment must then be the whole total.
	FullPaymentRequired bool
}

// Plan checks a pickup time against the rules of §15 and returns the
// order's schedule, or a *Rejection. Checks run from the most basic, so the
// customer reads the reason that matters first. A malformed request (no
// items, a DP validity under 30 minutes) is an error, not a rejection.
func (s Settings) Plan(r Request) (Plan, error) {
	if len(r.Items) == 0 {
		return Plan{}, errors.New("plan a pickup: no items")
	}
	if r.DPValidFor < minPaymentWindow || r.BalanceDueHoursBefore < 0 {
		return Plan{}, fmt.Errorf("plan a pickup: DP validity %v, balance due %d hours before", r.DPValidFor, r.BalanceDueHoursBefore)
	}
	var production, notice int32
	for _, it := range r.Items {
		if it.ProductionMinutes < 1 || it.MinNoticeHours < 0 {
			return Plan{}, fmt.Errorf("plan a pickup: item with %d production minutes, %d notice hours", it.ProductionMinutes, it.MinNoticeHours)
		}
		production = max(production, it.ProductionMinutes)
		notice = max(notice, it.MinNoticeHours)
	}

	if r.PickupAt.Sub(r.Now) > maxAdvance {
		return Plan{}, &Rejection{ErrTooFar, "Pesanan paling jauh 365 hari ke depan."}
	}
	if earliest := r.Now.Add(time.Duration(notice) * time.Hour); r.PickupAt.Before(earliest) {
		return Plan{}, &Rejection{ErrTooSoon, "Paling cepat bisa diambil " + formatInstant(earliest) + "."}
	}
	if at := timeOfDay(r.PickupAt); at < s.PickupStart || at > s.PickupEnd {
		return Plan{}, &Rejection{ErrOutsideWindow, fmt.Sprintf("Pengambilan hanya pukul %s–%s WIB.", s.PickupStart.indonesian(), s.PickupEnd.indonesian())}
	}

	p := Plan{PickupAt: r.PickupAt, ProductionStart: r.PickupAt.Add(-time.Duration(production) * time.Minute)}
	p.ProductionDate = clock.DateOf(p.ProductionStart)
	for _, d := range uniqueDates(clock.DateOf(r.PickupAt), p.ProductionDate) {
		if reason, closed := r.Closed[d]; closed {
			return Plan{}, &Rejection{ErrClosed, fmt.Sprintf("Toko libur pada %s (%s). Pilih tanggal lain.", formatDate(d), reason)}
		}
	}

	p.Cutoff = p.ProductionStart.Add(-time.Duration(s.ShoppingBufferHours) * time.Hour)
	if batch, ok := r.BatchCutoffs[p.ProductionDate]; ok && batch.Before(p.Cutoff) {
		p.Cutoff = batch
	}
	if p.Cutoff.Sub(r.Now) < minPaymentWindow {
		return Plan{}, &Rejection{ErrCutoffPassed, "Belanja bahan untuk tanggal " + formatDate(p.ProductionDate) + " sudah ditutup. Pilih tanggal lain."}
	}
	if s.DailyCapacityMinutes != nil && r.LoadMinutes+r.OrderMinutes > *s.DailyCapacityMinutes {
		return Plan{}, &Rejection{ErrFull, "Kapasitas produksi tanggal " + formatDate(p.ProductionDate) + " sudah penuh. Pilih tanggal lain."}
	}

	p.DPDeadline = r.Now.Add(r.DPValidFor)
	if p.Cutoff.Before(p.DPDeadline) {
		p.DPDeadline = p.Cutoff
	}
	p.BalanceDue = p.ProductionStart.Add(-time.Duration(r.BalanceDueHoursBefore) * time.Hour)
	p.FullPaymentRequired = !p.BalanceDue.After(p.DPDeadline)
	return p, nil
}

func uniqueDates(ds ...clock.Date) []clock.Date {
	slices.SortFunc(ds, clock.Date.Compare)
	return slices.Compact(ds)
}
