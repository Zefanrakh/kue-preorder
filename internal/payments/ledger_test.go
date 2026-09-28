package payments_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/payments"
)

// memLedger keeps what the ledger stores.
type memLedger struct {
	payments []payments.Payment
}

func (m *memLedger) AddPayment(_ context.Context, _ uuid.UUID, p payments.Payment, _ time.Time) error {
	m.payments = append(m.payments, p)
	return nil
}

func (m *memLedger) SetInvoice(context.Context, uuid.UUID, uuid.UUID, payments.Invoice, time.Time) error {
	return nil
}

func (m *memLedger) OrderPayments(context.Context, uuid.UUID, uuid.UUID) ([]payments.Payment, error) {
	return m.payments, nil
}

func (m *memLedger) ExpirePending(context.Context, uuid.UUID, uuid.UUID, time.Time) (int64, error) {
	return 0, nil
}

var at = time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC)

func manual(kind payments.Kind, amount int64) payments.Payment {
	return payments.Payment{
		ID: uuid.New(), OrderID: uuid.New(), Kind: kind, AmountIDR: amount,
		Manual: &payments.ManualProof{Reference: "BCA 0412", Note: "Transfer langsung", RecordedBy: uuid.New()},
	}
}

// A manual payment is paid as it is recorded, by the manual provider,
// without a fee or an invoice, whatever the caller filled in.
func TestLedger_AddManual(t *testing.T) {
	mem := &memLedger{}
	p := manual(payments.KindBalance, 50000)
	p.Provider, p.Method, p.FeeIDR, p.State, p.CheckoutURL = "dev", payments.MethodEWallet, 1000, payments.StatePending, "https://x"

	if err := payments.NewLedger(mem).AddManual(t.Context(), uuid.New(), p, at); err != nil {
		t.Fatal(err)
	}

	got := mem.payments[0]
	if got.Provider != payments.ProviderManual || got.Method != "" || got.FeeIDR != 0 || got.State != payments.StatePaid ||
		got.PaidAt == nil || !got.PaidAt.Equal(at) || got.CheckoutURL != "" || got.ExpiresAt != nil || got.Manual.Reference != "BCA 0412" {
		t.Errorf("stored %+v, want a paid manual payment without a fee", got)
	}
}

func TestLedger_AddManualRejects(t *testing.T) {
	noProof := manual(payments.KindDP, 100)
	noProof.Manual = nil
	noRecorder := manual(payments.KindDP, 100)
	noRecorder.Manual.RecordedBy = uuid.Nil
	noReference := manual(payments.KindDP, 100)
	noReference.Manual.Reference = ""
	for name, p := range map[string]payments.Payment{
		"zero":             manual(payments.KindDP, 0),
		"negative payment": manual(payments.KindFull, -100),
		"positive refund":  manual(payments.KindRefund, 100),
		"too large":        manual(payments.KindBalance, payments.MaxOrderTotalIDR+1),
		"unknown kind":     manual("tip", 100),
		"no proof":         noProof,
		"no recorder":      noRecorder,
		"no reference":     noReference,
	} {
		mem := &memLedger{}
		if err := payments.NewLedger(mem).AddManual(t.Context(), uuid.New(), p, at); err == nil || len(mem.payments) != 0 {
			t.Errorf("AddManual(%s) error = %v, stored %d; want an error and nothing stored", name, err, len(mem.payments))
		}
	}
	err := payments.NewLedger(&memLedger{}).AddManual(t.Context(), uuid.New(), manual(payments.KindRefund, 0), at)
	if !errors.Is(err, payments.ErrInvalidAmount) {
		t.Errorf("AddManual(refund of 0) error = %v, want ErrInvalidAmount", err)
	}
}

func TestPaid(t *testing.T) {
	ps := []payments.Payment{
		{AmountIDR: 49500, FeeIDR: 4440, State: payments.StatePaid},
		{AmountIDR: 49500, State: payments.StateExpired},
		{AmountIDR: 49500, State: payments.StatePending},
		{AmountIDR: 20000, State: payments.StatePaid},
		{AmountIDR: -69500, State: payments.StatePaid, Kind: payments.KindRefund},
		{AmountIDR: 1000, State: payments.StateFailed},
	}
	if got := payments.Paid(ps[:4]); got != 69500 {
		t.Errorf("Paid() = %d, want 69,500: only paid rows, never fees", got)
	}
	if got := payments.Paid(ps); got != 0 {
		t.Errorf("Paid() after the refund = %d, want 0", got)
	}
}
