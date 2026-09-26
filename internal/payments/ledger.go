package payments

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Kind is what a payment is for (§9.8).
type Kind string

// Payment kinds.
const (
	KindDP      Kind = "dp"
	KindBalance Kind = "balance"
	KindFull    Kind = "full" // the whole total at once
	KindRefund  Kind = "refund"
)

// State is where one payment of the ledger stands.
type State string

// Payment states.
const (
	StatePending State = "pending"
	StatePaid    State = "paid"
	StateExpired State = "expired"
	StateFailed  State = "failed"
)

// Payment is one row of the payment ledger. What an order has paid is the
// sum of AmountIDR over its paid payments; FeeIDR, the "biaya admin", is
// paid on top and never counts towards the order (§14).
type Payment struct {
	ID          uuid.UUID
	OrderID     uuid.UUID
	Kind        Kind
	Provider    string // "xendit", "manual", or "dev"
	ExternalID  string // the provider's id; empty until the invoice exists
	AmountIDR   int64  // negative for a refund
	FeeIDR      int64
	State       State
	CheckoutURL string // where the customer pays; empty until the invoice exists
	ExpiresAt   *time.Time
	PaidAt      *time.Time
	CreatedAt   time.Time
}

// InvoiceRequest asks a provider for an invoice the customer pays online.
type InvoiceRequest struct {
	PaymentID     uuid.UUID
	OrderCode     string
	Description   string // shown on the payment page, in Indonesian
	AmountIDR     int64
	ExpiresAt     time.Time
	CustomerName  string
	CustomerPhone string
	CustomerEmail string // optional
}

// Invoice is a provider's invoice for one payment.
type Invoice struct {
	ExternalID string
	URL        string
}

// Provider creates invoices customers pay online (§14); Xendit arrives in
// M2.6. CreateInvoice must be idempotent per PaymentID: asking again, after
// a timeout for example, returns the same invoice rather than a second one,
// so a customer is never billed twice.
type Provider interface {
	// Name is what the payments.provider column records.
	Name() string
	CreateInvoice(ctx context.Context, r InvoiceRequest) (Invoice, error)
}

// DevProvider pretends to create invoices, for development without a
// payment account. Its links lead nowhere; production refuses to run with it.
type DevProvider struct{}

// Name implements Provider.
func (DevProvider) Name() string { return "dev" }

// CreateInvoice implements Provider.
func (DevProvider) CreateInvoice(_ context.Context, r InvoiceRequest) (Invoice, error) {
	return Invoice{ExternalID: "dev-" + r.PaymentID.String(), URL: "https://pay.dev.invalid/" + r.PaymentID.String()}, nil
}

// LedgerRepository is the persistence port of the payment ledger. It works
// in the caller's transaction when there is one (platform/db.Tx), so an
// order and its first payment are stored together.
type LedgerRepository interface {
	AddPayment(ctx context.Context, tenantID uuid.UUID, p Payment, at time.Time) error
	// SetInvoice records a pending payment's invoice; ErrNotFound when the
	// payment is not pending.
	SetInvoice(ctx context.Context, tenantID, paymentID uuid.UUID, inv Invoice, at time.Time) error
	// OrderPayments returns an order's payments, oldest first.
	OrderPayments(ctx context.Context, tenantID, orderID uuid.UUID) ([]Payment, error)
}

// Ledger records payments. Other modules use it in-process for a tenant.
type Ledger struct {
	repo LedgerRepository
}

// NewLedger returns a Ledger over repo.
func NewLedger(repo LedgerRepository) *Ledger {
	return &Ledger{repo: repo}
}

// AddPending records a payment the customer is yet to make.
func (l *Ledger) AddPending(ctx context.Context, tenantID uuid.UUID, p Payment, at time.Time) error {
	if p.Kind == KindRefund || p.AmountIDR <= 0 || p.AmountIDR > MaxOrderTotalIDR {
		return fmt.Errorf("%w: pending %s payment of %d", ErrInvalidAmount, p.Kind, p.AmountIDR)
	}
	if p.ID == uuid.Nil || p.OrderID == uuid.Nil || p.Provider == "" {
		return errors.New("pending payment without an id, an order, or a provider")
	}
	p.State = StatePending
	return l.repo.AddPayment(ctx, tenantID, p, at)
}

// AttachInvoice records the invoice a provider made for a pending payment.
func (l *Ledger) AttachInvoice(ctx context.Context, tenantID, paymentID uuid.UUID, inv Invoice, at time.Time) error {
	return l.repo.SetInvoice(ctx, tenantID, paymentID, inv, at)
}

// OrderPayments returns an order's payments, oldest first.
func (l *Ledger) OrderPayments(ctx context.Context, tenantID, orderID uuid.UUID) ([]Payment, error) {
	return l.repo.OrderPayments(ctx, tenantID, orderID)
}
