package orders

import (
	"crypto/rand"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/payments"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

// Order is a placed order as its customer and the shop see it. Its prices,
// schedule, and terms were locked at checkout.
type Order struct {
	ID         uuid.UUID
	CustomerID uuid.UUID
	// Code is what the customer reads out on WhatsApp, such as K7M3QX.
	Code        string
	Status      Status
	Payment     payments.Status
	Fulfillment Fulfillment

	Items               []QuotedItem
	SubtotalIDR         int64
	TaxIDR              int64
	ShippingIDR         int64
	TotalIDR            int64
	DPRequiredIDR       int64
	FullPaymentRequired bool

	PickupAt         time.Time
	ProductionStart  time.Time
	ProductionDate   clock.Date
	ShoppingCutoffAt time.Time
	DPDueAt          time.Time
	BalanceDueAt     time.Time

	// Notes are the customer's, such as the writing on the cake.
	Notes string
	// The customer as they were at checkout.
	CustomerName, CustomerPhone, CustomerEmail string

	TermsVersion    string
	TermsAcceptedAt time.Time
	CreatedAt       time.Time

	// Payments is the order's ledger, oldest first.
	Payments []payments.Payment
}

// Summary is an order as a list of orders shows it.
type Summary struct {
	ID        uuid.UUID
	Code      string
	Status    Status
	Payment   payments.Status
	PickupAt  time.Time
	TotalIDR  int64
	FirstItem string // such as "Donut Coklat"
	ItemCount int32
	CreatedAt time.Time
}

// Active reports whether the order is still going somewhere: it holds a
// place on its days, and a closed day with active orders is on hold (§15).
func (s Status) Active() bool { return !s.Terminal() }

// codeAlphabet has no look-alikes: no 0, 1, I, L, or O.
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// NewCode draws a random order code of six characters. At 31^6, about 887
// million codes, a clash is rare; the database's unique key catches one and
// the caller draws again.
func NewCode() string {
	b := make([]byte, 6)
	for i := range b {
		b[i] = codeAlphabet[randIndex(len(codeAlphabet))]
	}
	return string(b)
}

// randIndex returns a uniform random index below n, from crypto/rand.
func randIndex(n int) int {
	var buf [1]byte
	limit := 256 - 256%n // reject the top to keep the draw uniform
	for {
		if _, err := rand.Read(buf[:]); err != nil {
			panic("orders: crypto/rand failed: " + err.Error()) // cannot happen since Go 1.24
		}
		if int(buf[0]) < limit {
			return int(buf[0]) % n
		}
	}
}
