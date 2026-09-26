package orders_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"pgregory.net/rapid"

	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/orders"
	"github.com/Zefanrakh/kue-preorder/internal/payments"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

func (s *shop) placeRequest(pickup time.Time, lines ...orders.ItemRequest) orders.PlaceRequest {
	return orders.PlaceRequest{
		QuoteRequest:   orders.QuoteRequest{Items: lines, PickupAt: pickup},
		TermsVersion:   orders.TermsVersion,
		Customer:       identity.CustomerInput{Name: "Sari", Email: "sari@contoh.com"},
		Notes:          "  Tulisan: Selamat ulang tahun Budi  ",
		IdempotencyKey: uuid.New(),
	}
}

func (s *shop) place(t *testing.T, req orders.PlaceRequest) orders.Order {
	t.Helper()
	o, err := s.checkout.Place(t.Context(), req)
	if err != nil {
		t.Fatalf("Place() error = %v", err)
	}
	return o
}

func TestPlace_AwaitsTheDP(t *testing.T) {
	s := newShop()

	o := s.place(t, s.placeRequest(wib(7, 9, 0), line(s.chocolate, 6), line(s.cheese, 6)))

	if o.Status != orders.AwaitingDP || o.Payment != payments.Unpaid || o.Fulfillment != orders.Pickup {
		t.Errorf("order = %s/%s/%s, want awaiting_dp, unpaid, pickup", o.Status, o.Payment, o.Fulfillment)
	}
	if len(o.Code) != 6 || o.TotalIDR != 99000 || o.DPRequiredIDR != 49500 || len(o.Items) != 2 {
		t.Errorf("order = %+v, want a code and the quoted prices", o)
	}
	if o.Notes != "Tulisan: Selamat ulang tahun Budi" || o.CustomerName != "Sari" || o.CustomerPhone != "+6281234567890" || o.TermsVersion != orders.TermsVersion {
		t.Errorf("order = %+v, want the trimmed notes, the customer as at checkout, and the terms", o)
	}
	if !o.DPDueAt.Equal(wib(5, 13, 0)) || !o.BalanceDueAt.Equal(wib(6, 19, 30)) || o.ProductionDate != date(7) {
		t.Errorf("schedule = DP %v, balance %v, day %v", o.DPDueAt, o.BalanceDueAt, o.ProductionDate)
	}
	if len(o.Payments) != 1 {
		t.Fatalf("payments = %+v, want the DP", o.Payments)
	}
	p := o.Payments[0]
	if p.Kind != payments.KindDP || p.AmountIDR != 49500 || p.State != payments.StatePending || p.CheckoutURL == "" || !p.ExpiresAt.Equal(o.DPDueAt) {
		t.Errorf("payment = %+v, want a pending DP of 49,500 with a link, open until the DP deadline", p)
	}
	if r := s.provider.requests[0]; r.Description != "DP pesanan "+o.Code || r.CustomerPhone != "+6281234567890" || r.AmountIDR != 49500 {
		t.Errorf("invoice request = %+v", r)
	}
	if !slices.Equal(s.repo.locked, []uuid.UUID{s.customers.customer.ID}) {
		t.Errorf("locked %v, want the customer's checkouts serialized", s.repo.locked)
	}
}

func TestPlace_PayInFull(t *testing.T) {
	s := newShop()
	req := s.placeRequest(wib(7, 9, 0), line(s.chocolate, 2))
	req.PayInFull = true

	o := s.place(t, req)

	if p := o.Payments[0]; p.Kind != payments.KindFull || p.AmountIDR != 16000 || o.DPRequiredIDR != 8000 {
		t.Errorf("payment = %+v, DP %d; want the full 16,000 now, the DP rule kept on the order", p, o.DPRequiredIDR)
	}
}

func TestPlace_FullPaymentWhenTheScheduleRequiresIt(t *testing.T) {
	s := newShop()
	s.clock.Set(wib(6, 17, 0)) // shopping closes at 19.30

	o := s.place(t, s.placeRequest(wib(7, 9, 0), line(s.chocolate, 2)))

	if p := o.Payments[0]; !o.FullPaymentRequired || p.Kind != payments.KindFull || p.AmountIDR != 16000 {
		t.Errorf("order = %+v, want full payment", o)
	}
}

// The same checkout sent twice, a double tap or a retry after a timeout,
// is one order, billed once.
func TestPlace_IsIdempotent(t *testing.T) {
	s := newShop()
	req := s.placeRequest(wib(7, 9, 0), line(s.chocolate, 1))

	first := s.place(t, req)
	second := s.place(t, req)

	if first.ID != second.ID || s.repo.inserts != 1 || len(s.ledger.payments) != 1 || s.provider.calls() != 1 {
		t.Errorf("orders %s and %s, %d inserts, %d payments, %d invoices; want one of each", first.Code, second.Code, s.repo.inserts, len(s.ledger.payments), s.provider.calls())
	}
}

func TestPlace_AtMostTwoAwaitingDP(t *testing.T) {
	s := newShop()
	s.place(t, s.placeRequest(wib(7, 9, 0), line(s.chocolate, 1)))
	s.place(t, s.placeRequest(wib(7, 10, 0), line(s.chocolate, 1)))

	_, err := s.checkout.Place(t.Context(), s.placeRequest(wib(7, 11, 0), line(s.chocolate, 1)))

	var p *apperr.PreconditionError
	if !errors.As(err, &p) || p.Reason != "too_many_unpaid" || s.repo.inserts != 2 {
		t.Errorf("third Place() error = %v after %d inserts, want too_many_unpaid and no third order", err, s.repo.inserts)
	}
}

func TestPlace_DrawsAnotherCodeOnAClash(t *testing.T) {
	s := newShop()
	s.repo.clashes = 2

	o := s.place(t, s.placeRequest(wib(7, 9, 0), line(s.chocolate, 1)))

	if s.repo.inserts != 1 || len(o.Code) != 6 {
		t.Errorf("order %q after %d inserts, want one after two clashes", o.Code, s.repo.inserts)
	}
}

// When the provider is down the order still stands; the next look at it
// makes the invoice.
func TestPlace_InvoiceRetriedWhenTheProviderFails(t *testing.T) {
	s := newShop()
	s.provider.broken = true

	o := s.place(t, s.placeRequest(wib(7, 9, 0), line(s.chocolate, 1)))

	if o.Payments[0].CheckoutURL != "" || !strings.Contains(s.logs.String(), `"level":"ERROR"`) {
		t.Fatalf("payment = %+v, logs %s; want no link yet and an alert", o.Payments[0], s.logs.String())
	}
	s.provider.broken = false
	again, err := s.checkout.MyOrder(t.Context(), strings.ToLower(o.Code))
	if err != nil || again.Payments[0].CheckoutURL == "" {
		t.Errorf("MyOrder() = %+v, %v; want the link made now", again.Payments, err)
	}
	if s.provider.calls() != 2 || s.provider.requests[0].PaymentID != s.provider.requests[1].PaymentID {
		t.Errorf("invoice requests = %+v, want the same payment asked twice", s.provider.requests)
	}
}

func TestPlace_Rejects(t *testing.T) {
	tests := []struct {
		name   string
		change func(*shop, *orders.PlaceRequest)
		field  string
	}{
		{"old terms", func(_ *shop, r *orders.PlaceRequest) { r.TermsVersion = "dp-old" }, "terms_version"},
		{"notes too long", func(_ *shop, r *orders.PlaceRequest) { r.Notes = strings.Repeat("a", 501) }, "notes"},
		{"no idempotency key", func(_ *shop, r *orders.PlaceRequest) { r.IdempotencyKey = uuid.Nil }, "idempotency_key"},
		{"closed pickup day", func(s *shop, _ *orders.PlaceRequest) { s.schedules.closed = map[clock.Date]string{date(7): "Libur"} }, "pickup_at"},
		{"empty cart", func(_ *shop, r *orders.PlaceRequest) { r.Items = nil }, "items"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newShop()
			req := s.placeRequest(wib(7, 9, 0), line(s.chocolate, 1))
			tt.change(s, &req)
			_, err := s.checkout.Place(t.Context(), req)
			var v *apperr.ValidationError
			if !errors.As(err, &v) || v.Fields[tt.field] == "" || s.repo.inserts != 0 {
				t.Errorf("Place() error = %v, want a message on %s and nothing stored", err, tt.field)
			}
		})
	}
}

func TestPlace_NeedsAPhone(t *testing.T) {
	s := newShop()
	s.customers.err = identity.ErrPhoneRequired

	_, err := s.checkout.Place(t.Context(), s.placeRequest(wib(7, 9, 0), line(s.chocolate, 1)))

	if !errors.Is(err, apperr.ErrFailedPrecondition) || s.repo.inserts != 0 {
		t.Errorf("Place() error = %v, want phone_required and nothing stored", err)
	}
}

func TestMyOrders(t *testing.T) {
	s := newShop()
	if got, err := s.checkout.MyOrders(t.Context()); err != nil || len(got) != 0 {
		t.Errorf("MyOrders() before ordering = %v, %v; want none", got, err)
	}
	o := s.place(t, s.placeRequest(wib(7, 9, 0), line(s.chocolate, 1)))

	if got, err := s.checkout.MyOrders(t.Context()); err != nil || len(got) != 1 || got[0].Code != o.Code {
		t.Errorf("MyOrders() = %v, %v; want the order", got, err)
	}
	if _, err := s.checkout.MyOrder(t.Context(), "ZZZZZZ"); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("MyOrder(unknown) error = %v, want ErrNotFound", err)
	}

	// Someone else, signed in, cannot open it.
	s.customers.customer.ID = uuid.New()
	if _, err := s.checkout.MyOrder(t.Context(), o.Code); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("MyOrder(another customer's) error = %v, want ErrNotFound", err)
	}
	s.customers.anon = true
	if _, err := s.checkout.MyOrders(t.Context()); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Errorf("MyOrders(anonymous) error = %v, want ErrUnauthenticated", err)
	}
}

func TestReader_ActiveOrdersCountsLiveOrdersOnly(t *testing.T) {
	repo := newFakeRepo()
	repo.activeDays = map[clock.Date]int{date(7): 2}

	got, err := orders.NewReader(repo).ActiveOrders(t.Context(), tenant, date(1), date(31))

	want := []orders.Status{orders.AwaitingDP, orders.Confirmed, orders.InProduction, orders.Ready, orders.OutForDelivery}
	if err != nil || got[date(7)] != 2 || !slices.Equal(repo.activeStatuses, want) {
		t.Errorf("ActiveOrders() = %v, %v with statuses %v; want every status not yet final", got, err, repo.activeStatuses)
	}
}

func TestNewCode(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		code := orders.NewCode()
		if len(code) != 6 || strings.ContainsAny(code, "01ILO") {
			t.Fatalf("NewCode() = %q, want six characters without look-alikes", code)
		}
		for _, r := range code {
			if !strings.ContainsRune("ABCDEFGHJKMNPQRSTUVWXYZ23456789", r) {
				t.Fatalf("NewCode() = %q has %q", code, r)
			}
		}
	})
}
