package orders

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/payments"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/scheduling"
)

// Catalog is what checkout reads from the catalog; catalog.Reader implements it.
type Catalog interface {
	Variants(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]catalog.VariantSummary, error)
	IngredientCosts(ctx context.Context, tenantID uuid.UUID, variantIDs []uuid.UUID) (map[uuid.UUID]catalog.IngredientCost, error)
}

// Schedules is what checkout reads from scheduling; scheduling.Reader implements it.
type Schedules interface {
	Settings(ctx context.Context, tenantID uuid.UUID) (scheduling.Settings, error)
	ClosedDates(ctx context.Context, tenantID uuid.UUID, from, to clock.Date) (map[clock.Date]string, error)
}

// Policies is what checkout reads from payments; payments.Reader implements it.
type Policies interface {
	Policy(ctx context.Context, tenantID uuid.UUID) (payments.Policy, error)
}

// Customers is what checkout needs from identity; identity.Service implements it.
type Customers interface {
	Principal(ctx context.Context) (identity.Principal, error)
	EnsureCustomer(ctx context.Context, in identity.CustomerInput, at time.Time) (identity.Customer, error)
}

// Ledger is what checkout needs from the payment ledger; payments.Ledger implements it.
type Ledger interface {
	AddPending(ctx context.Context, tenantID uuid.UUID, p payments.Payment, at time.Time) error
	AttachInvoice(ctx context.Context, tenantID, paymentID uuid.UUID, inv payments.Invoice, at time.Time) error
	OrderPayments(ctx context.Context, tenantID, orderID uuid.UUID) ([]payments.Payment, error)
}

// Transactor runs a unit of work across modules in one transaction;
// *db.DB implements it (platform/db.Tx).
type Transactor interface {
	Tx(ctx context.Context, fn func(ctx context.Context) error) error
}

// Repository is the persistence port of orders. It works in the caller's
// transaction when there is one.
type Repository interface {
	// BatchCutoffs returns, for each production date from..to with orders
	// in any of statuses, the earliest shopping cutoff among them.
	BatchCutoffs(ctx context.Context, tenantID uuid.UUID, from, to clock.Date, statuses []Status) (map[clock.Date]time.Time, error)
	// LockCustomer serializes one customer's checkouts until the
	// transaction ends.
	LockCustomer(ctx context.Context, customerID uuid.UUID) error
	// FindByIdempotencyKey returns apperr.ErrNotFound when the customer
	// never used key.
	FindByIdempotencyKey(ctx context.Context, tenantID, customerID, key uuid.UUID) (uuid.UUID, error)
	CountByStatus(ctx context.Context, tenantID, customerID uuid.UUID, status Status) (int, error)
	// Insert stores an order, its items, and its order.placed outbox event.
	// It returns false, storing nothing, when o.Code is already taken.
	Insert(ctx context.Context, tenantID uuid.UUID, o Order, idempotencyKey uuid.UUID, at time.Time) (bool, error)
	// Get and FindCustomerOrder return the order with its items but not its
	// payments, or apperr.ErrNotFound.
	Get(ctx context.Context, tenantID, id uuid.UUID) (Order, error)
	FindCustomerOrder(ctx context.Context, tenantID, customerID uuid.UUID, code string) (Order, error)
	// ListCustomerOrders returns a customer's orders, newest first.
	ListCustomerOrders(ctx context.Context, tenantID, customerID uuid.UUID, limit int32) ([]Summary, error)
	// ActiveOrderDays counts, for each day from..to, the orders in statuses
	// produced or picked up that day.
	ActiveOrderDays(ctx context.Context, tenantID uuid.UUID, from, to clock.Date, statuses []Status) (map[clock.Date]int, error)
}

// Cart limits.
const (
	maxCartLines   = 30
	maxLineItems   = 1000
	suggestHorizon = scheduling.SuggestDays + 1
)

// ItemRequest is one line of a cart.
type ItemRequest struct {
	VariantID uuid.UUID
	Quantity  int32
}

// QuoteRequest is a cart and the pickup time the customer wants.
type QuoteRequest struct {
	Items    []ItemRequest
	PickupAt time.Time
}

// QuotedItem is a cart line priced by the server.
type QuotedItem struct {
	VariantID         uuid.UUID
	ProductName       string
	VariantName       string
	Quantity          int32
	UnitPriceIDR      int64
	LineTotalIDR      int64
	ProductionMinutes int32
	MinNoticeHours    int32
}

// Quote is what an order would be, all computed on the server: prices,
// the down payment, and the schedule (§14, §15). It saves nothing.
type Quote struct {
	Items       []QuotedItem
	SubtotalIDR int64
	TaxIDR      int64 // 0 until the tax policy is set (§27)
	TotalIDR    int64
	// Schedule is set when the pickup time works. Otherwise Rejection says
	// why, and Suggestion, when set, is the nearest pickup that works.
	Schedule   *scheduling.Plan
	Rejection  *scheduling.Rejection
	Suggestion *scheduling.Plan
	// DPRequiredIDR is the first payment of an order paid with a DP; it is
	// the total when the schedule requires full payment. Set with Schedule.
	DPRequiredIDR int64
	// TermsVersion is the version of the terms the customer accepts by
	// placing the order.
	TermsVersion string
}

// Deps are what Checkout works with.
type Deps struct {
	Catalog   Catalog
	Schedules Schedules
	Policies  Policies
	Repo      Repository
	Customers Customers
	Ledger    Ledger
	Provider  payments.Provider
	Tx        Transactor
	Tenants   identity.TenantResolver
	Clock     clock.Clock
	Logger    *slog.Logger
}

// Checkout prices carts, places orders, and shows customers their orders.
// Quotes are for anyone, signed in or not, so the tenant comes from the
// request; placing an order needs a customer signed in with WhatsApp.
type Checkout struct {
	catalog   Catalog
	schedules Schedules
	policies  Policies
	repo      Repository
	customers Customers
	ledger    Ledger
	provider  payments.Provider
	tx        Transactor
	tenants   identity.TenantResolver
	clock     clock.Clock
	logger    *slog.Logger
}

// NewCheckout returns a Checkout over d.
func NewCheckout(d Deps) *Checkout {
	return &Checkout{
		catalog: d.Catalog, schedules: d.Schedules, policies: d.Policies, repo: d.Repo, customers: d.Customers,
		ledger: d.Ledger, provider: d.Provider, tx: d.Tx, tenants: d.Tenants, clock: d.Clock, logger: d.Logger,
	}
}

// committed are the statuses whose orders fix a batch's shopping cutoff.
func committed() []Status {
	return slices.DeleteFunc(slices.Clone(Statuses), func(s Status) bool { return !s.CountsForProduction() })
}

// Quote prices a cart and checks its pickup time. A pickup time that does
// not work is part of the quote, not an error; a cart that cannot be
// ordered is a *apperr.ValidationError on "items" or "pickup_at".
func (c *Checkout) Quote(ctx context.Context, req QuoteRequest) (Quote, error) {
	if err := validateCart(req); err != nil {
		return Quote{}, err
	}
	tenant := c.tenants.TenantID(ctx)
	q, err := c.price(ctx, tenant, req.Items)
	if err != nil {
		return Quote{}, err
	}
	q.TermsVersion = TermsVersion

	policy, err := c.policies.Policy(ctx, tenant)
	if err != nil {
		return Quote{}, err
	}
	ingredients, err := c.ingredientCost(ctx, tenant, req.Items)
	if err != nil {
		return Quote{}, err
	}
	dp, err := payments.DPRequired(policy, q.TotalIDR, ingredients)
	if err != nil {
		return Quote{}, err
	}

	settings, err := c.schedules.Settings(ctx, tenant)
	if err != nil {
		return Quote{}, err
	}
	from := clock.DateOf(req.PickupAt).AddDays(-1)
	to := from.AddDays(suggestHorizon + 1)
	closed, err := c.schedules.ClosedDates(ctx, tenant, from, to)
	if err != nil {
		return Quote{}, err
	}
	cutoffs, err := c.repo.BatchCutoffs(ctx, tenant, from, to, committed())
	if err != nil {
		return Quote{}, err
	}
	sr := scheduling.Request{
		Now: c.clock.Now(), PickupAt: req.PickupAt, Closed: closed, BatchCutoffs: cutoffs,
		BalanceDueHoursBefore: policy.BalanceDueHoursBefore, DPValidFor: policy.DPInvoiceValidFor(),
	}
	for _, it := range q.Items {
		sr.Items = append(sr.Items, scheduling.Item{ProductionMinutes: it.ProductionMinutes, MinNoticeHours: it.MinNoticeHours})
	}

	plan, err := settings.Plan(sr)
	var rejection *scheduling.Rejection
	switch {
	case errors.As(err, &rejection):
		q.Rejection = rejection
		if suggestion, ok := settings.Suggest(sr); ok {
			q.Suggestion = &suggestion
		}
	case err != nil:
		return Quote{}, err
	default:
		q.Schedule = &plan
		q.DPRequiredIDR = dp
		if plan.FullPaymentRequired {
			q.DPRequiredIDR = q.TotalIDR
		}
	}
	return q, nil
}

func validateCart(req QuoteRequest) error {
	f := apperr.Fields{}
	f.Check(len(req.Items) > 0, "items", "Keranjang masih kosong.")
	f.Check(len(req.Items) <= maxCartLines, "items", "Paling banyak 30 jenis kue dalam satu pesanan.")
	seen := map[uuid.UUID]bool{}
	for _, it := range req.Items {
		f.Check(it.VariantID != uuid.Nil, "items", "Pilih kuenya.")
		f.Check(!seen[it.VariantID], "items", "Kue yang sama muncul dua kali di keranjang; gabungkan jumlahnya.")
		f.Check(it.Quantity >= 1 && it.Quantity <= maxLineItems, "items", "Jumlah setiap kue 1 sampai 1000.")
		seen[it.VariantID] = true
	}
	f.Check(!req.PickupAt.IsZero(), "pickup_at", "Pilih tanggal dan jam pengambilan.")
	return f.Err()
}

// price reads every variant from the catalog and prices the cart with the
// server's prices; nothing the client sends about money is used.
func (c *Checkout) price(ctx context.Context, tenant uuid.UUID, items []ItemRequest) (Quote, error) {
	ids := make([]uuid.UUID, len(items))
	for i, it := range items {
		ids[i] = it.VariantID
	}
	variants, err := c.catalog.Variants(ctx, tenant, ids)
	if err != nil {
		return Quote{}, err
	}
	byID := make(map[uuid.UUID]catalog.VariantSummary, len(variants))
	for _, v := range variants {
		byID[v.ID] = v
	}

	var q Quote
	for _, it := range items {
		v, ok := byID[it.VariantID]
		if !ok || !v.OnSale() {
			return Quote{}, &apperr.ValidationError{Fields: map[string]string{"items": "Ada kue yang sudah tidak dijual. Muat ulang keranjang."}}
		}
		line := int64(it.Quantity) * v.PriceIDR
		q.Items = append(q.Items, QuotedItem{
			VariantID: v.ID, ProductName: v.ProductName, VariantName: v.Name, Quantity: it.Quantity,
			UnitPriceIDR: v.PriceIDR, LineTotalIDR: line, ProductionMinutes: v.ProductionMinutes, MinNoticeHours: v.MinNoticeHours,
		})
		q.SubtotalIDR += line
	}
	q.TotalIDR = q.SubtotalIDR + q.TaxIDR
	f := apperr.Fields{}
	f.Check(q.TotalIDR > 0, "items", "Total pesanan tidak boleh Rp0.")
	f.Check(q.TotalIDR <= payments.MaxOrderTotalIDR, "items", "Total pesanan terlalu besar.")
	return q, f.Err()
}

// ingredientCost estimates the cart's ingredients for the DP. Ingredients
// without a pack price are left out, and logged so the owner can fill them in.
func (c *Checkout) ingredientCost(ctx context.Context, tenant uuid.UUID, items []ItemRequest) (int64, error) {
	ids := make([]uuid.UUID, len(items))
	for i, it := range items {
		ids[i] = it.VariantID
	}
	costs, err := c.catalog.IngredientCosts(ctx, tenant, ids)
	if err != nil {
		return 0, err
	}
	var total int64
	var unpriced []uuid.UUID
	for _, it := range items {
		cost := costs[it.VariantID]
		total += int64(it.Quantity) * cost.PerItemIDR
		for _, ing := range cost.Unpriced {
			if !slices.Contains(unpriced, ing) {
				unpriced = append(unpriced, ing)
			}
		}
	}
	if len(unpriced) > 0 {
		c.logger.WarnContext(ctx, "DP estimate leaves out ingredients without a pack price", slog.Any("ingredient_ids", unpriced))
	}
	return total, nil
}
