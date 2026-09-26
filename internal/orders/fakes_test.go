package orders_test

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/orders"
	"github.com/Zefanrakh/kue-preorder/internal/payments"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

// fakeRepo keeps orders in memory.
type fakeRepo struct {
	mu             sync.Mutex
	cutoffs        map[clock.Date]time.Time
	statuses       []orders.Status
	orders         map[uuid.UUID]orders.Order
	keys           map[[2]uuid.UUID]uuid.UUID // (customer, idempotency key) -> order
	clashes        int                        // how many next codes count as taken
	inserts        int
	locked         []uuid.UUID
	activeDays     map[clock.Date]int
	activeStatuses []orders.Status
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{orders: map[uuid.UUID]orders.Order{}, keys: map[[2]uuid.UUID]uuid.UUID{}}
}

func (f *fakeRepo) BatchCutoffs(_ context.Context, _ uuid.UUID, _, _ clock.Date, statuses []orders.Status) (map[clock.Date]time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = statuses
	return f.cutoffs, nil
}

func (f *fakeRepo) LockCustomer(_ context.Context, customerID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.locked = append(f.locked, customerID)
	return nil
}

func (f *fakeRepo) FindByIdempotencyKey(_ context.Context, _, customerID, key uuid.UUID) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.keys[[2]uuid.UUID{customerID, key}]; ok {
		return id, nil
	}
	return uuid.Nil, apperr.ErrNotFound
}

func (f *fakeRepo) CountByStatus(_ context.Context, _, customerID uuid.UUID, status orders.Status) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, o := range f.orders {
		if o.CustomerID == customerID && o.Status == status {
			n++
		}
	}
	return n, nil
}

func (f *fakeRepo) Insert(_ context.Context, _ uuid.UUID, o orders.Order, key uuid.UUID, at time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.clashes > 0 {
		f.clashes--
		return false, nil
	}
	o.CreatedAt, o.TermsAcceptedAt = at, at
	f.orders[o.ID] = o
	f.keys[[2]uuid.UUID{o.CustomerID, key}] = o.ID
	f.inserts++
	return true, nil
}

func (f *fakeRepo) Get(_ context.Context, _, id uuid.UUID) (orders.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.orders[id]
	if !ok {
		return orders.Order{}, apperr.ErrNotFound
	}
	return o, nil
}

func (f *fakeRepo) FindCustomerOrder(_ context.Context, _, customerID uuid.UUID, code string) (orders.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.orders {
		if o.CustomerID == customerID && o.Code == code {
			return o, nil
		}
	}
	return orders.Order{}, apperr.ErrNotFound
}

func (f *fakeRepo) ListCustomerOrders(_ context.Context, _, customerID uuid.UUID, _ int32) ([]orders.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []orders.Summary{}
	for _, o := range f.orders {
		if o.CustomerID == customerID {
			out = append(out, orders.Summary{ID: o.ID, Code: o.Code, Status: o.Status, TotalIDR: o.TotalIDR})
		}
	}
	return out, nil
}

func (f *fakeRepo) ActiveOrderDays(_ context.Context, _ uuid.UUID, _, _ clock.Date, statuses []orders.Status) (map[clock.Date]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activeStatuses = statuses
	return f.activeDays, nil
}

// fakeCustomers is identity for a caller signed in with a phone number.
type fakeCustomers struct {
	customer identity.Customer
	err      error // what EnsureCustomer returns instead
	anon     bool  // Principal: nobody signed in
}

func (f *fakeCustomers) Principal(context.Context) (identity.Principal, error) {
	if f.anon {
		return identity.Principal{}, identity.ErrUnauthenticated
	}
	p := identity.Principal{AuthUserID: uuid.New(), TenantID: tenant}
	if f.customer.ID != uuid.Nil {
		p.CustomerID = &f.customer.ID
	}
	return p, nil
}

func (f *fakeCustomers) EnsureCustomer(_ context.Context, in identity.CustomerInput, _ time.Time) (identity.Customer, error) {
	if f.err != nil {
		return identity.Customer{}, f.err
	}
	if f.customer.ID == uuid.Nil {
		f.customer.ID = uuid.New()
	}
	f.customer.Name, f.customer.Email = in.Name, in.Email
	return f.customer, nil
}

// fakeLedger keeps payments in memory.
type fakeLedger struct {
	mu       sync.Mutex
	payments []payments.Payment
}

func (f *fakeLedger) AddPending(_ context.Context, _ uuid.UUID, p payments.Payment, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p.State, p.CreatedAt = payments.StatePending, at
	f.payments = append(f.payments, p)
	return nil
}

func (f *fakeLedger) AttachInvoice(_ context.Context, _, paymentID uuid.UUID, inv payments.Invoice, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.payments {
		if f.payments[i].ID == paymentID {
			f.payments[i].ExternalID, f.payments[i].CheckoutURL = inv.ExternalID, inv.URL
			return nil
		}
	}
	return apperr.ErrNotFound
}

func (f *fakeLedger) OrderPayments(_ context.Context, _, orderID uuid.UUID) ([]payments.Payment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []payments.Payment
	for _, p := range f.payments {
		if p.OrderID == orderID {
			out = append(out, p)
		}
	}
	return out, nil
}

// fakeProvider makes invoices, or fails while broken.
type fakeProvider struct {
	mu       sync.Mutex
	broken   bool
	requests []payments.InvoiceRequest
}

func (f *fakeProvider) Name() string { return "dev" }

func (f *fakeProvider) CreateInvoice(_ context.Context, r payments.InvoiceRequest) (payments.Invoice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r)
	if f.broken {
		return payments.Invoice{}, errors.New("provider unreachable")
	}
	return payments.Invoice{ExternalID: "inv-" + r.PaymentID.String(), URL: "https://pay.test/" + r.PaymentID.String()}, nil
}

func (f *fakeProvider) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// fakeTx runs the unit of work directly; rollback is platform/db's to test.
type fakeTx struct{}

func (fakeTx) Tx(ctx context.Context, fn func(ctx context.Context) error) error { return fn(ctx) }
