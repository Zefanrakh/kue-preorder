// Package orders holds the order lifecycle (docs/architecture.md §13). An
// order's status changes only through Transition, which refuses illegal
// steps and, above all, any step towards handing over a cake that is not
// paid in full.
package orders

import (
	"errors"
	"fmt"
	"slices"

	"github.com/Zefanrakh/kue-preorder/internal/payments"
)

// Status is where an order is in its life (§13).
type Status string

// Order statuses (§13).
const (
	AwaitingDP     Status = "awaiting_dp"
	Confirmed      Status = "confirmed" // the DP is in; the order counts for its batch
	Expired        Status = "expired"   // the DP invoice lapsed unpaid
	InProduction   Status = "in_production"
	Ready          Status = "ready"
	OutForDelivery Status = "out_for_delivery"
	Completed      Status = "completed"
	Cancelled      Status = "cancelled" // forfeited or refunded
)

// Statuses lists every order status, for exhaustive tests and validation.
var Statuses = []Status{AwaitingDP, Confirmed, Expired, InProduction, Ready, OutForDelivery, Completed, Cancelled}

// Fulfillment is how the cake reaches the customer.
type Fulfillment string

// Fulfillment types. Until Biteship arrives (M6) every order is a pickup;
// a delivery the shop arranges with the customer outside the system is a
// pickup too, as far as the system knows.
const (
	Pickup   Fulfillment = "pickup"
	Delivery Fulfillment = "delivery"
)

// Fulfillments lists every fulfillment type.
var Fulfillments = []Fulfillment{Pickup, Delivery}

// State is what a transition depends on.
type State struct {
	Status      Status
	Payment     payments.Status
	Fulfillment Fulfillment
}

// CountsForProduction reports whether the order is in its batch's shopping
// list and production: from Confirmed (the DP is in) until it is completed.
func (s Status) CountsForProduction() bool {
	switch s {
	case Confirmed, InProduction, Ready, OutForDelivery, Completed:
		return true
	default:
		return false
	}
}

// Terminal reports whether no transition leaves the status.
func (s Status) Terminal() bool {
	return s == Expired || s == Completed || s == Cancelled
}

var (
	// ErrIllegalTransition means the lifecycle has no such step.
	ErrIllegalTransition = errors.New("illegal order transition")
	// ErrPaymentRequired means the step exists but the payments do not allow
	// it, such as production before the balance is paid.
	ErrPaymentRequired = errors.New("order payment does not allow this transition")
)

// edge is one step of the lifecycle with the payment statuses it needs.
type edge struct {
	from, to    Status
	payments    []payments.Status
	fulfillment Fulfillment // empty: either
}

var (
	dpIn = []payments.Status{payments.DPPaid, payments.PaidInFull}
	full = []payments.Status{payments.PaidInFull}
)

// lifecycle is the diagram of §13. Every step from Confirmed onwards towards
// the customer needs PaidInFull, so there is no path to hand over a cake that
// is not paid; a refund on the way stops the order where it is.
var lifecycle = []edge{
	{AwaitingDP, Confirmed, dpIn, ""},
	{AwaitingDP, Expired, []payments.Status{payments.Unpaid}, ""},
	{Confirmed, InProduction, full, ""},
	{Confirmed, Cancelled, []payments.Status{payments.Forfeited, payments.Refunded}, ""},
	{InProduction, Ready, full, ""},
	{Ready, OutForDelivery, full, Delivery},
	{Ready, Completed, full, Pickup},
	{OutForDelivery, Completed, full, Delivery},
}

// Transition checks that the order in state s may move to status to. It
// returns ErrIllegalTransition for a step the lifecycle does not have and
// ErrPaymentRequired when the payments do not allow it yet.
func Transition(s State, to Status) error {
	for _, e := range lifecycle {
		if e.from != s.Status || e.to != to {
			continue
		}
		if e.fulfillment != "" && e.fulfillment != s.Fulfillment {
			continue
		}
		if !slices.Contains(e.payments, s.Payment) {
			return fmt.Errorf("%w: %s to %s with payment %s", ErrPaymentRequired, s.Status, to, s.Payment)
		}
		return nil
	}
	return fmt.Errorf("%w: %s to %s (%s)", ErrIllegalTransition, s.Status, to, s.Fulfillment)
}
