package orders

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/payments"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/audit"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
)

// Principals tells who is calling; identity.Service implements it.
type Principals interface {
	Principal(ctx context.Context) (identity.Principal, error)
}

// Who may do what (§8): the kitchen sees the orders and moves them through
// production; money (manual payments, forfeits, refunds) is the owner's.
var (
	owners = []identity.Role{identity.RoleOwner}
	staff  = []identity.Role{identity.RoleOwner, identity.RoleKitchen}
)

const (
	// maxStaffRange bounds one listing, in days of production.
	maxStaffRange = 92
	// staffListLimit bounds the rows of one listing.
	staffListLimit = 200
	// maxQuery bounds the search text of a listing.
	maxQuery = 100
	// maxReference bounds a transfer reference.
	maxReference = 100
)

// productionSteps are the statuses the kitchen moves an order to. Delivery
// arrives with Biteship (M6).
var productionSteps = []Status{InProduction, Ready, Completed}

// Errors of staff actions, in words the CMS shows as they are.
var (
	ErrNotPaidInFull = &apperr.PreconditionError{
		Reason:  "not_paid_in_full",
		Message: "Pesanan belum lunas. Kue hanya diproduksi dan diserahkan setelah lunas.",
	}
	ErrPaymentsClosed = &apperr.PreconditionError{
		Reason:  "payments_closed",
		Message: "Pembayaran pesanan ini sudah ditutup (DP hangus atau sudah dikembalikan). Kembalikan uangnya atau buat pesanan baru.",
	}
	ErrNothingOwed = &apperr.PreconditionError{
		Reason:  "nothing_owed",
		Message: "Pesanan ini sudah lunas.",
	}
	ErrBalanceNotDue = &apperr.PreconditionError{
		Reason:  "balance_not_due",
		Message: "Tenggat pelunasan belum lewat. DP baru hangus setelah tenggat.",
	}
	ErrNotForfeitable = &apperr.PreconditionError{
		Reason:  "not_forfeitable",
		Message: "DP hangus hanya untuk pesanan yang baru membayar DP.",
	}
	ErrNothingToRefund = &apperr.PreconditionError{
		Reason:  "nothing_to_refund",
		Message: "Belum ada pembayaran yang bisa dikembalikan.",
	}
	ErrAlreadyPaid = &apperr.PreconditionError{
		Reason:  "already_paid",
		Message: "Pesanan ini sudah dibayar. Batalkan dengan pengembalian dana, atau DP hangus setelah tenggat pelunasan.",
	}
)

// statusLabels name the order statuses as the CMS does.
var statusLabels = map[Status]string{
	AwaitingDP: "menunggu DP", Confirmed: "terkonfirmasi", Expired: "kedaluwarsa", InProduction: "diproduksi",
	Ready: "siap diambil", OutForDelivery: "diantar", Completed: "selesai", Cancelled: "dibatalkan",
}

// StaffSummary is an order as the staff's list shows it.
type StaffSummary struct {
	Summary
	ProductionDate              clock.Date
	CustomerName, CustomerPhone string
}

// StaffFilter narrows the staff's list of orders.
type StaffFilter struct {
	// From and To bound the production dates, both included.
	From, To clock.Date
	// Statuses keeps the orders in any of them; all when empty.
	Statuses []Status
	// Query keeps the orders whose code is it, or whose customer's phone
	// number or name contains it. A phone number may start with 0.
	Query string
}

// ManualPayment is money the owner received outside the payment provider,
// such as a transfer straight to the shop's account (§14).
type ManualPayment struct {
	AmountIDR int64
	// Reference is the proof, such as the transfer's reference number.
	Reference string
	// Note says what happened; it is the audit entry's reason.
	Note string
}

// CancelMode is how an order ends early.
type CancelMode string

// Ways to end an order early (§13, §14).
const (
	// CancelUnpaid ends an order still waiting for its DP: it expires, as
	// if its invoice had lapsed.
	CancelUnpaid CancelMode = "unpaid"
	// CancelForfeit ends an order whose balance was not paid by its
	// deadline. The DP is kept, as the terms say.
	CancelForfeit CancelMode = "forfeit"
	// CancelRefund ends an order and returns everything paid, such as when
	// the shop cannot bake it (§16). The owner transfers the money back
	// first and records the transfer's reference.
	CancelRefund CancelMode = "refund"
)

// CancelRequest ends an order early.
type CancelRequest struct {
	Mode CancelMode
	// Reason is the audit entry's reason.
	Reason string
	// RefundReference is the reference of the transfer back to the
	// customer. Required for CancelRefund, ignored otherwise.
	RefundReference string
}

// AdminDeps are what Admin works with.
type AdminDeps struct {
	Repo       Repository
	Ledger     Ledger
	Principals Principals
	Tx         Transactor
	Clock      clock.Clock
}

// Admin is the staff's side of orders in the CMS (§13, §14): listing them,
// moving them through production, and, for the owner, the money that moves
// outside the payment provider. Every change runs in one transaction with
// the order's row locked, and leaves its outbox events; changes of money also
// leave an audit entry (§22).
type Admin struct {
	repo       Repository
	ledger     Ledger
	principals Principals
	tx         Transactor
	clock      clock.Clock
}

// NewAdmin returns an Admin over d.
func NewAdmin(d AdminDeps) *Admin {
	return &Admin{repo: d.Repo, ledger: d.Ledger, principals: d.Principals, tx: d.Tx, clock: d.Clock}
}

func (a *Admin) authorize(ctx context.Context, roles []identity.Role) (identity.Principal, error) {
	p, err := a.principals.Principal(ctx)
	if err != nil {
		return identity.Principal{}, err
	}
	if !slices.ContainsFunc(roles, p.HasRole) {
		return identity.Principal{}, apperr.ErrForbidden
	}
	return p, nil
}

// List returns the orders produced in f's dates that match it, by pickup
// time, at most 200. Staff only.
func (a *Admin) List(ctx context.Context, f StaffFilter) ([]StaffSummary, error) {
	p, err := a.authorize(ctx, staff)
	if err != nil {
		return nil, err
	}
	f.Query = strings.TrimSpace(f.Query)
	v := apperr.Fields{}
	v.Check(!f.To.Before(f.From), "to_date", "Tanggal akhir tidak boleh sebelum tanggal awal.")
	v.Check(!f.To.After(f.From.AddDays(maxStaffRange)), "to_date", "Paling panjang 92 hari sekali tampil.")
	v.Check(utf8.RuneCountInString(f.Query) <= maxQuery, "query", "Pencarian paling panjang 100 karakter.")
	for _, s := range f.Statuses {
		v.Check(slices.Contains(Statuses, s), "statuses", "Status tidak dikenal.")
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	f.Query = phoneQuery(f.Query)
	return a.repo.ListStaff(ctx, p.TenantID, f, staffListLimit)
}

// phoneQuery turns a phone number as people write it, such as
// 0812-3456-789, into digits of the stored E.164 form (628123456789). Any
// other text is returned as it is.
func phoneQuery(q string) string {
	var b strings.Builder
	for _, r := range q {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' || r == '-' || r == ' ':
		default:
			return q
		}
	}
	digits := b.String()
	if len(digits) < 4 {
		return q
	}
	if strings.HasPrefix(digits, "0") {
		return "62" + digits[1:]
	}
	return digits
}

// Get returns the order with that code, with its payments. Staff only.
func (a *Admin) Get(ctx context.Context, code string) (Order, error) {
	p, err := a.authorize(ctx, staff)
	if err != nil {
		return Order{}, err
	}
	return a.load(ctx, p.TenantID, code)
}

// Advance moves an order through production: in_production, ready, then
// completed when the customer picks it up. Transition refuses a step the
// lifecycle does not have, and every one of these needs the order paid in
// full. Asking for the status the order already has changes nothing, so a
// second tap is harmless. Staff only.
func (a *Admin) Advance(ctx context.Context, code string, to Status) (Order, error) {
	p, err := a.authorize(ctx, staff)
	if err != nil {
		return Order{}, err
	}
	if !slices.Contains(productionSteps, to) {
		return Order{}, &apperr.ValidationError{Fields: map[string]string{"status": "Pilih diproduksi, siap diambil, atau selesai."}}
	}
	now := a.clock.Now()
	err = a.tx.Tx(ctx, func(ctx context.Context) error {
		o, err := a.lock(ctx, p.TenantID, code)
		if err != nil || o.Status == to {
			return err
		}
		if err := Transition(o.state(), to); err != nil {
			return transitionError(o.Status, to, err)
		}
		if err := a.repo.SetStatus(ctx, p.TenantID, o.ID, to, o.Payment, now); err != nil {
			return err
		}
		return a.publish(ctx, p.TenantID, o, "order.status_changed", now, map[string]any{
			"from": o.Status, "to": to, "by": p.AuthUserID,
		})
	})
	if err != nil {
		return Order{}, err
	}
	return a.load(ctx, p.TenantID, code)
}

// RecordPayment records money the owner received outside the provider
// (§14). A waiting order needs at least its DP and is confirmed by it; a
// confirmed order takes its balance, whole or in part. Overpaying, paying an
// order that ended, and paying into closed payments are refused. The payment,
// the new statuses, the expiry of the order's open invoices, the audit entry,
// and the events commit together. Owner only.
func (a *Admin) RecordPayment(ctx context.Context, code string, in ManualPayment) (Order, error) {
	p, err := a.authorize(ctx, owners)
	if err != nil {
		return Order{}, err
	}
	in.Reference, in.Note = strings.TrimSpace(in.Reference), strings.TrimSpace(in.Note)
	v := apperr.Fields{}
	v.Check(in.AmountIDR > 0 && in.AmountIDR <= payments.MaxOrderTotalIDR, "amount_idr", "Isi nominal yang diterima.")
	checkReference(v, "reference", in.Reference, "Isi nomor referensi atau bukti transfer.")
	checkReason(v, "note", in.Note, "Isi catatan pembayaran.")
	if err := v.Err(); err != nil {
		return Order{}, err
	}
	now := a.clock.Now()
	err = a.tx.Tx(ctx, func(ctx context.Context) error {
		o, err := a.lock(ctx, p.TenantID, code)
		if err != nil {
			return err
		}
		ps, err := a.ledger.OrderPayments(ctx, p.TenantID, o.ID)
		if err != nil {
			return err
		}
		paid := payments.Paid(ps)
		kind, err := manualKind(o, paid, in.AmountIDR)
		if err != nil {
			return err
		}
		payment := payments.Payment{
			ID: uuid.New(), OrderID: o.ID, Kind: kind, AmountIDR: in.AmountIDR,
			Manual: &payments.ManualProof{Reference: in.Reference, Note: in.Note, RecordedBy: p.AuthUserID},
		}
		pay, err := payments.Settle(o.Payment, payments.Amounts{TotalIDR: o.TotalIDR, DPRequiredIDR: o.DPRequiredIDR, PaidIDR: paid + in.AmountIDR})
		if err != nil {
			return err // manualKind refused closed payments already
		}
		status := o.Status
		if status == AwaitingDP {
			if err := Transition(State{Status: status, Payment: pay, Fulfillment: o.Fulfillment}, Confirmed); err != nil {
				return err // manualKind asked for the DP at least
			}
			status = Confirmed
		}
		if err := a.ledger.AddManual(ctx, p.TenantID, payment, now); err != nil {
			return err
		}
		if err := a.settle(ctx, p.TenantID, o, status, pay, now); err != nil {
			return err
		}
		if err := a.repo.Audit(ctx, audit.Entry{
			TenantID: p.TenantID, ActorID: p.AuthUserID, Action: "orders.payment.recorded_manually",
			Entity: "order", EntityID: o.ID, Reason: in.Note, At: now,
			Before: map[string]any{"status": o.Status, "payment_status": o.Payment, "paid_idr": paid},
			After: map[string]any{
				"status": status, "payment_status": pay, "paid_idr": paid + in.AmountIDR,
				"payment_id": payment.ID, "kind": kind, "amount_idr": in.AmountIDR, "reference": in.Reference,
			},
		}); err != nil {
			return err
		}
		if err := a.publish(ctx, p.TenantID, o, "order.payment_received", now, map[string]any{
			"payment_id": payment.ID, "kind": kind, "amount_idr": in.AmountIDR, "payment_status": pay, "manual": true,
		}); err != nil {
			return err
		}
		if status == o.Status {
			return nil
		}
		return a.publish(ctx, p.TenantID, o, "order.confirmed", now, nil)
	})
	if err != nil {
		return Order{}, err
	}
	return a.load(ctx, p.TenantID, code)
}

// manualKind decides what a manual payment of amount is for, or why the
// order takes none.
func manualKind(o Order, paid, amount int64) (payments.Kind, error) {
	owed := o.TotalIDR - paid
	switch {
	case o.Payment.Closed():
		return "", ErrPaymentsClosed
	case owed <= 0:
		return "", ErrNothingOwed
	case o.Status != AwaitingDP && o.Status != Confirmed:
		return "", &apperr.PreconditionError{
			Reason:  "order_not_open",
			Message: fmt.Sprintf("Pesanan berstatus %s tidak menerima pembayaran.", statusLabels[o.Status]),
		}
	case amount > owed:
		return "", &apperr.ValidationError{Fields: map[string]string{
			"amount_idr": "Melebihi sisa tagihan " + formatIDR(owed) + ".",
		}}
	case o.Status == Confirmed:
		return payments.KindBalance, nil
	case paid+amount < o.DPRequiredIDR:
		return "", &apperr.ValidationError{Fields: map[string]string{
			"amount_idr": "Kurang dari DP " + formatIDR(o.DPRequiredIDR) + ". Pesanan baru terkonfirmasi setelah DP masuk.",
		}}
	case paid+amount == o.TotalIDR:
		return payments.KindFull, nil
	default:
		return payments.KindDP, nil
	}
}

// Cancel ends an order early (§13, §14). An order waiting for its DP
// expires. A confirmed order is cancelled: with its DP forfeited once the
// balance deadline has passed, or with everything paid refunded, recorded as
// a negative manual payment. An order in production or later is not
// cancelled here. The order's open invoices expire with it. Owner only.
func (a *Admin) Cancel(ctx context.Context, code string, in CancelRequest) (Order, error) {
	p, err := a.authorize(ctx, owners)
	if err != nil {
		return Order{}, err
	}
	in.Reason, in.RefundReference = strings.TrimSpace(in.Reason), strings.TrimSpace(in.RefundReference)
	v := apperr.Fields{}
	v.Check(in.Mode == CancelUnpaid || in.Mode == CancelForfeit || in.Mode == CancelRefund, "mode", "Pilih cara pembatalan.")
	checkReason(v, "reason", in.Reason, "Isi alasan pembatalan.")
	if in.Mode == CancelRefund {
		checkReference(v, "refund_reference", in.RefundReference, "Isi nomor referensi transfer pengembalian dana.")
	}
	if err := v.Err(); err != nil {
		return Order{}, err
	}
	now := a.clock.Now()
	err = a.tx.Tx(ctx, func(ctx context.Context) error {
		o, err := a.lock(ctx, p.TenantID, code)
		if err != nil {
			return err
		}
		ps, err := a.ledger.OrderPayments(ctx, p.TenantID, o.ID)
		if err != nil {
			return err
		}
		paid := payments.Paid(ps)
		status, pay, err := cancelled(o, in.Mode, now)
		if err != nil {
			return err
		}
		if in.Mode == CancelRefund {
			if err := a.ledger.AddManual(ctx, p.TenantID, payments.Payment{
				ID: uuid.New(), OrderID: o.ID, Kind: payments.KindRefund, AmountIDR: -paid,
				Manual: &payments.ManualProof{Reference: in.RefundReference, Note: in.Reason, RecordedBy: p.AuthUserID},
			}, now); err != nil {
				return err
			}
		}
		if err := a.settle(ctx, p.TenantID, o, status, pay, now); err != nil {
			return err
		}
		after := map[string]any{"status": status, "payment_status": pay, "mode": in.Mode}
		if in.Mode == CancelRefund {
			after["refunded_idr"], after["reference"] = paid, in.RefundReference
		}
		if err := a.repo.Audit(ctx, audit.Entry{
			TenantID: p.TenantID, ActorID: p.AuthUserID, Action: "orders.order.cancelled",
			Entity: "order", EntityID: o.ID, Reason: in.Reason, At: now,
			Before: map[string]any{"status": o.Status, "payment_status": o.Payment, "paid_idr": paid},
			After:  after,
		}); err != nil {
			return err
		}
		event := "order.cancelled"
		if status == Expired {
			event = "order.expired"
		}
		return a.publish(ctx, p.TenantID, o, event, now, map[string]any{"mode": in.Mode, "payment_status": pay})
	})
	if err != nil {
		return Order{}, err
	}
	return a.load(ctx, p.TenantID, code)
}

// cancelled returns where an order ends up when cancelled in mode, or why it
// cannot be.
func cancelled(o Order, mode CancelMode, now time.Time) (Status, payments.Status, error) {
	if mode == CancelUnpaid {
		if o.Status == Confirmed {
			return "", "", ErrAlreadyPaid
		}
		if err := Transition(o.state(), Expired); err != nil {
			return "", "", transitionError(o.Status, Expired, err)
		}
		return Expired, o.Payment, nil
	}
	closing := payments.Forfeited
	if mode == CancelRefund {
		closing = payments.Refunded
	}
	pay, err := payments.Close(o.Payment, closing)
	if err != nil {
		if closing == payments.Forfeited {
			return "", "", ErrNotForfeitable
		}
		return "", "", ErrNothingToRefund
	}
	if err := Transition(State{Status: o.Status, Payment: pay, Fulfillment: o.Fulfillment}, Cancelled); err != nil {
		return "", "", transitionError(o.Status, Cancelled, err)
	}
	if closing == payments.Forfeited && now.Before(o.BalanceDueAt) {
		return "", "", ErrBalanceNotDue
	}
	return Cancelled, pay, nil
}

// settle stores an order's new statuses and expires its open invoices,
// which nobody should pay any more: the order was paid another way, or it
// ended. A confirmed order gets its balance invoice from the worker (M2.7).
func (a *Admin) settle(ctx context.Context, tenant uuid.UUID, o Order, status Status, pay payments.Status, at time.Time) error {
	if err := a.repo.SetStatus(ctx, tenant, o.ID, status, pay, at); err != nil {
		return err
	}
	_, err := a.ledger.ExpirePending(ctx, tenant, o.ID, at)
	return err
}

// publish appends an order event carrying what every consumer needs: the
// order and its batch.
func (a *Admin) publish(ctx context.Context, tenant uuid.UUID, o Order, typ string, at time.Time, extra map[string]any) error {
	payload := map[string]any{"order_id": o.ID, "code": o.Code, "production_date": o.ProductionDate.String()}
	maps.Copy(payload, extra)
	return a.repo.Publish(ctx, outbox.Event{TenantID: tenant, Aggregate: "order", Type: typ, Payload: payload, At: at})
}

// lock reads the order with that code and holds its row until the
// transaction ends, so two staff acting on it at once take turns.
func (a *Admin) lock(ctx context.Context, tenant uuid.UUID, code string) (Order, error) {
	code, ok := normalizeCode(code)
	if !ok {
		return Order{}, apperr.ErrNotFound
	}
	return a.repo.LockByCode(ctx, tenant, code)
}

func (a *Admin) load(ctx context.Context, tenant uuid.UUID, code string) (Order, error) {
	code, ok := normalizeCode(code)
	if !ok {
		return Order{}, apperr.ErrNotFound
	}
	o, err := a.repo.GetByCode(ctx, tenant, code)
	if err != nil {
		return Order{}, err
	}
	if o.Payments, err = a.ledger.OrderPayments(ctx, tenant, o.ID); err != nil {
		return Order{}, err
	}
	return o, nil
}

// transitionError says in the CMS's words why an order cannot move.
func transitionError(from, to Status, err error) error {
	if errors.Is(err, ErrPaymentRequired) && to != Expired {
		return ErrNotPaidInFull
	}
	action := "diubah menjadi " + statusLabels[to]
	if to == Cancelled || to == Expired {
		action = "dibatalkan"
	}
	return &apperr.PreconditionError{
		Reason:  "illegal_transition",
		Message: fmt.Sprintf("Pesanan berstatus %s tidak bisa %s.", statusLabels[from], action),
	}
}

func checkReference(v apperr.Fields, field, ref, missing string) {
	v.Check(ref != "", field, missing)
	v.Check(utf8.RuneCountInString(ref) <= maxReference, field, "Paling panjang 100 karakter.")
}

func checkReason(v apperr.Fields, field, reason, missing string) {
	v.Check(reason != "", field, missing)
	v.Check(utf8.RuneCountInString(reason) <= maxNotes, field, "Paling panjang 500 karakter.")
}

// normalizeCode reads an order code as people type it.
func normalizeCode(code string) (string, bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	return code, len(code) == 6
}

func (o Order) state() State {
	return State{Status: o.Status, Payment: o.Payment, Fulfillment: o.Fulfillment}
}
