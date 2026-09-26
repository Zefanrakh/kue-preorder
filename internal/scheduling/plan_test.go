package scheduling_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/scheduling"
)

// wib returns the instant of a Jakarta wall-clock time in October 2026.
func wib(day, hour, minute int) time.Time {
	return time.Date(2026, time.October, day, hour, minute, 0, 0, clock.Jakarta)
}

func date(day int) clock.Date { return clock.Date{Year: 2026, Month: time.October, Day: day} }

// Monday 5 October 2026, 10.00 WIB.
var monday10 = wib(5, 10, 0)

// donut needs 90 minutes of production and a day's notice.
var donut = scheduling.Item{ProductionMinutes: 90, MinNoticeHours: 24}

func request(pickup time.Time) scheduling.Request {
	return scheduling.Request{
		Now: monday10, PickupAt: pickup, Items: []scheduling.Item{donut},
		BalanceDueHoursBefore: 12, DPValidFor: 24 * time.Hour,
	}
}

func TestPlan_Accepted(t *testing.T) {
	// Wednesday 09.00: produced from 07.30, shopping closes Tuesday 19.30.
	p, err := scheduling.DefaultSettings().Plan(request(wib(7, 9, 0)))

	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	want := scheduling.Plan{
		PickupAt:        wib(7, 9, 0),
		ProductionStart: wib(7, 7, 30),
		ProductionDate:  date(7),
		Cutoff:          wib(6, 19, 30),
		DPDeadline:      wib(6, 10, 0), // 24 hours from now, before the cutoff
		BalanceDue:      wib(6, 19, 30),
	}
	if !plansEqual(p, want) {
		t.Errorf("Plan() = %+v\nwant     %+v", p, want)
	}
}

func TestPlan_LongestItemSetsProduction(t *testing.T) {
	r := request(wib(7, 9, 0))
	r.Items = []scheduling.Item{donut, {ProductionMinutes: 240, MinNoticeHours: 2}}

	p, err := scheduling.DefaultSettings().Plan(r)

	if err != nil || !p.ProductionStart.Equal(wib(7, 5, 0)) {
		t.Errorf("Plan() = %v, %v; want production from 05.00, the 4-hour item", p.ProductionStart, err)
	}
}

func TestPlan_Boundaries(t *testing.T) {
	early := scheduling.DefaultSettings()
	early.PickupStart = 60 // 01.00, so production can start the day before
	cap300 := int32(300)
	capped := scheduling.DefaultSettings()
	capped.DailyCapacityMinutes = &cap300

	tests := []struct {
		name     string
		settings scheduling.Settings
		change   func(*scheduling.Request)
		want     error // nil: accepted
	}{
		{"exactly the earliest pickup", scheduling.DefaultSettings(), func(r *scheduling.Request) { r.PickupAt = wib(6, 10, 0) }, nil},
		{"a minute too soon", scheduling.DefaultSettings(), func(r *scheduling.Request) { r.PickupAt = wib(6, 9, 59) }, scheduling.ErrTooSoon},
		{"first minute of pickup hours", scheduling.DefaultSettings(), func(r *scheduling.Request) { r.PickupAt = wib(7, 9, 0) }, nil},
		{"a minute before pickup hours", scheduling.DefaultSettings(), func(r *scheduling.Request) { r.PickupAt = wib(7, 8, 59) }, scheduling.ErrOutsideWindow},
		{"last minute of pickup hours", scheduling.DefaultSettings(), func(r *scheduling.Request) { r.PickupAt = wib(7, 17, 0) }, nil},
		{"a minute after pickup hours", scheduling.DefaultSettings(), func(r *scheduling.Request) { r.PickupAt = wib(7, 17, 1) }, scheduling.ErrOutsideWindow},
		{"a year ahead", scheduling.DefaultSettings(), func(r *scheduling.Request) { r.PickupAt = monday10.AddDate(0, 0, 365) }, nil},
		{"more than a year ahead", scheduling.DefaultSettings(), func(r *scheduling.Request) { r.PickupAt = monday10.AddDate(0, 0, 366) }, scheduling.ErrTooFar},
		{"pickup date closed", scheduling.DefaultSettings(), func(r *scheduling.Request) {
			r.PickupAt = wib(7, 9, 0)
			r.Closed = map[clock.Date]string{date(7): "Ibu ke luar kota"}
		}, scheduling.ErrClosed},
		{"production date closed", early, func(r *scheduling.Request) {
			r.PickupAt = wib(8, 1, 0) // produced from 23.30 on the 7th
			r.Closed = map[clock.Date]string{date(7): "Libur"}
		}, scheduling.ErrClosed},
		{"the day before is closed but unused", scheduling.DefaultSettings(), func(r *scheduling.Request) {
			r.PickupAt = wib(7, 9, 0)
			r.Closed = map[clock.Date]string{date(6): "Libur"}
		}, nil},
		{"own cutoff too close", scheduling.Settings{ShoppingBufferHours: 48, PickupStart: 9 * 60, PickupEnd: 17 * 60}, func(r *scheduling.Request) {
			r.PickupAt = wib(7, 9, 0) // cutoff Monday 07.30, already past
		}, scheduling.ErrCutoffPassed},
		{"batch cutoff 30 minutes away", scheduling.DefaultSettings(), func(r *scheduling.Request) {
			r.PickupAt = wib(7, 9, 0)
			r.BatchCutoff = ptr(monday10.Add(30 * time.Minute))
		}, nil},
		{"batch cutoff 29 minutes away", scheduling.DefaultSettings(), func(r *scheduling.Request) {
			r.PickupAt = wib(7, 9, 0)
			r.BatchCutoff = ptr(monday10.Add(29 * time.Minute))
		}, scheduling.ErrCutoffPassed},
		{"capacity exactly used", capped, func(r *scheduling.Request) {
			r.PickupAt, r.LoadMinutes, r.OrderMinutes = wib(7, 9, 0), 210, 90
		}, nil},
		{"capacity exceeded", capped, func(r *scheduling.Request) {
			r.PickupAt, r.LoadMinutes, r.OrderMinutes = wib(7, 9, 0), 211, 90
		}, scheduling.ErrFull},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := request(time.Time{})
			tt.change(&r)
			_, err := tt.settings.Plan(r)
			var rejection *scheduling.Rejection
			switch {
			case tt.want == nil && err != nil:
				t.Errorf("Plan() error = %v, want accepted", err)
			case tt.want != nil && (!errors.As(err, &rejection) || !errors.Is(err, tt.want)):
				t.Errorf("Plan() error = %v, want a Rejection for %v", err, tt.want)
			}
		})
	}
}

func TestPlan_MessagesAreForCustomers(t *testing.T) {
	tests := []struct {
		name   string
		change func(*scheduling.Request)
		want   string
	}{
		{"too soon", func(r *scheduling.Request) { r.PickupAt = wib(6, 9, 0) },
			"Paling cepat bisa diambil Selasa, 6 Oktober 2026 pukul 10.00 WIB."},
		{"outside hours", func(r *scheduling.Request) { r.PickupAt = wib(7, 18, 0) },
			"Pengambilan hanya pukul 09.00–17.00 WIB."},
		{"closed", func(r *scheduling.Request) {
			r.PickupAt = wib(7, 9, 0)
			r.Closed = map[clock.Date]string{date(7): "Libur keluarga"}
		}, "Toko libur pada Rabu, 7 Oktober 2026 (Libur keluarga). Pilih tanggal lain."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := request(time.Time{})
			tt.change(&r)
			_, err := scheduling.DefaultSettings().Plan(r)
			var rejection *scheduling.Rejection
			if !errors.As(err, &rejection) || rejection.Message != tt.want {
				t.Errorf("message = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestPlan_Payments(t *testing.T) {
	t.Run("DP deadline stops at the cutoff", func(t *testing.T) {
		r := request(wib(7, 9, 0))
		r.DPValidFor = 72 * time.Hour
		p, err := scheduling.DefaultSettings().Plan(r)
		if err != nil || !p.DPDeadline.Equal(wib(6, 19, 30)) {
			t.Errorf("DPDeadline = %v, %v; want the cutoff, Tuesday 19.30", p.DPDeadline, err)
		}
	})
	t.Run("balance due before the DP deadline needs full payment", func(t *testing.T) {
		r := request(wib(7, 9, 0))
		r.BalanceDueHoursBefore = 24 // Tuesday 07.30, before the DP deadline at 10.00
		p, err := scheduling.DefaultSettings().Plan(r)
		if err != nil || !p.FullPaymentRequired || !p.BalanceDue.Equal(wib(6, 7, 30)) {
			t.Errorf("Plan() = %+v, %v; want full payment required", p, err)
		}
	})
}

func TestPlan_RejectsMalformedRequests(t *testing.T) {
	tests := map[string]func(*scheduling.Request){
		"no items":              func(r *scheduling.Request) { r.Items = nil },
		"no DP validity":        func(r *scheduling.Request) { r.DPValidFor = 0 },
		"DP validity too short": func(r *scheduling.Request) { r.DPValidFor = 29 * time.Minute },
		"negative balance gap":  func(r *scheduling.Request) { r.BalanceDueHoursBefore = -1 },
		"zero production":       func(r *scheduling.Request) { r.Items = []scheduling.Item{{ProductionMinutes: 0}} },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			r := request(wib(7, 9, 0))
			change(&r)
			_, err := scheduling.DefaultSettings().Plan(r)
			var rejection *scheduling.Rejection
			if err == nil || errors.As(err, &rejection) {
				t.Errorf("Plan() error = %v, want a plain error: the caller is wrong, not the customer", err)
			}
		})
	}
}

// The same instants written in any zone give the same plan: nothing reads
// the server's zone.
func TestPlan_IndependentOfTheCallersZone(t *testing.T) {
	far := time.FixedZone("UTC-11", -11*3600)
	r := request(wib(7, 9, 0))
	moved := r
	moved.Now, moved.PickupAt = r.Now.In(far), r.PickupAt.In(far)

	a, errA := scheduling.DefaultSettings().Plan(r)
	b, errB := scheduling.DefaultSettings().Plan(moved)

	if errA != nil || errB != nil || !plansEqual(a, b) {
		t.Errorf("plans differ by zone:\n%+v, %v\n%+v, %v", a, errA, b, errB)
	}
}

// Whatever the request, an accepted plan keeps its deadlines in order.
func TestProperty_PlanKeepsDeadlinesInOrder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		start := scheduling.TimeOfDay(rapid.IntRange(0, 22*60).Draw(t, "start"))
		s := scheduling.Settings{
			ShoppingBufferHours: rapid.Int32Range(0, 168).Draw(t, "buffer"),
			PickupStart:         start,
			PickupEnd:           scheduling.TimeOfDay(rapid.IntRange(int(start)+1, 24*60-1).Draw(t, "end")),
		}
		now := monday10.Add(time.Duration(rapid.Int64Range(0, 30*24*60).Draw(t, "now")) * time.Minute)
		r := scheduling.Request{
			Now:                   now,
			PickupAt:              now.Add(time.Duration(rapid.Int64Range(-60, 400*24*60).Draw(t, "ahead")) * time.Minute),
			BalanceDueHoursBefore: rapid.Int32Range(0, 72).Draw(t, "balance"),
			DPValidFor:            time.Duration(rapid.Int64Range(30, 72*60).Draw(t, "dp valid")) * time.Minute,
		}
		for range rapid.IntRange(1, 4).Draw(t, "items") {
			r.Items = append(r.Items, scheduling.Item{
				ProductionMinutes: rapid.Int32Range(1, 240).Draw(t, "production"),
				MinNoticeHours:    rapid.Int32Range(0, 96).Draw(t, "notice"),
			})
		}
		if rapid.Bool().Draw(t, "has batch") {
			r.BatchCutoff = ptr(now.Add(time.Duration(rapid.Int64Range(-600, 30*24*60).Draw(t, "batch")) * time.Minute))
		}

		p, err := s.Plan(r)

		var rejection *scheduling.Rejection
		if err != nil {
			if !errors.As(err, &rejection) || rejection.Message == "" || !strings.HasSuffix(rejection.Message, ".") {
				t.Fatalf("Plan() error = %v, want a Rejection with a message", err)
			}
			return
		}
		ordered := now.Before(p.DPDeadline) && !p.DPDeadline.After(p.Cutoff) &&
			!p.Cutoff.After(p.ProductionStart) && !p.ProductionStart.After(p.PickupAt) && !p.BalanceDue.After(p.ProductionStart)
		if !ordered {
			t.Fatalf("deadlines out of order: now %v, %+v", now, p)
		}
		if p.DPDeadline.Sub(now) < 30*time.Minute {
			t.Fatalf("only %v to pay the DP", p.DPDeadline.Sub(now))
		}
		if r.BatchCutoff != nil && p.Cutoff.After(*r.BatchCutoff) {
			t.Fatalf("cutoff %v is after the batch's %v", p.Cutoff, *r.BatchCutoff)
		}
		if p.FullPaymentRequired != !p.BalanceDue.After(p.DPDeadline) {
			t.Fatalf("FullPaymentRequired = %v with balance due %v and DP deadline %v", p.FullPaymentRequired, p.BalanceDue, p.DPDeadline)
		}
		if p.ProductionDate != clock.DateOf(p.ProductionStart) {
			t.Fatalf("production date %v is not the date of %v", p.ProductionDate, p.ProductionStart)
		}
	})
}

func plansEqual(a, b scheduling.Plan) bool {
	return a.PickupAt.Equal(b.PickupAt) && a.ProductionStart.Equal(b.ProductionStart) && a.ProductionDate == b.ProductionDate &&
		a.Cutoff.Equal(b.Cutoff) && a.DPDeadline.Equal(b.DPDeadline) && a.BalanceDue.Equal(b.BalanceDue) &&
		a.FullPaymentRequired == b.FullPaymentRequired
}

func ptr[T any](v T) *T { return &v }
