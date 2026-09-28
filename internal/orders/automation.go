package orders

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/payments"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

// Grace is how long after a deadline the worker waits before ending an
// order. The invoice itself closes at the deadline, so no payment starts
// after it; a payment made just before may still be reported a little
// later, and the grace keeps that customer from losing the order.
const Grace = 30 * time.Minute

// sweepLimit bounds the orders one sweep ends; the next sweep takes the rest.
const sweepLimit = 100

// AutomationDeps are what Automation works with.
type AutomationDeps struct {
	Repo     Repository
	Ledger   Ledger
	Policies Policies
	Provider payments.Provider
	Tx       Transactor
	Clock    clock.Clock
}

// Automation is what the worker does to orders on its own (§14, §21): it
// expires orders whose DP never came, forfeits the DP of orders whose balance
// never came, and bills the balance once the DP is in. Every step runs in
// one transaction with the order's row locked, goes through Transition, and
// leaves its outbox events; each checks the order again under the lock, so
// running it twice, or while staff act on the order, changes nothing twice.
// It acts for no person, so it writes no audit entry: its events say
// "automatic".
type Automation struct {
	repo     Repository
	ledger   Ledger
	policies Policies
	invoices invoicer
	tx       Transactor
	clock    clock.Clock
}

// NewAutomation returns an Automation over d.
func NewAutomation(d AutomationDeps) *Automation {
	return &Automation{
		repo: d.Repo, ledger: d.Ledger, policies: d.Policies, tx: d.Tx, clock: d.Clock,
		invoices: invoicer{provider: d.Provider, ledger: d.Ledger, clock: d.Clock},
	}
}

// ExpireUnpaid expires the tenant's orders whose DP did not arrive by
// their DP deadline, plus Grace (§13). It returns how many it expired.
func (a *Automation) ExpireUnpaid(ctx context.Context, tenant uuid.UUID) (int, error) {
	now := a.clock.Now()
	ids, err := a.repo.PastDPDue(ctx, tenant, AwaitingDP, payments.Unpaid, now.Add(-Grace), sweepLimit)
	if err != nil {
		return 0, err
	}
	return a.sweep(ctx, tenant, ids, func(ctx context.Context, o Order) (bool, error) {
		if o.Status != AwaitingDP || o.Payment != payments.Unpaid || now.Before(o.DPDueAt.Add(Grace)) {
			return false, nil // paid or ended meanwhile
		}
		if err := Transition(o.state(), Expired); err != nil {
			return false, err
		}
		return true, a.end(ctx, tenant, o, Expired, o.Payment, "order.expired", now, map[string]any{"mode": "dp_overdue"})
	})
}

// ForfeitUnpaid cancels the tenant's orders whose balance did not arrive by
// their balance deadline, plus Grace, and keeps what was paid, as the terms
// say (§14). An order paid in full is never touched. It returns how many it
// cancelled.
func (a *Automation) ForfeitUnpaid(ctx context.Context, tenant uuid.UUID) (int, error) {
	now := a.clock.Now()
	ids, err := a.repo.PastBalanceDue(ctx, tenant, Confirmed, payments.DPPaid, now.Add(-Grace), sweepLimit)
	if err != nil {
		return 0, err
	}
	return a.sweep(ctx, tenant, ids, func(ctx context.Context, o Order) (bool, error) {
		if o.Status != Confirmed || o.Payment != payments.DPPaid || now.Before(o.BalanceDueAt.Add(Grace)) {
			return false, nil // paid or ended meanwhile
		}
		pay, err := payments.Close(o.Payment, payments.Forfeited)
		if err != nil {
			return false, err
		}
		if err := Transition(State{Status: o.Status, Payment: pay, Fulfillment: o.Fulfillment}, Cancelled); err != nil {
			return false, err
		}
		return true, a.end(ctx, tenant, o, Cancelled, pay, "order.cancelled", now, map[string]any{"mode": "forfeit", "payment_status": pay})
	})
}

// sweep runs end on each order in its own transaction with the row locked,
// so one order that fails does not hold up the others. It returns how many
// orders end changed and every failure.
func (a *Automation) sweep(ctx context.Context, tenant uuid.UUID, ids []uuid.UUID, end func(ctx context.Context, o Order) (bool, error)) (int, error) {
	n := 0
	var errs []error
	for _, id := range ids {
		var changed bool
		err := a.tx.Tx(ctx, func(ctx context.Context) error {
			o, err := a.repo.LockByID(ctx, tenant, id)
			if err != nil {
				return err
			}
			changed, err = end(ctx, o)
			return err
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("order %s: %w", id, err))
			continue
		}
		if changed {
			n++
		}
	}
	return n, errors.Join(errs...)
}

// end stores an order's final statuses, expires its open invoices, and
// publishes typ.
func (a *Automation) end(ctx context.Context, tenant uuid.UUID, o Order, s Status, pay payments.Status, typ string, at time.Time, extra map[string]any) error {
	if err := a.repo.SetStatus(ctx, tenant, o.ID, s, pay, at); err != nil {
		return err
	}
	if _, err := a.ledger.ExpirePending(ctx, tenant, o.ID, at); err != nil {
		return err
	}
	extra["automatic"] = true
	return a.repo.Publish(ctx, orderEvent(tenant, o, typ, at, extra))
}

// EnsureBalanceInvoice bills what a confirmed order still owes (§14): one
// pending balance payment for the rest of the total, open until the balance
// deadline, with the method the customer chose at checkout and its "Biaya
// admin" from the current fee rules. Nothing is billed for an order paid in
// full, ended, past its balance deadline, or already billed. The invoice is
// made after the commit, as at checkout; a provider failure is returned, so
// the job retries, and the provider's idempotency per payment keeps retries
// from billing twice.
func (a *Automation) EnsureBalanceInvoice(ctx context.Context, tenant, orderID uuid.UUID) error {
	now := a.clock.Now()
	err := a.tx.Tx(ctx, func(ctx context.Context) error {
		o, err := a.repo.LockByID(ctx, tenant, orderID)
		if err != nil {
			return err
		}
		if o.Status != Confirmed || o.Payment != payments.DPPaid || !now.Before(o.BalanceDueAt) {
			return nil
		}
		ps, err := a.ledger.OrderPayments(ctx, tenant, o.ID)
		if err != nil {
			return err
		}
		owed := o.TotalIDR - payments.Paid(ps)
		if owed <= 0 || billed(ps) {
			return nil
		}
		method, fee, err := a.balanceMethod(ctx, tenant, ps, owed)
		if err != nil {
			return err
		}
		due := o.BalanceDueAt
		return a.ledger.AddPending(ctx, tenant, payments.Payment{
			ID: uuid.New(), OrderID: o.ID, Kind: payments.KindBalance, Provider: a.invoices.provider.Name(),
			Method: method, AmountIDR: owed, FeeIDR: fee, ExpiresAt: &due,
		}, now)
	})
	if err != nil {
		return err
	}
	o, err := a.repo.Get(ctx, tenant, orderID)
	if err != nil {
		return err
	}
	if o.Payments, err = a.ledger.OrderPayments(ctx, tenant, o.ID); err != nil {
		return err
	}
	return a.invoices.ensure(ctx, tenant, &o)
}

// billed reports whether an open balance payment exists already.
func billed(ps []payments.Payment) bool {
	for _, p := range ps {
		if p.State == payments.StatePending && p.Kind == payments.KindBalance {
			return true
		}
	}
	return false
}

// balanceMethod picks how the balance is paid (§14, decided 2026-09-28): the
// method of the order's first invoice, which the customer chose at checkout.
// When the owner has switched it off since, QRIS, which costs the customer
// nothing; failing that, the first method on offer.
func (a *Automation) balanceMethod(ctx context.Context, tenant uuid.UUID, ps []payments.Payment, owed int64) (payments.Method, int64, error) {
	rules, err := a.policies.FeeRules(ctx, tenant)
	if err != nil {
		return "", 0, err
	}
	var chosen payments.Method
	for _, p := range ps {
		if p.Method != "" {
			chosen = p.Method
			break
		}
	}
	for _, m := range append([]payments.Method{chosen}, payments.Methods...) {
		if rule, ok := rules[m]; ok && rule.Enabled {
			fee, err := rule.Fee(owed)
			return m, fee, err
		}
	}
	return "", 0, errors.New("no payment method on offer")
}
