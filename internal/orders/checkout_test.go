package orders_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/orders"
	"github.com/Zefanrakh/kue-preorder/internal/payments"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/scheduling"
)

func wib(day, hour, minute int) time.Time {
	return time.Date(2026, time.October, day, hour, minute, 0, 0, clock.Jakarta)
}

func date(day int) clock.Date { return clock.Date{Year: 2026, Month: time.October, Day: day} }

// Monday 5 October 2026, 10.00 WIB.
var monday10 = wib(5, 10, 0)

var tenant = uuid.MustParse("00000000-0000-0000-0000-000000000001")

type tenantOf uuid.UUID

func (t tenantOf) TenantID(context.Context) uuid.UUID { return uuid.UUID(t) }

type fakeCatalog struct {
	variants []catalog.VariantSummary
	costs    map[uuid.UUID]catalog.IngredientCost
}

func (f *fakeCatalog) Variants(_ context.Context, _ uuid.UUID, ids []uuid.UUID) ([]catalog.VariantSummary, error) {
	var out []catalog.VariantSummary
	for _, v := range f.variants {
		if slices.Contains(ids, v.ID) {
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *fakeCatalog) IngredientCosts(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]catalog.IngredientCost, error) {
	out := map[uuid.UUID]catalog.IngredientCost{}
	for _, id := range ids {
		out[id] = f.costs[id]
	}
	return out, nil
}

type fakeSchedules struct {
	settings scheduling.Settings
	closed   map[clock.Date]string
}

func (f *fakeSchedules) Settings(context.Context, uuid.UUID) (scheduling.Settings, error) {
	return f.settings, nil
}

func (f *fakeSchedules) ClosedDates(_ context.Context, _ uuid.UUID, from, to clock.Date) (map[clock.Date]string, error) {
	out := map[clock.Date]string{}
	for d, reason := range f.closed {
		if !d.Before(from) && !d.After(to) {
			out[d] = reason
		}
	}
	return out, nil
}

type fakePolicies struct{ policy payments.Policy }

func (f *fakePolicies) Policy(context.Context, uuid.UUID) (payments.Policy, error) {
	return f.policy, nil
}

type fakeRepo struct {
	cutoffs  map[clock.Date]time.Time
	statuses []orders.Status
}

func (f *fakeRepo) BatchCutoffs(_ context.Context, _ uuid.UUID, _, _ clock.Date, statuses []orders.Status) (map[clock.Date]time.Time, error) {
	f.statuses = statuses
	return f.cutoffs, nil
}

// shop sells donuts: chocolate at 8,000 and cheese at 8,500, 90 minutes of
// production and 12 hours' notice each, with 1,530 rupiah of ingredients.
type shop struct {
	chocolate, cheese uuid.UUID
	catalog           *fakeCatalog
	schedules         *fakeSchedules
	policies          *fakePolicies
	repo              *fakeRepo
	clock             *clock.Fake
	logs              *bytes.Buffer
	checkout          *orders.Checkout
}

func newShop() *shop {
	s := &shop{chocolate: uuid.New(), cheese: uuid.New(), clock: clock.NewFake(monday10), logs: &bytes.Buffer{}}
	donut := func(id uuid.UUID, name string, price int64) catalog.VariantSummary {
		return catalog.VariantSummary{ID: id, ProductName: "Donut", Name: name, PriceIDR: price, ProductionMinutes: 90, MinNoticeHours: 12, Active: true, ProductActive: true}
	}
	s.catalog = &fakeCatalog{
		variants: []catalog.VariantSummary{donut(s.chocolate, "Coklat", 8000), donut(s.cheese, "Keju", 8500)},
		costs:    map[uuid.UUID]catalog.IngredientCost{s.chocolate: {PerItemIDR: 1530}, s.cheese: {PerItemIDR: 1530}},
	}
	s.schedules = &fakeSchedules{settings: scheduling.DefaultSettings()}
	s.policies = &fakePolicies{policy: payments.DefaultPolicy()}
	s.repo = &fakeRepo{}
	s.checkout = orders.NewCheckout(s.catalog, s.schedules, s.policies, s.repo, tenantOf(tenant), s.clock, slog.New(slog.NewJSONHandler(s.logs, nil)))
	return s
}

func (s *shop) add(v catalog.VariantSummary) uuid.UUID {
	v.ID = uuid.New()
	s.catalog.variants = append(s.catalog.variants, v)
	return v.ID
}

func line(variant uuid.UUID, quantity int32) orders.ItemRequest {
	return orders.ItemRequest{VariantID: variant, Quantity: quantity}
}

func (s *shop) quote(t *testing.T, pickup time.Time, lines ...orders.ItemRequest) orders.Quote {
	t.Helper()
	q, err := s.checkout.Quote(t.Context(), orders.QuoteRequest{Items: lines, PickupAt: pickup})
	if err != nil {
		t.Fatalf("Quote() error = %v", err)
	}
	return q
}

func TestQuote_PricesAndSchedule(t *testing.T) {
	s := newShop()

	q := s.quote(t, wib(7, 9, 0), line(s.chocolate, 6), line(s.cheese, 6))

	if q.SubtotalIDR != 99000 || q.TaxIDR != 0 || q.TotalIDR != 99000 || len(q.Items) != 2 || q.Items[1].LineTotalIDR != 51000 {
		t.Errorf("prices = %+v, want 6×8,000 + 6×8,500 = 99,000", q)
	}
	if q.Items[0].ProductName != "Donut" || q.Items[0].VariantName != "Coklat" || q.Items[0].UnitPriceIDR != 8000 {
		t.Errorf("first line = %+v", q.Items[0])
	}
	// Half is 49,500; twelve donuts' ingredients cost 18,360, less than that.
	if q.Schedule == nil || q.DPRequiredIDR != 49500 {
		t.Fatalf("quote = %+v, want a schedule and a DP of 49,500", q)
	}
	p := q.Schedule
	if !p.ProductionStart.Equal(wib(7, 7, 30)) || !p.Cutoff.Equal(wib(6, 19, 30)) ||
		!p.DPDeadline.Equal(wib(5, 13, 0)) || !p.BalanceDue.Equal(wib(6, 19, 30)) || p.FullPaymentRequired {
		t.Errorf("schedule = %+v, want production 07.30, cutoff and balance Tuesday 19.30, DP within 3 hours", p)
	}
}

func TestQuote_DPCoversIngredients(t *testing.T) {
	s := newShop()
	cheap := s.add(catalog.VariantSummary{ProductName: "Donut", Name: "Murah", PriceIDR: 2000, ProductionMinutes: 90, MinNoticeHours: 12, Active: true, ProductActive: true})
	s.catalog.costs[cheap] = catalog.IngredientCost{PerItemIDR: 1530}

	// Half of 20,000 is 10,000; ten donuts' ingredients cost 15,300.
	if q := s.quote(t, wib(7, 9, 0), line(cheap, 10)); q.DPRequiredIDR != 15300 {
		t.Errorf("DP = %d, want 15,300, the ingredients", q.DPRequiredIDR)
	}
}

// Ingredients without a pack price are left out of the DP and reported, so
// the owner can fill the prices in.
func TestQuote_ReportsUnpricedIngredients(t *testing.T) {
	s := newShop()
	egg := uuid.New()
	s.catalog.costs[s.chocolate] = catalog.IngredientCost{PerItemIDR: 1530, Unpriced: []uuid.UUID{egg}}

	s.quote(t, wib(7, 9, 0), line(s.chocolate, 1))

	if logs := s.logs.String(); !strings.Contains(logs, `"level":"WARN"`) || !strings.Contains(logs, egg.String()) {
		t.Errorf("logs = %s, want a WARN naming the egg", logs)
	}
}

func TestQuote_FullPaymentWhenTheCutoffIsNear(t *testing.T) {
	s := newShop()
	s.clock.Set(wib(6, 17, 0)) // Tuesday 17.00: shopping closes at 19.30

	q := s.quote(t, wib(7, 9, 0), line(s.chocolate, 2))

	if q.Schedule == nil || !q.Schedule.FullPaymentRequired || q.DPRequiredIDR != q.TotalIDR || !q.Schedule.DPDeadline.Equal(wib(6, 19, 30)) {
		t.Errorf("quote = %+v, want full payment of 16,000 by the cutoff", q)
	}
}

func TestQuote_ClosedDateSuggestsTheNextDay(t *testing.T) {
	s := newShop()
	s.schedules.closed = map[clock.Date]string{date(7): "Ibu ke luar kota"}

	q := s.quote(t, wib(7, 9, 0), line(s.chocolate, 6))

	if q.Schedule != nil || q.Rejection == nil || !errors.Is(q.Rejection, scheduling.ErrClosed) {
		t.Fatalf("quote = %+v, want a rejection: the 7th is closed", q)
	}
	if q.Suggestion == nil || !q.Suggestion.PickupAt.Equal(wib(8, 9, 0)) {
		t.Errorf("suggestion = %+v, want Thursday 09.00", q.Suggestion)
	}
	if q.TotalIDR != 48000 || q.DPRequiredIDR != 0 {
		t.Errorf("quote = %+v, want the prices but no DP without a schedule", q)
	}
}

// The batch cutoff comes from the orders counting for production only: an
// order still waiting for its DP closes shopping for no one.
func TestQuote_BatchCutoffFromCommittedOrders(t *testing.T) {
	s := newShop()
	s.repo.cutoffs = map[clock.Date]time.Time{date(7): monday10.Add(20 * time.Minute)}

	q := s.quote(t, wib(7, 9, 0), line(s.chocolate, 1))

	if q.Rejection == nil || !errors.Is(q.Rejection, scheduling.ErrCutoffPassed) || q.Suggestion == nil || !q.Suggestion.PickupAt.Equal(wib(8, 9, 0)) {
		t.Errorf("quote = %+v, want shopping closed and Thursday suggested", q)
	}
	want := []orders.Status{orders.Confirmed, orders.InProduction, orders.Ready, orders.OutForDelivery, orders.Completed}
	if !slices.Equal(s.repo.statuses, want) {
		t.Errorf("cutoffs read for %v, want the statuses counting for production", s.repo.statuses)
	}
}

func TestQuote_RejectsCartsThatCannotBeOrdered(t *testing.T) {
	s := newShop()
	old := s.add(catalog.VariantSummary{Name: "Lama", PriceIDR: 5000, ProductionMinutes: 60, ProductActive: true})
	hidden := s.add(catalog.VariantSummary{Name: "Rahasia", PriceIDR: 5000, ProductionMinutes: 60, Active: true})
	free := s.add(catalog.VariantSummary{Name: "Gratis", PriceIDR: 0, ProductionMinutes: 60, Active: true, ProductActive: true})

	tests := []struct {
		name  string
		req   orders.QuoteRequest
		field string
	}{
		{"empty cart", orders.QuoteRequest{PickupAt: wib(7, 9, 0)}, "items"},
		{"no pickup time", orders.QuoteRequest{Items: []orders.ItemRequest{line(s.chocolate, 1)}}, "pickup_at"},
		{"zero quantity", orders.QuoteRequest{Items: []orders.ItemRequest{line(s.chocolate, 0)}, PickupAt: wib(7, 9, 0)}, "items"},
		{"too many", orders.QuoteRequest{Items: []orders.ItemRequest{line(s.chocolate, 1001)}, PickupAt: wib(7, 9, 0)}, "items"},
		{"the same cake twice", orders.QuoteRequest{Items: []orders.ItemRequest{line(s.chocolate, 1), line(s.chocolate, 2)}, PickupAt: wib(7, 9, 0)}, "items"},
		{"inactive variant", orders.QuoteRequest{Items: []orders.ItemRequest{line(old, 1)}, PickupAt: wib(7, 9, 0)}, "items"},
		{"variant of an inactive product", orders.QuoteRequest{Items: []orders.ItemRequest{line(hidden, 1)}, PickupAt: wib(7, 9, 0)}, "items"},
		{"unknown variant", orders.QuoteRequest{Items: []orders.ItemRequest{line(uuid.New(), 1)}, PickupAt: wib(7, 9, 0)}, "items"},
		{"nothing to pay", orders.QuoteRequest{Items: []orders.ItemRequest{line(free, 1)}, PickupAt: wib(7, 9, 0)}, "items"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.checkout.Quote(t.Context(), tt.req)
			var v *apperr.ValidationError
			if !errors.As(err, &v) || v.Fields[tt.field] == "" {
				t.Errorf("Quote() error = %v, want a message on %s", err, tt.field)
			}
		})
	}
}

func TestQuote_UsesTheTenantsPolicy(t *testing.T) {
	s := newShop()
	s.policies.policy = payments.Policy{DPMinPercent: 30, BalanceDueHoursBefore: 24, DPInvoiceValidMinutes: 60}

	q := s.quote(t, wib(8, 9, 0), line(s.chocolate, 10))

	if q.DPRequiredIDR != 24000 || !q.Schedule.DPDeadline.Equal(wib(5, 11, 0)) || !q.Schedule.BalanceDue.Equal(wib(7, 7, 30)) {
		t.Errorf("quote = %+v %+v, want a 30%% DP of 24,000 within an hour and the balance 24 hours before production", q, q.Schedule)
	}
}
