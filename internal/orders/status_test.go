package orders_test

import (
	"errors"
	"slices"
	"testing"

	"pgregory.net/rapid"

	"github.com/Zefanrakh/kue-preorder/internal/orders"
	"github.com/Zefanrakh/kue-preorder/internal/payments"
)

type step struct {
	from, to    orders.Status
	fulfillment orders.Fulfillment
}

// spec is the diagram of §13 written out for both fulfillment types: each
// step and the payment statuses it allows. Everything else is illegal.
var spec = func() map[step][]payments.Status {
	dpIn := []payments.Status{payments.DPPaid, payments.PaidInFull}
	full := []payments.Status{payments.PaidInFull}
	out := map[step][]payments.Status{}
	for _, f := range orders.Fulfillments {
		out[step{orders.AwaitingDP, orders.Confirmed, f}] = dpIn
		out[step{orders.AwaitingDP, orders.Expired, f}] = []payments.Status{payments.Unpaid}
		out[step{orders.Confirmed, orders.InProduction, f}] = full
		out[step{orders.Confirmed, orders.Cancelled, f}] = []payments.Status{payments.Forfeited, payments.Refunded}
		out[step{orders.InProduction, orders.Ready, f}] = full
	}
	out[step{orders.Ready, orders.Completed, orders.Pickup}] = full
	out[step{orders.Ready, orders.OutForDelivery, orders.Delivery}] = full
	out[step{orders.OutForDelivery, orders.Completed, orders.Delivery}] = full
	return out
}()

// Every combination of status, target, payment, and fulfillment.
func TestTransition_MatchesTheLifecycle(t *testing.T) {
	for _, from := range orders.Statuses {
		for _, to := range orders.Statuses {
			for _, f := range orders.Fulfillments {
				for _, p := range payments.Statuses {
					err := orders.Transition(orders.State{Status: from, Payment: p, Fulfillment: f}, to)
					allowed, exists := spec[step{from, to, f}]
					switch {
					case exists && slices.Contains(allowed, p):
						if err != nil {
							t.Errorf("%s -> %s (%s, %s) error = %v, want allowed", from, to, f, p, err)
						}
					case exists:
						if !errors.Is(err, orders.ErrPaymentRequired) {
							t.Errorf("%s -> %s (%s, %s) error = %v, want ErrPaymentRequired", from, to, f, p, err)
						}
					default:
						if !errors.Is(err, orders.ErrIllegalTransition) {
							t.Errorf("%s -> %s (%s, %s) error = %v, want ErrIllegalTransition", from, to, f, p, err)
						}
					}
				}
			}
		}
	}
}

// The M2 invariant: no step towards the customer without full payment.
func TestTransition_NoHandoverWithoutFullPayment(t *testing.T) {
	handover := []orders.Status{orders.InProduction, orders.Ready, orders.OutForDelivery, orders.Completed}
	for _, from := range orders.Statuses {
		for _, to := range handover {
			for _, f := range orders.Fulfillments {
				for _, p := range payments.Statuses {
					if p == payments.PaidInFull {
						continue
					}
					if err := orders.Transition(orders.State{Status: from, Payment: p, Fulfillment: f}, to); err == nil {
						t.Errorf("%s -> %s allowed with payment %s", from, to, p)
					}
				}
			}
		}
	}
}

func TestTransition_TerminalStatusesAreFinal(t *testing.T) {
	for _, from := range orders.Statuses {
		if !from.Terminal() {
			continue
		}
		for _, to := range orders.Statuses {
			for _, f := range orders.Fulfillments {
				for _, p := range payments.Statuses {
					if err := orders.Transition(orders.State{Status: from, Payment: p, Fulfillment: f}, to); !errors.Is(err, orders.ErrIllegalTransition) {
						t.Errorf("%s -> %s error = %v, want ErrIllegalTransition", from, to, err)
					}
				}
			}
		}
	}
}

// A random life of an order: money comes in, gets forfeited or refunded, and
// the kitchen tries every step in any order. Whatever happens, the order
// only enters a handover status while paid in full, and a refunded order
// never moves on.
func TestProperty_RandomLifeNeverHandsOverUnpaid(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		total := rapid.Int64Range(2, 1_000_000).Draw(t, "total")
		dp := rapid.Int64Range(1, total-1).Draw(t, "dp")
		s := orders.State{Status: orders.AwaitingDP, Payment: payments.Unpaid, Fulfillment: rapid.SampledFrom(orders.Fulfillments).Draw(t, "fulfillment")}
		var paid int64

		for range rapid.IntRange(0, 30).Draw(t, "events") {
			switch rapid.IntRange(0, 2).Draw(t, "kind") {
			case 0: // money arrives
				paid += rapid.Int64Range(1, total).Draw(t, "amount")
				next, err := payments.Settle(s.Payment, payments.Amounts{TotalIDR: total, DPRequiredIDR: dp, PaidIDR: paid})
				if err != nil && !errors.Is(err, payments.ErrClosed) {
					t.Fatalf("Settle() error = %v", err)
				}
				s.Payment = next
			case 1: // forfeit or refund
				if next, err := payments.Close(s.Payment, rapid.SampledFrom([]payments.Status{payments.Forfeited, payments.Refunded}).Draw(t, "close")); err == nil {
					s.Payment = next
				}
			case 2: // someone tries a step
				to := rapid.SampledFrom(orders.Statuses).Draw(t, "to")
				if orders.Transition(s, to) != nil {
					continue
				}
				switch to {
				case orders.InProduction, orders.Ready, orders.OutForDelivery, orders.Completed:
					if s.Payment != payments.PaidInFull {
						t.Fatalf("entered %s with payment %s", to, s.Payment)
					}
				}
				if s.Payment == payments.Refunded && to != orders.Cancelled {
					t.Fatalf("a refunded order moved from %s to %s", s.Status, to)
				}
				s.Status = to
			}
		}
	})
}

func TestStatus_CountsForProduction(t *testing.T) {
	var counted []orders.Status
	for _, s := range orders.Statuses {
		if s.CountsForProduction() {
			counted = append(counted, s)
		}
	}
	want := []orders.Status{orders.Confirmed, orders.InProduction, orders.Ready, orders.OutForDelivery, orders.Completed}
	if !slices.Equal(counted, want) {
		t.Errorf("counted = %v, want %v: an order counts from its DP until it is completed", counted, want)
	}
}
