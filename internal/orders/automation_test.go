package orders_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/orders"
	"github.com/Zefanrakh/kue-preorder/internal/payments"
)

func (o *office) current(t *testing.T) orders.Order {
	t.Helper()
	got, err := o.admin.Get(t.Context(), o.order.Code)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (o *office) expire(t *testing.T) int {
	t.Helper()
	n, err := o.auto.ExpireUnpaid(t.Context(), tenant)
	if err != nil {
		t.Fatalf("ExpireUnpaid() error = %v", err)
	}
	return n
}

func (o *office) forfeit(t *testing.T) int {
	t.Helper()
	n, err := o.auto.ForfeitUnpaid(t.Context(), tenant)
	if err != nil {
		t.Fatalf("ForfeitUnpaid() error = %v", err)
	}
	return n
}

func (o *office) lastEvent() map[string]any {
	e := o.repo.events[len(o.repo.events)-1]
	p, _ := e.Payload.(map[string]any)
	p["type"] = e.Type
	return p
}

// The order's DP is due Monday 13.00; it expires half an hour later, not a
// minute sooner, and only once.
func TestAutomation_ExpiresUnpaidOrders(t *testing.T) {
	o := newOffice(t, identity.RoleOwner)

	o.clock.Set(wib(5, 13, 29))
	if n := o.expire(t); n != 0 || o.current(t).Status != orders.AwaitingDP {
		t.Fatalf("within the grace: %d expired, want none", n)
	}
	o.clock.Set(wib(5, 13, 30))
	if n := o.expire(t); n != 1 {
		t.Fatalf("after the grace: %d expired, want 1", n)
	}

	got := o.current(t)
	if got.Status != orders.Expired || got.Payment != payments.Unpaid || got.Payments[0].State != payments.StateExpired {
		t.Errorf("order = %s/%s, invoice %s; want expired with its invoice", got.Status, got.Payment, got.Payments[0].State)
	}
	if e := o.lastEvent(); e["type"] != "order.expired" || e["mode"] != "dp_overdue" || e["automatic"] != true || e["code"] != o.order.Code {
		t.Errorf("event = %v", e)
	}
	events := len(o.repo.events)
	if n := o.expire(t); n != 0 || len(o.repo.events) != events {
		t.Errorf("second sweep: %d expired, %d new events; want nothing", n, len(o.repo.events)-events)
	}
	if len(o.repo.audits) != 0 {
		t.Errorf("audits = %+v, want none: the worker acts for no person", o.repo.audits)
	}
}

func TestAutomation_DoesNotExpireAPaidOrder(t *testing.T) {
	o := newOffice(t, identity.RoleOwner)
	o.pay(t, 49500)
	o.clock.Set(wib(5, 23, 0))

	if n := o.expire(t); n != 0 || o.current(t).Status != orders.Confirmed {
		t.Errorf("%d expired, status %s; want the confirmed order left alone", n, o.current(t).Status)
	}
}

// The balance is due Tuesday 19.30: the DP is forfeited at 20.00, and an
// order paid in full is never touched.
func TestAutomation_ForfeitsUnpaidBalances(t *testing.T) {
	o := newOffice(t, identity.RoleOwner)
	o.pay(t, 49500)

	o.clock.Set(wib(6, 19, 59))
	if n := o.forfeit(t); n != 0 {
		t.Fatalf("within the grace: %d forfeited, want none", n)
	}
	o.clock.Set(wib(6, 20, 0))
	if n := o.forfeit(t); n != 1 {
		t.Fatalf("after the grace: %d forfeited, want 1", n)
	}

	got := o.current(t)
	if got.Status != orders.Cancelled || got.Payment != payments.Forfeited || payments.Paid(got.Payments) != 49500 {
		t.Errorf("order = %s/%s, paid %d; want cancelled with the DP kept", got.Status, got.Payment, payments.Paid(got.Payments))
	}
	if e := o.lastEvent(); e["type"] != "order.cancelled" || e["mode"] != "forfeit" || e["automatic"] != true || e["payment_status"] != payments.Forfeited {
		t.Errorf("event = %v", e)
	}
	if n := o.forfeit(t); n != 0 {
		t.Errorf("second sweep: %d forfeited, want none", n)
	}

	paid := newOffice(t, identity.RoleOwner)
	paid.pay(t, 99000)
	paid.clock.Set(wib(7, 0, 0))
	if n := paid.forfeit(t); n != 0 || paid.current(t).Payment != payments.PaidInFull {
		t.Errorf("paid in full: %d forfeited, payment %s; want it left alone", n, paid.current(t).Payment)
	}
}

// One order failing does not hold up the others; the failure is returned.
func TestAutomation_SweepGoesOnPastAFailure(t *testing.T) {
	o := newOffice(t, identity.RoleOwner)
	second := o.place(t, o.placeRequest(wib(7, 10, 0), line(o.chocolate, 2)))
	o.repo.lockErr = map[uuid.UUID]error{o.order.ID: errors.New("connection reset")}
	o.clock.Set(wib(5, 14, 0))

	n, err := o.auto.ExpireUnpaid(t.Context(), tenant)

	if n != 1 || err == nil || !strings.Contains(err.Error(), o.order.ID.String()) {
		t.Errorf("ExpireUnpaid() = %d, %v; want the other order expired and this one's failure", n, err)
	}
	if got, _ := o.admin.Get(t.Context(), second.Code); got.Status != orders.Expired {
		t.Errorf("second order = %s, want expired", got.Status)
	}
}

// balanceOffice has an order paid by bank transfer, its DP recorded by the
// owner: 99,000 with 49,500 still owed.
func balanceOffice(t *testing.T) *office {
	t.Helper()
	o := newOffice(t, identity.RoleOwner)
	req := o.placeRequest(wib(7, 11, 0), line(o.cheese, 6), line(o.chocolate, 6))
	req.Method = payments.MethodBankTransfer
	o.order = o.place(t, req)
	o.pay(t, 49500)
	return o
}

func balances(ps []payments.Payment) []payments.Payment {
	var out []payments.Payment
	for _, p := range ps {
		if p.Kind == payments.KindBalance && p.Provider != payments.ProviderManual {
			out = append(out, p)
		}
	}
	return out
}

func TestAutomation_BillsTheBalance(t *testing.T) {
	o := balanceOffice(t)
	calls := o.provider.calls()

	if err := o.auto.EnsureBalanceInvoice(t.Context(), tenant, o.order.ID); err != nil {
		t.Fatal(err)
	}

	bs := balances(o.current(t).Payments)
	if len(bs) != 1 {
		t.Fatalf("balance payments = %+v, want one", bs)
	}
	b := bs[0]
	if b.State != payments.StatePending || b.AmountIDR != 49500 || b.Method != payments.MethodBankTransfer || b.FeeIDR != 4440 ||
		!b.ExpiresAt.Equal(o.order.BalanceDueAt) || b.CheckoutURL == "" || b.Provider != "dev" {
		t.Errorf("balance = %+v, want 49,500 by bank transfer plus Rp4.440, open until the deadline, with a link", b)
	}
	if r := o.provider.requests[len(o.provider.requests)-1]; r.Description != "Pelunasan pesanan "+o.order.Code || r.FeeIDR != 4440 {
		t.Errorf("invoice request = %+v", r)
	}

	if err := o.auto.EnsureBalanceInvoice(t.Context(), tenant, o.order.ID); err != nil {
		t.Fatal(err)
	}
	if bs := balances(o.current(t).Payments); len(bs) != 1 || o.provider.calls() != calls+1 {
		t.Errorf("after a second run: %d balances, %d invoice requests; want one of each", len(bs), o.provider.calls()-calls)
	}
}

// Part of the balance transferred to the shop replaces the open invoice with
// one for the rest.
func TestAutomation_BillsWhatIsLeft(t *testing.T) {
	o := balanceOffice(t)
	if err := o.auto.EnsureBalanceInvoice(t.Context(), tenant, o.order.ID); err != nil {
		t.Fatal(err)
	}
	o.pay(t, 20000)

	if err := o.auto.EnsureBalanceInvoice(t.Context(), tenant, o.order.ID); err != nil {
		t.Fatal(err)
	}

	var open []payments.Payment
	for _, p := range balances(o.current(t).Payments) {
		if p.State == payments.StatePending {
			open = append(open, p)
		}
	}
	if len(open) != 1 || open[0].AmountIDR != 29500 {
		t.Errorf("open balances = %+v, want one for the remaining 29,500", open)
	}
}

// A method the owner has switched off since checkout falls back to QRIS.
func TestAutomation_BalanceMethodFallsBackToQRIS(t *testing.T) {
	o := balanceOffice(t)
	va := o.policies.rules[payments.MethodBankTransfer]
	va.Enabled = false
	o.policies.rules[payments.MethodBankTransfer] = va

	if err := o.auto.EnsureBalanceInvoice(t.Context(), tenant, o.order.ID); err != nil {
		t.Fatal(err)
	}

	if bs := balances(o.current(t).Payments); len(bs) != 1 || bs[0].Method != payments.MethodQRIS || bs[0].FeeIDR != 0 {
		t.Errorf("balance = %+v, want QRIS without a fee", bs)
	}
}

func TestAutomation_BillsNothingWhenNothingIsOwed(t *testing.T) {
	tests := map[string]func(t *testing.T, o *office){
		"paid in full":      func(t *testing.T, o *office) { o.pay(t, 49500) },
		"past the deadline": func(_ *testing.T, o *office) { o.clock.Set(o.order.BalanceDueAt) },
		"cancelled": func(t *testing.T, o *office) {
			if _, err := o.admin.Cancel(t.Context(), o.order.Code, orders.CancelRequest{Mode: orders.CancelRefund, Reason: "x", RefundReference: "y"}); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			o := balanceOffice(t)
			setup(t, o)

			if err := o.auto.EnsureBalanceInvoice(t.Context(), tenant, o.order.ID); err != nil {
				t.Fatal(err)
			}

			if bs := balances(o.current(t).Payments); len(bs) != 0 {
				t.Errorf("balances = %+v, want none", bs)
			}
		})
	}

	o := newOffice(t, identity.RoleOwner) // still waiting for its DP
	if err := o.auto.EnsureBalanceInvoice(t.Context(), tenant, o.order.ID); err != nil || len(balances(o.current(t).Payments)) != 0 {
		t.Errorf("awaiting the DP: %v, balances %+v; want none", err, balances(o.current(t).Payments))
	}
}

// A provider failure is returned, so the job retries; the retry asks for
// the same payment's invoice again and bills nothing twice.
func TestAutomation_RetriesTheBalanceInvoice(t *testing.T) {
	o := balanceOffice(t)
	o.provider.broken = true

	if err := o.auto.EnsureBalanceInvoice(t.Context(), tenant, o.order.ID); err == nil {
		t.Fatal("EnsureBalanceInvoice() succeeded with the provider down, want an error to retry")
	}
	o.provider.broken = false
	if err := o.auto.EnsureBalanceInvoice(t.Context(), tenant, o.order.ID); err != nil {
		t.Fatal(err)
	}

	bs := balances(o.current(t).Payments)
	if len(bs) != 1 || bs[0].CheckoutURL == "" {
		t.Fatalf("balances = %+v, want one with its link", bs)
	}
	var asked []string
	for _, r := range o.provider.requests {
		if r.PaymentID == bs[0].ID {
			asked = append(asked, r.PaymentID.String())
		}
	}
	if len(asked) != 2 || !slices.Equal(asked[:1], asked[1:]) {
		t.Errorf("invoice requests for the balance = %v, want the same payment twice", asked)
	}
}
