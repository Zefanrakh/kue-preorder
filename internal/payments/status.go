// Package payments holds the money side of an order (docs/architecture.md
// §14): how large the down payment is, and what the payment ledger says the
// customer has paid. There is no cash on delivery and no credit; handing over
// a cake needs PaidInFull, which package orders enforces.
//
// Money is int64 rupiah everywhere. Nothing here uses floats.
package payments

import (
	"errors"
	"fmt"
)

// Status is how far an order is paid (§14). Unpaid, DPPaid, and PaidInFull
// follow from the ledger; Forfeited and Refunded close the order's payments.
type Status string

// Payment statuses (§14).
const (
	Unpaid     Status = "unpaid"
	DPPaid     Status = "dp_paid"      // at least the down payment, less than the total
	PaidInFull Status = "paid_in_full" // the total, before production starts
	Forfeited  Status = "forfeited"    // the balance was not paid in time; the DP is kept (terms)
	Refunded   Status = "refunded"     // money went back: the shop cancelled or rescheduled
)

// Statuses lists every payment status, for exhaustive tests and validation.
var Statuses = []Status{Unpaid, DPPaid, PaidInFull, Forfeited, Refunded}

// Closed reports whether no payment can change the status any more.
func (s Status) Closed() bool { return s == Forfeited || s == Refunded }

// rank orders the ledger-driven statuses; payments only ever move up.
func (s Status) rank() int {
	switch s {
	case Unpaid:
		return 0
	case DPPaid:
		return 1
	case PaidInFull:
		return 2
	default:
		return -1
	}
}

var (
	// ErrClosed means money arrived for an order whose payments are closed,
	// such as a late balance after the DP was forfeited. The status stays;
	// the caller alerts, because a person must return or keep the money.
	ErrClosed = errors.New("payments are closed")
	// ErrIllegalClose means a forfeit or refund does not fit the status.
	ErrIllegalClose = errors.New("illegal payment status change")
)

// Amounts are what an order owes and what the ledger holds, in rupiah.
type Amounts struct {
	TotalIDR      int64 // the order total
	DPRequiredIDR int64 // locked at checkout
	PaidIDR       int64 // Paid over the ledger
}

// Settle returns the status after the ledger changed. It never moves
// backwards: a refund entry does not turn PaidInFull into DPPaid, because
// refunds close the payments through Close instead. For closed payments it
// returns the status unchanged with ErrClosed.
func Settle(current Status, a Amounts) (Status, error) {
	if current.Closed() {
		return current, fmt.Errorf("%w: %s, %d of %d paid", ErrClosed, current, a.PaidIDR, a.TotalIDR)
	}
	next := Unpaid
	switch {
	case a.PaidIDR >= a.TotalIDR && a.TotalIDR > 0:
		next = PaidInFull
	case a.PaidIDR >= a.DPRequiredIDR && a.PaidIDR > 0:
		next = DPPaid
	}
	if next.rank() < current.rank() {
		return current, nil
	}
	return next, nil
}

// Close forfeits or refunds. A forfeit needs a paid DP and an unpaid
// balance; a refund needs something paid.
func Close(current, to Status) (Status, error) {
	ok := false
	switch to {
	case Forfeited:
		ok = current == DPPaid
	case Refunded:
		ok = current == DPPaid || current == PaidInFull
	}
	if !ok {
		return current, fmt.Errorf("%w: %s to %s", ErrIllegalClose, current, to)
	}
	return to, nil
}
