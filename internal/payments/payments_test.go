package payments_test

import (
	"errors"
	"slices"
	"testing"

	"pgregory.net/rapid"

	"github.com/Zefanrakh/kue-preorder/internal/payments"
)

var defaultPolicy = payments.Policy{DPMinPercent: 50, DPCoversIngredientCost: true}

func TestDPRequired(t *testing.T) {
	tests := []struct {
		name   string
		policy payments.Policy
		total  int64
		cost   int64
		want   int64
	}{
		{"half of the total", defaultPolicy, 100000, 20000, 50000},
		{"rounds up to the rupiah", defaultPolicy, 100001, 0, 50001},
		{"ingredients cost more than half", defaultPolicy, 100000, 70000, 70000},
		{"ingredient cost ignored when the policy says so", payments.Policy{DPMinPercent: 50}, 100000, 70000, 50000},
		{"never more than the total", defaultPolicy, 100000, 150000, 100000},
		{"a full payment policy", payments.Policy{DPMinPercent: 100}, 88000, 0, 88000},
		{"one rupiah still needs a DP", payments.Policy{DPMinPercent: 1}, 1, 0, 1},
		{"the largest total", payments.Policy{DPMinPercent: 100}, payments.MaxOrderTotalIDR, 0, payments.MaxOrderTotalIDR},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := payments.DPRequired(tt.policy, tt.total, tt.cost)
			if err != nil || got != tt.want {
				t.Errorf("DPRequired() = %d, %v; want %d", got, err, tt.want)
			}
		})
	}
}

func TestDPRequired_RejectsBadInput(t *testing.T) {
	tests := []struct {
		name   string
		policy payments.Policy
		total  int64
		cost   int64
	}{
		{"no DP", payments.Policy{DPMinPercent: 0}, 100000, 0},
		{"more than the total", payments.Policy{DPMinPercent: 101}, 100000, 0},
		{"zero total", defaultPolicy, 0, 0},
		{"negative total", defaultPolicy, -1, 0},
		{"absurd total", defaultPolicy, payments.MaxOrderTotalIDR + 1, 0},
		{"negative cost", defaultPolicy, 100000, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := payments.DPRequired(tt.policy, tt.total, tt.cost); err == nil {
				t.Errorf("DPRequired() = %d, want an error", got)
			}
		})
	}
}

// The DP is at least the percentage and, when asked, the ingredient cost,
// but never more than the total.
func TestProperty_DPRequired(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := payments.Policy{
			DPMinPercent:           rapid.Int32Range(1, 100).Draw(t, "percent"),
			DPCoversIngredientCost: rapid.Bool().Draw(t, "covers"),
		}
		total := rapid.Int64Range(1, payments.MaxOrderTotalIDR).Draw(t, "total")
		cost := rapid.Int64Range(0, payments.MaxOrderTotalIDR).Draw(t, "cost")

		dp, err := payments.DPRequired(p, total, cost)

		if err != nil {
			t.Fatalf("DPRequired() error = %v", err)
		}
		if dp < 1 || dp > total {
			t.Fatalf("dp = %d, want 1..%d", dp, total)
		}
		// dp*100 >= total*percent, the percentage rounded up, unless capped.
		if dp < total && dp*100 < total*int64(p.DPMinPercent) {
			t.Fatalf("dp = %d is below %d%% of %d", dp, p.DPMinPercent, total)
		}
		if p.DPCoversIngredientCost && dp < min(cost, total) {
			t.Fatalf("dp = %d does not cover ingredients of %d", dp, cost)
		}
		// Rounding up adds less than one rupiah.
		if !p.DPCoversIngredientCost && (dp-1)*100 >= total*int64(p.DPMinPercent) {
			t.Fatalf("dp = %d is more than %d%% of %d rounded up", dp, p.DPMinPercent, total)
		}
	})
}

func TestSettle(t *testing.T) {
	owed := func(paid int64) payments.Amounts {
		return payments.Amounts{TotalIDR: 100000, DPRequiredIDR: 50000, PaidIDR: paid}
	}
	tests := []struct {
		name    string
		current payments.Status
		paid    int64
		want    payments.Status
	}{
		{"nothing paid", payments.Unpaid, 0, payments.Unpaid},
		{"less than the DP", payments.Unpaid, 49999, payments.Unpaid},
		{"the DP", payments.Unpaid, 50000, payments.DPPaid},
		{"paid in full at once", payments.Unpaid, 100000, payments.PaidInFull},
		{"the balance", payments.DPPaid, 100000, payments.PaidInFull},
		{"more than the total", payments.DPPaid, 100500, payments.PaidInFull},
		{"never backwards", payments.PaidInFull, 50000, payments.PaidInFull},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := payments.Settle(tt.current, owed(tt.paid))
			if err != nil || got != tt.want {
				t.Errorf("Settle(%s, paid %d) = %s, %v; want %s", tt.current, tt.paid, got, err, tt.want)
			}
		})
	}
}

// Money for a closed order changes nothing and is reported, so a person can
// return it: it must never reopen a forfeited order silently.
func TestSettle_ClosedPaymentsStayClosed(t *testing.T) {
	for _, s := range []payments.Status{payments.Forfeited, payments.Refunded} {
		got, err := payments.Settle(s, payments.Amounts{TotalIDR: 100000, DPRequiredIDR: 50000, PaidIDR: 100000})
		if got != s || !errors.Is(err, payments.ErrClosed) {
			t.Errorf("Settle(%s) = %s, %v; want %s and ErrClosed", s, got, err, s)
		}
	}
}

func TestProperty_SettleNeverMovesBackwards(t *testing.T) {
	rank := map[payments.Status]int{payments.Unpaid: 0, payments.DPPaid: 1, payments.PaidInFull: 2}
	rapid.Check(t, func(t *rapid.T) {
		total := rapid.Int64Range(1, 1_000_000).Draw(t, "total")
		dp := rapid.Int64Range(1, total).Draw(t, "dp")
		status := payments.Unpaid
		for _, paid := range rapid.SliceOf(rapid.Int64Range(-total, 2*total)).Draw(t, "ledger sums") {
			next, err := payments.Settle(status, payments.Amounts{TotalIDR: total, DPRequiredIDR: dp, PaidIDR: paid})
			if err != nil {
				t.Fatalf("Settle() error = %v", err)
			}
			if rank[next] < rank[status] {
				t.Fatalf("Settle(%s, paid %d) = %s: moved backwards", status, paid, next)
			}
			if paid >= total && next != payments.PaidInFull {
				t.Fatalf("Settle(paid %d of %d) = %s, want paid_in_full", paid, total, next)
			}
			status = next
		}
	})
}

func TestClose(t *testing.T) {
	allowed := map[[2]payments.Status]bool{
		{payments.DPPaid, payments.Forfeited}:    true,
		{payments.DPPaid, payments.Refunded}:     true,
		{payments.PaidInFull, payments.Refunded}: true,
	}
	for _, from := range payments.Statuses {
		for _, to := range payments.Statuses {
			got, err := payments.Close(from, to)
			switch {
			case allowed[[2]payments.Status{from, to}]:
				if err != nil || got != to {
					t.Errorf("Close(%s, %s) = %s, %v; want %s", from, to, got, err, to)
				}
			case !errors.Is(err, payments.ErrIllegalClose) || got != from:
				t.Errorf("Close(%s, %s) = %s, %v; want %s and ErrIllegalClose", from, to, got, err, from)
			}
		}
	}
}

func TestStatus_Closed(t *testing.T) {
	var closed []payments.Status
	for _, s := range payments.Statuses {
		if s.Closed() {
			closed = append(closed, s)
		}
	}
	if !slices.Equal(closed, []payments.Status{payments.Forfeited, payments.Refunded}) {
		t.Errorf("closed statuses = %v, want forfeited and refunded", closed)
	}
}
