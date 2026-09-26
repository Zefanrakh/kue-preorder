package orders

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/payments"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

// TermsVersion is the version of the DP terms (§14) a customer accepts at
// checkout. The text is still to be written by the owner (§27); bump the
// version whenever it changes, and customers accept the new one.
const TermsVersion = "dp-draft-1"

const (
	// maxAwaitingDP is how many orders one customer may have waiting for
	// their DP at once (§27): a single account cannot flood the kitchen's
	// dashboard with orders it never pays.
	maxAwaitingDP = 2
	// maxNotes bounds the customer's notes, such as the writing on a cake.
	maxNotes = 500
	// myOrdersLimit bounds the list of a customer's orders.
	myOrdersLimit = 50
	// codeAttempts is how many random codes Place draws before giving up.
	codeAttempts = 5
)

// ErrTooManyUnpaid refuses a third order while two still wait for their DP.
var ErrTooManyUnpaid = &apperr.PreconditionError{
	Reason:  "too_many_unpaid",
	Message: "Selesaikan pembayaran pesanan sebelumnya dulu. Paling banyak 2 pesanan boleh menunggu DP.",
}

// PlaceRequest is a checkout: the cart, the pickup time, and what the
// customer chose and typed.
type PlaceRequest struct {
	QuoteRequest
	// PayInFull pays the whole total at once instead of a DP. A schedule
	// that requires full payment ignores it.
	PayInFull    bool
	TermsVersion string
	Customer     identity.CustomerInput
	Notes        string
	// IdempotencyKey comes from the client, one per checkout attempt: sending
	// the same checkout twice returns the order placed the first time.
	IdempotencyKey uuid.UUID
}

// Place turns a cart into an order waiting for its first payment (§13, §14).
// The customer must be signed in with WhatsApp. Everything is priced and
// scheduled again on the server; the order, its items, its first payment,
// and its order.placed event are stored in one transaction. The invoice is
// made afterwards: if the provider fails, the order stands and the invoice is
// made again when the customer opens it.
func (c *Checkout) Place(ctx context.Context, req PlaceRequest) (Order, error) {
	req.Notes = strings.TrimSpace(req.Notes)
	f := apperr.Fields{}
	f.Check(req.TermsVersion == TermsVersion, "terms_version", "Syarat dan ketentuan sudah diperbarui. Baca dan setujui lagi.")
	f.Check(utf8.RuneCountInString(req.Notes) <= maxNotes, "notes", "Catatan paling panjang 500 karakter.")
	f.Check(req.IdempotencyKey != uuid.Nil, "idempotency_key", "Muat ulang halaman lalu coba lagi.")
	if err := f.Err(); err != nil {
		return Order{}, err
	}
	now := c.clock.Now()
	customer, err := c.customers.EnsureCustomer(ctx, req.Customer, now)
	if err != nil {
		return Order{}, err
	}
	tenant := c.tenants.TenantID(ctx)
	if id, err := c.repo.FindByIdempotencyKey(ctx, tenant, customer.ID, req.IdempotencyKey); err == nil {
		return c.load(ctx, tenant, id)
	} else if !errors.Is(err, apperr.ErrNotFound) {
		return Order{}, err
	}

	q, err := c.Quote(ctx, req.QuoteRequest)
	if err != nil {
		return Order{}, err
	}
	if q.Schedule == nil {
		return Order{}, &apperr.ValidationError{Fields: map[string]string{"pickup_at": q.Rejection.Message}}
	}
	p := q.Schedule
	o := Order{
		ID: uuid.New(), CustomerID: customer.ID, Status: AwaitingDP, Payment: payments.Unpaid, Fulfillment: Pickup,
		Items: q.Items, SubtotalIDR: q.SubtotalIDR, TaxIDR: q.TaxIDR, TotalIDR: q.TotalIDR,
		DPRequiredIDR: q.DPRequiredIDR, FullPaymentRequired: p.FullPaymentRequired,
		PickupAt: p.PickupAt, ProductionStart: p.ProductionStart, ProductionDate: p.ProductionDate,
		ShoppingCutoffAt: p.Cutoff, DPDueAt: p.DPDeadline, BalanceDueAt: p.BalanceDue,
		Notes: req.Notes, CustomerName: customer.Name, CustomerPhone: customer.Phone, CustomerEmail: customer.Email,
		TermsVersion: TermsVersion,
	}
	first := payments.Payment{
		ID: uuid.New(), OrderID: o.ID, Kind: payments.KindDP, Provider: c.provider.Name(),
		AmountIDR: o.DPRequiredIDR, ExpiresAt: &o.DPDueAt,
	}
	if o.FullPaymentRequired || req.PayInFull {
		first.Kind, first.AmountIDR = payments.KindFull, o.TotalIDR
	}

	placed := o.ID
	err = c.tx.Tx(ctx, func(ctx context.Context) error {
		if err := c.repo.LockCustomer(ctx, customer.ID); err != nil {
			return err
		}
		// A twin request may have committed while this one was pricing.
		if id, err := c.repo.FindByIdempotencyKey(ctx, tenant, customer.ID, req.IdempotencyKey); err == nil {
			placed = id
			return nil
		} else if !errors.Is(err, apperr.ErrNotFound) {
			return err
		}
		waiting, err := c.repo.CountByStatus(ctx, tenant, customer.ID, AwaitingDP)
		if err != nil {
			return err
		}
		if waiting >= maxAwaitingDP {
			return ErrTooManyUnpaid
		}
		if err := c.insert(ctx, tenant, &o, req.IdempotencyKey, now); err != nil {
			return err
		}
		return c.ledger.AddPending(ctx, tenant, first, now)
	})
	if err != nil {
		return Order{}, err
	}
	return c.load(ctx, tenant, placed)
}

// insert stores o under a fresh random code, drawing again on a clash.
func (c *Checkout) insert(ctx context.Context, tenant uuid.UUID, o *Order, key uuid.UUID, at time.Time) error {
	for range codeAttempts {
		o.Code = NewCode()
		ok, err := c.repo.Insert(ctx, tenant, *o, key, at)
		if err != nil || ok {
			return err
		}
	}
	return fmt.Errorf("no free order code after %d draws", codeAttempts)
}

// MyOrders lists the caller's orders, newest first. A signed-in user who
// never ordered has none.
func (c *Checkout) MyOrders(ctx context.Context) ([]Summary, error) {
	p, err := c.customers.Principal(ctx)
	if err != nil {
		return nil, err
	}
	if p.CustomerID == nil {
		return []Summary{}, nil
	}
	return c.repo.ListCustomerOrders(ctx, p.TenantID, *p.CustomerID, myOrdersLimit)
}

// MyOrder returns the caller's order with that code, with its payments. An
// order of someone else is apperr.ErrNotFound, as if it did not exist.
func (c *Checkout) MyOrder(ctx context.Context, code string) (Order, error) {
	p, err := c.customers.Principal(ctx)
	if err != nil {
		return Order{}, err
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	if p.CustomerID == nil || len(code) != 6 {
		return Order{}, apperr.ErrNotFound
	}
	o, err := c.repo.FindCustomerOrder(ctx, p.TenantID, *p.CustomerID, code)
	if err != nil {
		return Order{}, err
	}
	return c.withPayments(ctx, p.TenantID, o)
}

// load reads an order with its payments, making any missing invoice.
func (c *Checkout) load(ctx context.Context, tenant, id uuid.UUID) (Order, error) {
	o, err := c.repo.Get(ctx, tenant, id)
	if err != nil {
		return Order{}, err
	}
	return c.withPayments(ctx, tenant, o)
}

func (c *Checkout) withPayments(ctx context.Context, tenant uuid.UUID, o Order) (Order, error) {
	ps, err := c.ledger.OrderPayments(ctx, tenant, o.ID)
	if err != nil {
		return Order{}, err
	}
	o.Payments = ps
	c.ensureInvoices(ctx, tenant, &o)
	return o, nil
}

// ensureInvoices asks the provider for the invoice of every pending payment
// that has none yet and is still open. A failure is logged at ERROR, since a
// customer who cannot pay is lost revenue, and the order is returned without
// the link; the next read tries again. The provider's idempotency per payment
// keeps retries from billing twice.
func (c *Checkout) ensureInvoices(ctx context.Context, tenant uuid.UUID, o *Order) {
	now := c.clock.Now()
	for i := range o.Payments {
		p := &o.Payments[i]
		if p.State != payments.StatePending || p.CheckoutURL != "" || (p.ExpiresAt != nil && !p.ExpiresAt.After(now)) {
			continue
		}
		inv, err := c.provider.CreateInvoice(ctx, payments.InvoiceRequest{
			PaymentID: p.ID, OrderCode: o.Code, Description: invoiceDescription(p.Kind, o.Code),
			AmountIDR: p.AmountIDR, ExpiresAt: derefTime(p.ExpiresAt),
			CustomerName: o.CustomerName, CustomerPhone: o.CustomerPhone, CustomerEmail: o.CustomerEmail,
		})
		if err != nil {
			c.logger.ErrorContext(ctx, "create invoice", slog.String("order", o.Code), slog.String("payment_id", p.ID.String()), slog.Any("error", err))
			continue
		}
		if err := c.ledger.AttachInvoice(ctx, tenant, p.ID, inv, now); err != nil {
			c.logger.ErrorContext(ctx, "record invoice", slog.String("order", o.Code), slog.String("payment_id", p.ID.String()), slog.Any("error", err))
			continue
		}
		p.ExternalID, p.CheckoutURL = inv.ExternalID, inv.URL
	}
}

func invoiceDescription(kind payments.Kind, code string) string {
	switch kind {
	case payments.KindFull:
		return "Pembayaran penuh pesanan " + code
	case payments.KindBalance:
		return "Pelunasan pesanan " + code
	default:
		return "DP pesanan " + code
	}
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// Reader lets other modules read orders in-process for a tenant.
type Reader struct {
	repo Repository
}

// NewReader returns a Reader over repo.
func NewReader(repo Repository) *Reader {
	return &Reader{repo: repo}
}

// ActiveOrders counts, for each day from..to, the active orders produced or
// picked up that day: a closed day with any is on hold (§15).
func (r *Reader) ActiveOrders(ctx context.Context, tenantID uuid.UUID, from, to clock.Date) (map[clock.Date]int, error) {
	var active []Status
	for _, s := range Statuses {
		if s.Active() {
			active = append(active, s)
		}
	}
	return r.repo.ActiveOrderDays(ctx, tenantID, from, to, active)
}
