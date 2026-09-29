package orders_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/orders"
	"github.com/Zefanrakh/kue-preorder/internal/payments"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/audit"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
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
	lockedCodes    []string
	staffFilter    orders.StaffFilter
	audits         []audit.Entry
	events         []outbox.Event
	lockErr        map[uuid.UUID]error // LockByID fails for these orders
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

func (f *fakeRepo) byCode(code string) (orders.Order, error) {
	for _, o := range f.orders {
		if o.Code == code {
			return o, nil
		}
	}
	return orders.Order{}, apperr.ErrNotFound
}

func (f *fakeRepo) LockByCode(_ context.Context, _ uuid.UUID, code string) (orders.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lockedCodes = append(f.lockedCodes, code)
	return f.byCode(code)
}

func (f *fakeRepo) GetByCode(_ context.Context, _ uuid.UUID, code string) (orders.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byCode(code)
}

func (f *fakeRepo) SetStatus(_ context.Context, _, id uuid.UUID, s orders.Status, p payments.Status, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	o := f.orders[id]
	o.Status, o.Payment = s, p
	f.orders[id] = o
	return nil
}

func (f *fakeRepo) ListStaff(_ context.Context, _ uuid.UUID, filter orders.StaffFilter, _ int32) ([]orders.StaffSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.staffFilter = filter
	out := []orders.StaffSummary{}
	for _, o := range f.orders {
		out = append(out, orders.StaffSummary{Summary: orders.Summary{ID: o.ID, Code: o.Code, Status: o.Status}, CustomerPhone: o.CustomerPhone})
	}
	return out, nil
}

func (f *fakeRepo) Audit(_ context.Context, e audit.Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audits = append(f.audits, e)
	return nil
}

func (f *fakeRepo) Publish(_ context.Context, e outbox.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
	return nil
}

func (f *fakeRepo) LockByID(_ context.Context, _, id uuid.UUID) (orders.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.lockErr[id]; err != nil {
		return orders.Order{}, err
	}
	o, ok := f.orders[id]
	if !ok {
		return orders.Order{}, apperr.ErrNotFound
	}
	return o, nil
}

func (f *fakeRepo) PastDPDue(_ context.Context, _ uuid.UUID, s orders.Status, p payments.Status, before time.Time, limit int32) ([]uuid.UUID, error) {
	return f.pastDue(s, p, before, limit, func(o orders.Order) time.Time { return o.DPDueAt })
}

func (f *fakeRepo) PastBalanceDue(_ context.Context, _ uuid.UUID, s orders.Status, p payments.Status, before time.Time, limit int32) ([]uuid.UUID, error) {
	return f.pastDue(s, p, before, limit, func(o orders.Order) time.Time { return o.BalanceDueAt })
}

func (f *fakeRepo) pastDue(s orders.Status, p payments.Status, before time.Time, limit int32, due func(orders.Order) time.Time) ([]uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var hits []orders.Order
	for _, o := range f.orders {
		if o.Status == s && o.Payment == p && !due(o).After(before) {
			hits = append(hits, o)
		}
	}
	slices.SortFunc(hits, func(a, b orders.Order) int {
		if c := due(a).Compare(due(b)); c != 0 {
			return c
		}
		return slices.Compare(a.ID[:], b.ID[:])
	})
	var ids []uuid.UUID
	for _, o := range hits {
		if len(ids) < int(limit) {
			ids = append(ids, o.ID)
		}
	}
	return ids, nil
}

func (f *fakeRepo) CommittedItems(_ context.Context, _ uuid.UUID, date clock.Date, statuses []orders.Status, pays []payments.Status) ([]orders.CommittedItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sums := map[uuid.UUID]int64{}
	for _, o := range f.orders {
		if o.ProductionDate != date || !slices.Contains(statuses, o.Status) || !slices.Contains(pays, o.Payment) {
			continue
		}
		for _, it := range o.Items {
			sums[it.VariantID] += int64(it.Quantity)
		}
	}
	out := []orders.CommittedItem{}
	for id, q := range sums {
		out = append(out, orders.CommittedItem{VariantID: id, Quantity: q})
	}
	slices.SortFunc(out, func(a, b orders.CommittedItem) int { return slices.Compare(a.VariantID[:], b.VariantID[:]) })
	return out, nil
}

// eventTypes lists the types of the events published so far.
func (f *fakeRepo) eventTypes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.events {
		out = append(out, e.Type)
	}
	return out
}

// fakeCustomers is identity for a caller signed in with a phone number.
type fakeCustomers struct {
	customer identity.Customer
	err      error           // what EnsureCustomer returns instead
	anon     bool            // Principal: nobody signed in
	roles    []identity.Role // Principal: a staff member's roles
	user     uuid.UUID       // Principal: the auth user; random when nil
}

func (f *fakeCustomers) Principal(context.Context) (identity.Principal, error) {
	if f.anon {
		return identity.Principal{}, identity.ErrUnauthenticated
	}
	user := f.user
	if user == uuid.Nil {
		user = uuid.New()
	}
	p := identity.Principal{AuthUserID: user, TenantID: tenant, Roles: f.roles}
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

// fakeLedger keeps the payment ledger in memory, behind the real
// payments.Ledger.
type fakeLedger struct {
	mu       sync.Mutex
	payments []payments.Payment
}

func (f *fakeLedger) AddPayment(_ context.Context, _ uuid.UUID, p payments.Payment, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p.CreatedAt = at
	f.payments = append(f.payments, p)
	return nil
}

func (f *fakeLedger) SetInvoice(_ context.Context, _, paymentID uuid.UUID, inv payments.Invoice, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.payments {
		if f.payments[i].ID == paymentID && f.payments[i].State == payments.StatePending {
			f.payments[i].ExternalID, f.payments[i].CheckoutURL = inv.ExternalID, inv.URL
			return nil
		}
	}
	return apperr.ErrNotFound
}

func (f *fakeLedger) ExpirePending(_ context.Context, _, orderID uuid.UUID, _ time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for i := range f.payments {
		if p := &f.payments[i]; p.OrderID == orderID && p.State == payments.StatePending {
			p.State = payments.StateExpired
			n++
		}
	}
	return n, nil
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
