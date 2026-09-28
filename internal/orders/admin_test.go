package orders_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"pgregory.net/rapid"

	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/orders"
	"github.com/Zefanrakh/kue-preorder/internal/payments"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
)

// office is the shop's CMS: the checkout's shop, plus a staff member acting
// through orders.Admin.
type office struct {
	*shop
	staff *fakeCustomers
	admin *orders.Admin
	auto  *orders.Automation
	order orders.Order // placed for Wednesday: 99,000, a DP of 49,500
}

func newOffice(t *testing.T, roles ...identity.Role) *office {
	t.Helper()
	s := newShop()
	o := &office{shop: s, staff: &fakeCustomers{roles: roles, user: uuid.New()}}
	o.admin = orders.NewAdmin(orders.AdminDeps{
		Repo: s.repo, Ledger: payments.NewLedger(s.ledger), Principals: o.staff, Tx: fakeTx{}, Clock: s.clock,
	})
	o.auto = orders.NewAutomation(orders.AutomationDeps{
		Repo: s.repo, Ledger: payments.NewLedger(s.ledger), Policies: s.policies, Provider: s.provider, Tx: fakeTx{}, Clock: s.clock,
	})
	o.order = s.place(t, s.placeRequest(wib(7, 9, 0), line(s.chocolate, 6), line(s.cheese, 6)))
	return o
}

func (o *office) pay(t *testing.T, amount int64) orders.Order {
	t.Helper()
	got, err := o.admin.RecordPayment(t.Context(), o.order.Code, orders.ManualPayment{AmountIDR: amount, Reference: "BCA 0412", Note: "Transfer langsung"})
	if err != nil {
		t.Fatalf("RecordPayment(%d) error = %v", amount, err)
	}
	return got
}

func (o *office) advance(t *testing.T, to orders.Status) orders.Order {
	t.Helper()
	got, err := o.admin.Advance(t.Context(), o.order.Code, to)
	if err != nil {
		t.Fatalf("Advance(%s) error = %v", to, err)
	}
	return got
}

// written counts what staff actions stored: payments, audit entries, events.
func (o *office) written() (int, int, int) {
	return len(o.ledger.payments), len(o.repo.audits), len(o.repo.events)
}

func precondition(err error) string {
	var p *apperr.PreconditionError
	if errors.As(err, &p) {
		return p.Reason
	}
	return ""
}

func fieldError(err error, field string) string {
	var v *apperr.ValidationError
	if errors.As(err, &v) {
		return v.Fields[field]
	}
	return ""
}

func TestAdmin_Roles(t *testing.T) {
	actions := []struct {
		name    string
		owners  bool                          // owner only
		prepare func(t *testing.T, o *office) // done as the owner first
		call    func(t *testing.T, o *office) error
	}{
		{"List", false, nil, func(t *testing.T, o *office) error {
			_, err := o.admin.List(t.Context(), orders.StaffFilter{From: date(1), To: date(31)})
			return err
		}},
		{"Get", false, nil, func(t *testing.T, o *office) error {
			_, err := o.admin.Get(t.Context(), o.order.Code)
			return err
		}},
		{"Advance", false, func(t *testing.T, o *office) { o.pay(t, 99000) }, func(t *testing.T, o *office) error {
			_, err := o.admin.Advance(t.Context(), o.order.Code, orders.InProduction)
			return err
		}},
		{"RecordPayment", true, nil, func(t *testing.T, o *office) error {
			_, err := o.admin.RecordPayment(t.Context(), o.order.Code, orders.ManualPayment{AmountIDR: 49500, Reference: "BCA 1", Note: "DP"})
			return err
		}},
		{"Cancel", true, nil, func(t *testing.T, o *office) error {
			_, err := o.admin.Cancel(t.Context(), o.order.Code, orders.CancelRequest{Mode: orders.CancelUnpaid, Reason: "Pelanggan batal"})
			return err
		}},
	}
	callers := []struct {
		name  string
		roles []identity.Role
		anon  bool
	}{
		{"owner", []identity.Role{identity.RoleOwner}, false},
		{"kitchen", []identity.Role{identity.RoleKitchen}, false},
		{"customer", nil, false},
		{"nobody", nil, true},
	}
	for _, a := range actions {
		for _, c := range callers {
			t.Run(a.name+"/"+c.name, func(t *testing.T) {
				o := newOffice(t, identity.RoleOwner)
				if a.prepare != nil {
					a.prepare(t, o)
				}
				o.staff.roles, o.staff.anon = c.roles, c.anon

				err := a.call(t, o)

				var want error
				switch {
				case c.anon:
					want = identity.ErrUnauthenticated
				case c.roles == nil || (a.owners && c.name == "kitchen"):
					want = apperr.ErrForbidden
				}
				if (want == nil && err != nil) || (want != nil && !errors.Is(err, want)) {
					t.Errorf("error = %v, want %v", err, want)
				}
			})
		}
	}
}

// A DP transferred straight to the shop confirms the order in one go: the
// payment with its proof, the statuses, the lapsed invoice, the audit entry,
// and the events.
func TestAdmin_RecordPayment_DPConfirms(t *testing.T) {
	o := newOffice(t, identity.RoleOwner)

	got := o.pay(t, 49500)

	if got.Status != orders.Confirmed || got.Payment != payments.DPPaid {
		t.Errorf("order = %s/%s, want confirmed with the DP paid", got.Status, got.Payment)
	}
	if len(got.Payments) != 2 || got.Payments[0].State != payments.StateExpired {
		t.Fatalf("payments = %+v, want the invoice expired and the manual DP", got.Payments)
	}
	p := got.Payments[1]
	if p.Kind != payments.KindDP || p.AmountIDR != 49500 || p.State != payments.StatePaid || p.Provider != payments.ProviderManual ||
		p.FeeIDR != 0 || p.PaidAt == nil || !p.PaidAt.Equal(monday10) || p.Method != "" {
		t.Errorf("payment = %+v, want a paid manual DP of 49,500 without a fee", p)
	}
	if p.Manual == nil || p.Manual.Reference != "BCA 0412" || p.Manual.Note != "Transfer langsung" || p.Manual.RecordedBy != o.staff.user {
		t.Errorf("proof = %+v, want the reference, the note, and who recorded it", p.Manual)
	}
	if len(o.repo.audits) != 1 {
		t.Fatalf("audit entries = %+v, want one", o.repo.audits)
	}
	if a := o.repo.audits[0]; a.Action != "orders.payment.recorded_manually" || a.ActorID != o.staff.user || a.Reason != "Transfer langsung" ||
		a.EntityID != o.order.ID || a.TenantID != tenant || !a.At.Equal(monday10) {
		t.Errorf("audit = %+v", a)
	}
	if got := o.repo.eventTypes(); !slices.Equal(got, []string{"order.payment_received", "order.confirmed"}) {
		t.Errorf("events = %v, want the payment and the confirmation", got)
	}
	if !slices.Equal(o.repo.lockedCodes, []string{o.order.Code}) {
		t.Errorf("locked %v, want the order's row", o.repo.lockedCodes)
	}
}

func TestAdmin_RecordPayment_Kinds(t *testing.T) {
	o := newOffice(t, identity.RoleOwner)
	if got := o.pay(t, 99000); got.Status != orders.Confirmed || got.Payment != payments.PaidInFull || got.Payments[1].Kind != payments.KindFull {
		t.Errorf("paying everything at once: %s/%s, %+v", got.Status, got.Payment, got.Payments)
	}

	o = newOffice(t, identity.RoleOwner)
	o.pay(t, 60000) // more than the DP
	got := o.pay(t, 20000)
	if got.Status != orders.Confirmed || got.Payment != payments.DPPaid || got.Payments[2].Kind != payments.KindBalance {
		t.Errorf("part of the balance: %s/%s, %+v", got.Status, got.Payment, got.Payments)
	}
	got = o.pay(t, 19000)
	if got.Payment != payments.PaidInFull || payments.Paid(got.Payments) != 99000 {
		t.Errorf("the rest: %s, paid %d, want paid in full", got.Payment, payments.Paid(got.Payments))
	}
	if n := slices.Index(o.repo.eventTypes(), "order.confirmed"); n != 1 || len(o.repo.events) != 4 {
		t.Errorf("events = %v, want the order confirmed once", o.repo.eventTypes())
	}
}

func TestAdmin_RecordPayment_Refuses(t *testing.T) {
	good := orders.ManualPayment{AmountIDR: 49500, Reference: "BCA 1", Note: "DP"}
	tests := []struct {
		name  string
		setup func(t *testing.T, o *office)
		in    func(p *orders.ManualPayment)
		code  string
		want  func(error) bool
	}{
		{"less than the DP", nil, func(p *orders.ManualPayment) { p.AmountIDR = 49499 },
			"", func(err error) bool { return fieldError(err, "amount_idr") != "" }},
		{"more than owed", nil, func(p *orders.ManualPayment) { p.AmountIDR = 99001 },
			"", func(err error) bool { return fieldError(err, "amount_idr") == "Melebihi sisa tagihan Rp99.000." }},
		{"no amount", nil, func(p *orders.ManualPayment) { p.AmountIDR = 0 },
			"", func(err error) bool { return fieldError(err, "amount_idr") != "" }},
		{"no reference", nil, func(p *orders.ManualPayment) { p.Reference = "  " },
			"", func(err error) bool { return fieldError(err, "reference") != "" }},
		{"no note", nil, func(p *orders.ManualPayment) { p.Note = "" },
			"", func(err error) bool { return fieldError(err, "note") != "" }},
		{"paid in full", func(t *testing.T, o *office) { o.pay(t, 99000) }, nil,
			"", func(err error) bool { return precondition(err) == "nothing_owed" }},
		{"forfeited", func(t *testing.T, o *office) {
			o.pay(t, 49500)
			o.clock.Set(wib(6, 19, 30))
			if _, err := o.admin.Cancel(t.Context(), o.order.Code, orders.CancelRequest{Mode: orders.CancelForfeit, Reason: "Tidak dilunasi"}); err != nil {
				t.Fatal(err)
			}
		}, nil, "", func(err error) bool { return precondition(err) == "payments_closed" }},
		{"expired", func(t *testing.T, o *office) {
			if _, err := o.admin.Cancel(t.Context(), o.order.Code, orders.CancelRequest{Mode: orders.CancelUnpaid, Reason: "Batal"}); err != nil {
				t.Fatal(err)
			}
		}, nil, "", func(err error) bool { return precondition(err) == "order_not_open" }},
		{"unknown order", nil, nil, "ZZZZZZ", func(err error) bool { return errors.Is(err, apperr.ErrNotFound) }},
		{"malformed code", nil, nil, "K7", func(err error) bool { return errors.Is(err, apperr.ErrNotFound) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := newOffice(t, identity.RoleOwner)
			if tt.setup != nil {
				tt.setup(t, o)
			}
			in, code := good, o.order.Code
			if tt.in != nil {
				tt.in(&in)
			}
			if tt.code != "" {
				code = tt.code
			}
			np, na, ne := o.written()

			_, err := o.admin.RecordPayment(t.Context(), code, in)

			if !tt.want(err) {
				t.Errorf("RecordPayment() error = %v", err)
			}
			if p, a, e := o.written(); p != np || a != na || e != ne {
				t.Errorf("stored %d payments, %d audits, %d events after a refusal; want none", p-np, a-na, e-ne)
			}
		})
	}
}

// Production needs the order paid in full at every step (§13), and a
// second tap on the same step changes nothing.
func TestAdmin_Advance(t *testing.T) {
	o := newOffice(t, identity.RoleOwner)
	o.pay(t, 49500)

	if _, err := o.admin.Advance(t.Context(), o.order.Code, orders.InProduction); precondition(err) != "not_paid_in_full" {
		t.Fatalf("Advance() with only the DP: error = %v, want not_paid_in_full", err)
	}

	o.pay(t, 49500)
	o.staff.roles = []identity.Role{identity.RoleKitchen}
	o.advance(t, orders.InProduction)
	o.advance(t, orders.Ready)
	events := len(o.repo.events)
	if got := o.advance(t, orders.Ready); got.Status != orders.Ready || len(o.repo.events) != events {
		t.Errorf("second tap: %s with %d new events, want ready and none", got.Status, len(o.repo.events)-events)
	}
	if _, err := o.admin.Advance(t.Context(), o.order.Code, orders.InProduction); precondition(err) != "illegal_transition" {
		t.Errorf("Advance() backwards: error = %v, want illegal_transition", err)
	}
	if got := o.advance(t, orders.Completed); got.Status != orders.Completed {
		t.Errorf("status = %s, want completed", got.Status)
	}
	last := o.repo.events[len(o.repo.events)-1]
	if p, _ := last.Payload.(map[string]any); last.Type != "order.status_changed" || p["from"] != orders.Ready || p["to"] != orders.Completed ||
		p["by"] != o.staff.user || p["code"] != o.order.Code || p["production_date"] != "2026-10-07" {
		t.Errorf("event = %+v", last)
	}
	for _, to := range []orders.Status{orders.Confirmed, orders.Cancelled, orders.OutForDelivery, "baked"} {
		if _, err := o.admin.Advance(t.Context(), o.order.Code, to); fieldError(err, "status") == "" {
			t.Errorf("Advance(%s) error = %v, want a field error: not a production step", to, err)
		}
	}
}

func TestAdmin_Cancel_Unpaid(t *testing.T) {
	o := newOffice(t, identity.RoleOwner)

	got, err := o.admin.Cancel(t.Context(), o.order.Code, orders.CancelRequest{Mode: orders.CancelUnpaid, Reason: " Pelanggan batal lewat WA "})

	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if got.Status != orders.Expired || got.Payment != payments.Unpaid || got.Payments[0].State != payments.StateExpired {
		t.Errorf("order = %s/%s, invoice %s; want expired and the invoice with it", got.Status, got.Payment, got.Payments[0].State)
	}
	if a := o.repo.audits; len(a) != 1 || a[0].Action != "orders.order.cancelled" || a[0].Reason != "Pelanggan batal lewat WA" {
		t.Errorf("audit = %+v", a)
	}
	if got := o.repo.eventTypes(); !slices.Equal(got, []string{"order.expired"}) {
		t.Errorf("events = %v", got)
	}
}

// The DP is forfeited only once the balance deadline has passed, as the
// terms say (§14); nothing goes back to the customer.
func TestAdmin_Cancel_Forfeit(t *testing.T) {
	o := newOffice(t, identity.RoleOwner)
	o.pay(t, 49500)
	forfeit := orders.CancelRequest{Mode: orders.CancelForfeit, Reason: "Tidak dilunasi sampai tenggat"}

	o.clock.Set(wib(6, 19, 29))
	if _, err := o.admin.Cancel(t.Context(), o.order.Code, forfeit); precondition(err) != "balance_not_due" {
		t.Fatalf("Cancel() a minute early: error = %v, want balance_not_due", err)
	}
	o.clock.Set(wib(6, 19, 30))
	got, err := o.admin.Cancel(t.Context(), o.order.Code, forfeit)

	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if got.Status != orders.Cancelled || got.Payment != payments.Forfeited || len(got.Payments) != 2 || payments.Paid(got.Payments) != 49500 {
		t.Errorf("order = %s/%s, payments %+v; want cancelled, the DP kept", got.Status, got.Payment, got.Payments)
	}
	if got := o.repo.eventTypes(); got[len(got)-1] != "order.cancelled" {
		t.Errorf("events = %v", got)
	}
}

// A refund returns everything paid, recorded as a negative manual payment
// with the reference of the transfer back.
func TestAdmin_Cancel_Refund(t *testing.T) {
	o := newOffice(t, identity.RoleOwner)
	o.pay(t, 99000)

	got, err := o.admin.Cancel(t.Context(), o.order.Code, orders.CancelRequest{
		Mode: orders.CancelRefund, Reason: "Oven rusak", RefundReference: "BCA balik 77",
	})

	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if got.Status != orders.Cancelled || got.Payment != payments.Refunded || payments.Paid(got.Payments) != 0 {
		t.Errorf("order = %s/%s, paid %d; want cancelled, refunded, nothing kept", got.Status, got.Payment, payments.Paid(got.Payments))
	}
	r := got.Payments[len(got.Payments)-1]
	if r.Kind != payments.KindRefund || r.AmountIDR != -99000 || r.State != payments.StatePaid || r.Manual == nil ||
		r.Manual.Reference != "BCA balik 77" || r.Manual.Note != "Oven rusak" {
		t.Errorf("refund = %+v", r)
	}
	a := o.repo.audits[len(o.repo.audits)-1]
	if after, _ := a.After.(map[string]any); a.Reason != "Oven rusak" || after["refunded_idr"] != int64(99000) || after["mode"] != orders.CancelRefund {
		t.Errorf("audit = %+v", a)
	}
}

func TestAdmin_Cancel_Refuses(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, o *office)
		in    orders.CancelRequest
		want  func(error) bool
	}{
		{"no mode", nil, orders.CancelRequest{Reason: "x"},
			func(err error) bool { return fieldError(err, "mode") != "" }},
		{"no reason", nil, orders.CancelRequest{Mode: orders.CancelUnpaid},
			func(err error) bool { return fieldError(err, "reason") != "" }},
		{"refund without a reference", func(t *testing.T, o *office) { o.pay(t, 49500) }, orders.CancelRequest{Mode: orders.CancelRefund, Reason: "x"},
			func(err error) bool { return fieldError(err, "refund_reference") != "" }},
		{"unpaid, but paid", func(t *testing.T, o *office) { o.pay(t, 49500) }, orders.CancelRequest{Mode: orders.CancelUnpaid, Reason: "x"},
			func(err error) bool { return precondition(err) == "already_paid" }},
		{"forfeit, but paid in full", func(t *testing.T, o *office) { o.pay(t, 99000); o.clock.Set(wib(7, 0, 0)) },
			orders.CancelRequest{Mode: orders.CancelForfeit, Reason: "x"},
			func(err error) bool { return precondition(err) == "not_forfeitable" }},
		{"forfeit, but unpaid", nil, orders.CancelRequest{Mode: orders.CancelForfeit, Reason: "x"},
			func(err error) bool { return precondition(err) == "not_forfeitable" }},
		{"refund, but unpaid", nil, orders.CancelRequest{Mode: orders.CancelRefund, Reason: "x", RefundReference: "y"},
			func(err error) bool { return precondition(err) == "nothing_to_refund" }},
		{"refund in production", func(t *testing.T, o *office) { o.pay(t, 99000); o.advance(t, orders.InProduction) },
			orders.CancelRequest{Mode: orders.CancelRefund, Reason: "x", RefundReference: "y"},
			func(err error) bool { return precondition(err) == "illegal_transition" }},
		{"twice", func(t *testing.T, o *office) {
			if _, err := o.admin.Cancel(t.Context(), o.order.Code, orders.CancelRequest{Mode: orders.CancelUnpaid, Reason: "x"}); err != nil {
				t.Fatal(err)
			}
		}, orders.CancelRequest{Mode: orders.CancelUnpaid, Reason: "x"},
			func(err error) bool { return precondition(err) == "illegal_transition" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := newOffice(t, identity.RoleOwner)
			if tt.setup != nil {
				tt.setup(t, o)
			}
			before, _ := o.admin.Get(t.Context(), o.order.Code)
			np, na, ne := o.written()

			_, err := o.admin.Cancel(t.Context(), o.order.Code, tt.in)

			if !tt.want(err) {
				t.Errorf("Cancel() error = %v", err)
			}
			after, _ := o.admin.Get(t.Context(), o.order.Code)
			if p, a, e := o.written(); p != np || a != na || e != ne || after.Status != before.Status || after.Payment != before.Payment {
				t.Errorf("a refusal changed the order: %d payments, %d audits, %d events, %s/%s", p-np, a-na, e-ne, after.Status, after.Payment)
			}
		})
	}
}

func TestAdmin_List(t *testing.T) {
	o := newOffice(t, identity.RoleKitchen)
	list := func(f orders.StaffFilter) error {
		_, err := o.admin.List(t.Context(), f)
		return err
	}

	if err := list(orders.StaffFilter{From: date(1), To: date(31), Query: " 0812-3456-7890 ", Statuses: []orders.Status{orders.Confirmed}}); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if f := o.repo.staffFilter; f.Query != "6281234567890" || f.From != date(1) || f.To != date(31) || !slices.Equal(f.Statuses, []orders.Status{orders.Confirmed}) {
		t.Errorf("filter = %+v, want the phone number as stored", f)
	}
	for q, want := range map[string]string{"k7m3qx": "k7m3qx", "+62 812 3456": "628123456", "Sari": "Sari", "081": "081", "234567": "234567"} {
		if err := list(orders.StaffFilter{From: date(1), To: date(1), Query: q}); err != nil || o.repo.staffFilter.Query != want {
			t.Errorf("List(%q) searched %q (%v), want %q", q, o.repo.staffFilter.Query, err, want)
		}
	}

	for _, tt := range []struct {
		f     orders.StaffFilter
		field string
	}{
		{orders.StaffFilter{From: date(2), To: date(1)}, "to_date"},
		{orders.StaffFilter{From: date(1), To: date(1).AddDays(93)}, "to_date"},
		{orders.StaffFilter{From: date(1), To: date(1), Statuses: []orders.Status{"baked"}}, "statuses"},
	} {
		if err := list(tt.f); fieldError(err, tt.field) == "" {
			t.Errorf("List(%+v) error = %v, want a field error on %s", tt.f, err, tt.field)
		}
	}
}

// Whatever staff and the worker do, in any order and at any time, an order
// never reaches production or the customer unpaid, never holds more than its
// total, and its payment status always agrees with its ledger (§13, §14).
func TestAdmin_Invariants(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		o := newOffice(t, identity.RoleOwner)
		for range rapid.IntRange(1, 12).Draw(rt, "steps") {
			switch rapid.IntRange(0, 4).Draw(rt, "action") {
			case 0:
				amount := rapid.Int64Range(1, 120000).Draw(rt, "amount")
				_, _ = o.admin.RecordPayment(t.Context(), o.order.Code, orders.ManualPayment{AmountIDR: amount, Reference: "r", Note: "n"})
			case 1:
				to := rapid.SampledFrom([]orders.Status{orders.InProduction, orders.Ready, orders.Completed}).Draw(rt, "to")
				_, _ = o.admin.Advance(t.Context(), o.order.Code, to)
			case 2:
				mode := rapid.SampledFrom([]orders.CancelMode{orders.CancelUnpaid, orders.CancelForfeit, orders.CancelRefund}).Draw(rt, "mode")
				_, _ = o.admin.Cancel(t.Context(), o.order.Code, orders.CancelRequest{Mode: mode, Reason: "r", RefundReference: "r"})
			case 3:
				o.clock.Advance(rapid.SampledFrom([]time.Duration{time.Hour, 12 * time.Hour, 36 * time.Hour}).Draw(rt, "wait"))
			case 4: // the worker's turn
				_, _ = o.auto.ExpireUnpaid(t.Context(), tenant)
				_, _ = o.auto.ForfeitUnpaid(t.Context(), tenant)
				_ = o.auto.EnsureBalanceInvoice(t.Context(), tenant, o.order.ID)
			}

			got, err := o.admin.Get(t.Context(), o.order.Code)
			if err != nil {
				rt.Fatalf("Get() error = %v", err)
			}
			paid := payments.Paid(got.Payments)
			switch got.Status {
			case orders.InProduction, orders.Ready, orders.OutForDelivery, orders.Completed:
				if got.Payment != payments.PaidInFull || paid != got.TotalIDR {
					rt.Fatalf("%s with %s and %d of %d paid", got.Status, got.Payment, paid, got.TotalIDR)
				}
			}
			if paid < 0 || paid > got.TotalIDR {
				rt.Fatalf("paid %d of %d", paid, got.TotalIDR)
			}
			if got.Payment == payments.Refunded && paid != 0 {
				rt.Fatalf("refunded but %d kept", paid)
			}
			if !got.Payment.Closed() {
				want, _ := payments.Settle(payments.Unpaid, payments.Amounts{TotalIDR: got.TotalIDR, DPRequiredIDR: got.DPRequiredIDR, PaidIDR: paid})
				if got.Payment != want {
					rt.Fatalf("payment status %s, ledger says %s (%d paid)", got.Payment, want, paid)
				}
			}
			if (got.Status == orders.Cancelled) != got.Payment.Closed() {
				rt.Fatalf("%s with payments %s", got.Status, got.Payment)
			}
		}
	})
}
