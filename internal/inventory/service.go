package inventory

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/audit"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
)

// Movement is one entry of the stock ledger.
type Movement struct {
	LotID        uuid.UUID
	IngredientID uuid.UUID
	Kind         MovementKind
	Qty          int64      // positive in, negative out
	BatchID      *uuid.UUID // consume only (M3.3)
	Reason       string     // required for waste and adjust
	ActorID      uuid.UUID
	At           time.Time
}

// Check is the kitchen's look at a lot.
type Check struct {
	LotID     uuid.UUID
	OK        bool
	Reason    DiscardReason // when not OK
	Note      string
	CheckedBy uuid.UUID
	At        time.Time
}

// Repository is the persistence port of inventory. It works in the
// caller's transaction.
type Repository interface {
	// LockIngredient serializes the changes to one ingredient's stock until
	// the transaction ends.
	LockIngredient(ctx context.Context, ingredientID uuid.UUID) error
	InsertLot(ctx context.Context, tenantID uuid.UUID, l Lot, createdBy uuid.UUID, at time.Time) error
	InsertMovement(ctx context.Context, tenantID uuid.UUID, m Movement) error
	InsertCheck(ctx context.Context, tenantID uuid.UUID, c Check) error
	SetLotStatus(ctx context.Context, tenantID, lotID uuid.UUID, s LotStatus, at time.Time) error
	// Lots returns the lots of the ingredients (all when none) in any of
	// statuses, oldest first, with their balance and latest ok check.
	Lots(ctx context.Context, tenantID uuid.UUID, ingredientIDs []uuid.UUID, statuses []LotStatus) ([]Lot, error)
	// GetLot returns apperr.ErrNotFound.
	GetLot(ctx context.Context, tenantID, lotID uuid.UUID) (Lot, error)
	// ExpireLots marks the available lots past their expiry at now and
	// returns how many.
	ExpireLots(ctx context.Context, tenantID uuid.UUID, now time.Time) (int64, error)
	Audit(ctx context.Context, e audit.Entry) error
	Publish(ctx context.Context, e outbox.Event) error
}

// Catalog is what inventory reads from the catalog; catalog.Reader
// implements it.
type Catalog interface {
	Labels(ctx context.Context, tenantID uuid.UUID) (catalog.Labels, error)
}

// Principals tells who is calling; identity.Service implements it.
type Principals interface {
	Principal(ctx context.Context) (identity.Principal, error)
}

// Transactor runs a unit of work in one transaction; *db.DB implements it.
type Transactor interface {
	Tx(ctx context.Context, fn func(ctx context.Context) error) error
}

// staff keep the stock (§8): ibu receives, checks, and counts it.
var staff = []identity.Role{identity.RoleOwner, identity.RoleKitchen}

// receivedWithin bounds how far back a delivery may be recorded.
const receivedWithin = 30 * 24 * time.Hour

// counted are the lot statuses that may still hold something.
var counted = []LotStatus{LotAvailable, LotExpired}

// ServiceDeps are what Service works with.
type ServiceDeps struct {
	Repo       Repository
	Catalog    Catalog
	Principals Principals
	Tx         Transactor
	Clock      clock.Clock
}

// Service is the kitchen's stock in the PWA: deliveries in, checks of what
// is left, and stock counts. Every change runs in one transaction under the
// ingredient's lock, is audited (§22), and tells aggregation the stock
// changed, so the shopping lists follow.
type Service struct {
	repo       Repository
	catalog    Catalog
	principals Principals
	tx         Transactor
	clock      clock.Clock
}

// NewService returns a Service over d.
func NewService(d ServiceDeps) *Service {
	return &Service{repo: d.Repo, catalog: d.Catalog, principals: d.Principals, tx: d.Tx, clock: d.Clock}
}

func (s *Service) authorize(ctx context.Context) (identity.Principal, error) {
	p, err := s.principals.Principal(ctx)
	if err != nil {
		return identity.Principal{}, err
	}
	if !slices.ContainsFunc(staff, p.HasRole) {
		return identity.Principal{}, apperr.ErrForbidden
	}
	return p, nil
}

// ReceiveInput is a delivery of one ingredient.
type ReceiveInput struct {
	IngredientID uuid.UUID
	// Qty is in the ingredient's base unit, such as 2000 for 2 kg of flour.
	Qty int64
	// ReceivedAt defaults to now; it may be up to 30 days back.
	ReceivedAt time.Time
	// ExpiresAt defaults to ReceivedAt plus the ingredient's shelf life.
	ExpiresAt *time.Time
	Note      string
}

// Receive records a delivery as a new lot ("Belanja masuk"). Staff only.
func (s *Service) Receive(ctx context.Context, in ReceiveInput) (Lot, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return Lot{}, err
	}
	labels, err := s.catalog.Labels(ctx, p.TenantID)
	if err != nil {
		return Lot{}, err
	}
	now := s.clock.Now()
	if in.ReceivedAt.IsZero() {
		in.ReceivedAt = now
	}
	in.Note = strings.TrimSpace(in.Note)
	ing, known := labels.Ingredients[in.IngredientID]
	f := apperr.Fields{}
	f.Check(known, "ingredient_id", "Bahan tidak dikenal.")
	f.Check(in.Qty > 0 && in.Qty <= maxQty, "qty", "Isi jumlah yang diterima.")
	f.Check(!in.ReceivedAt.After(now.Add(5*time.Minute)), "received_at", "Tanggal belanja tidak boleh di masa depan.")
	f.Check(!in.ReceivedAt.Before(now.Add(-receivedWithin)), "received_at", "Belanja paling lama 30 hari yang lalu.")
	f.Check(in.ExpiresAt == nil || in.ExpiresAt.After(in.ReceivedAt), "expires_at", "Tanggal kedaluwarsa harus setelah tanggal belanja.")
	f.Check(utf8.RuneCountInString(in.Note) <= maxNote, "note", "Catatan paling panjang 500 karakter.")
	if err := f.Err(); err != nil {
		return Lot{}, err
	}
	expires := expiry(ing, in.ReceivedAt, in.ExpiresAt)
	lot := Lot{
		ID: uuid.New(), IngredientID: in.IngredientID, ReceivedAt: in.ReceivedAt, ExpiresAt: expires,
		Status: LotAvailable, Source: SourceManual, Note: in.Note,
	}
	reason := in.Note
	if reason == "" {
		reason = "Belanja masuk"
	}
	err = s.tx.Tx(ctx, func(ctx context.Context) error {
		return intake(ctx, s.repo, p.TenantID, ing, lot, in.Qty, p.AuthUserID, reason, now)
	})
	if err != nil {
		return Lot{}, err
	}
	return s.repo.GetLot(ctx, p.TenantID, lot.ID)
}

// intake records a delivery as the new lot l under the ingredient's lock:
// its receive movement, its audit entry, and the stock change. A perishable
// delivery is fresh from the shop, so it counts as checked "masih bagus" at
// its arrival (decided 2026-09-29): it counts at once, and needs a new check
// 24 hours later like any other (§12).
func intake(ctx context.Context, repo Repository, tenant uuid.UUID, ing catalog.Ingredient, l Lot, qty int64, actor uuid.UUID, reason string, now time.Time) error {
	if err := repo.LockIngredient(ctx, l.IngredientID); err != nil {
		return err
	}
	if err := repo.InsertLot(ctx, tenant, l, actor, now); err != nil {
		return err
	}
	if err := repo.InsertMovement(ctx, tenant, Movement{
		LotID: l.ID, IngredientID: l.IngredientID, Kind: MoveReceive, Qty: qty, ActorID: actor, At: now,
	}); err != nil {
		return err
	}
	if ing.LeftoverPolicy == catalog.LeftoverConfirm {
		if err := repo.InsertCheck(ctx, tenant, Check{LotID: l.ID, OK: true, CheckedBy: actor, At: l.ReceivedAt}); err != nil {
			return err
		}
	}
	after := map[string]any{"ingredient_id": l.IngredientID, "qty": qty, "received_at": l.ReceivedAt, "expires_at": l.ExpiresAt}
	if l.ProcurementItemID != nil {
		after["procurement_item_id"] = *l.ProcurementItemID
	}
	if err := repo.Audit(ctx, audit.Entry{
		TenantID: tenant, ActorID: actor, Action: "inventory.stock.received", Entity: "stock_lot", EntityID: l.ID,
		After: after, Reason: reason, At: now,
	}); err != nil {
		return err
	}
	return repo.Publish(ctx, stockChanged(tenant, l.IngredientID, now))
}

// expiry is when a delivery of ing received at expires: given, or its shelf
// life after it; nil when neither is known.
func expiry(ing catalog.Ingredient, receivedAt time.Time, given *time.Time) *time.Time {
	if given != nil {
		return given
	}
	if ing.ShelfLifeDays == nil {
		return nil
	}
	e := receivedAt.AddDate(0, 0, int(*ing.ShelfLifeDays))
	return &e
}

// Delivery is what arrived for one item of a purchase order.
type Delivery struct {
	IngredientID      uuid.UUID
	Qty               int64
	ReceivedAt        time.Time
	ExpiresAt         *time.Time // nil: the shelf life after ReceivedAt
	ProcurementItemID uuid.UUID
	Note              string
}

// Receiver puts what a purchase order brought into the stock (M4). It acts
// for a person procurement already authorized, in procurement's
// transaction.
type Receiver struct {
	repo    Repository
	catalog Catalog
}

// NewReceiver returns a Receiver over repo.
func NewReceiver(repo Repository, cat Catalog) *Receiver {
	return &Receiver{repo: repo, catalog: cat}
}

// ReceiveOrdered records a delivery as a new lot from procurement, as
// "Belanja masuk" does.
func (r *Receiver) ReceiveOrdered(ctx context.Context, tenant uuid.UUID, d Delivery, actor uuid.UUID, at time.Time) (Lot, error) {
	labels, err := r.catalog.Labels(ctx, tenant)
	if err != nil {
		return Lot{}, err
	}
	ing, ok := labels.Ingredients[d.IngredientID]
	if !ok || d.Qty <= 0 || d.Qty > maxQty {
		return Lot{}, fmt.Errorf("delivery of %d of ingredient %s", d.Qty, d.IngredientID)
	}
	if d.ExpiresAt != nil && !d.ExpiresAt.After(d.ReceivedAt) {
		return Lot{}, &apperr.ValidationError{Fields: map[string]string{"expires_at": "Tanggal kedaluwarsa harus setelah tanggal terima."}}
	}
	item := d.ProcurementItemID
	lot := Lot{
		ID: uuid.New(), IngredientID: d.IngredientID, ReceivedAt: d.ReceivedAt, ExpiresAt: expiry(ing, d.ReceivedAt, d.ExpiresAt),
		Status: LotAvailable, Source: SourceProcurement, ProcurementItemID: &item, Note: d.Note,
	}
	reason := d.Note
	if reason == "" {
		reason = "Diterima dari pesanan pembelian"
	}
	if err := intake(ctx, r.repo, tenant, ing, lot, d.Qty, actor, reason, at); err != nil {
		return Lot{}, err
	}
	return r.repo.GetLot(ctx, tenant, lot.ID)
}

// IngredientStock is one ingredient's stock as the PWA shows it.
type IngredientStock struct {
	IngredientID uuid.UUID
	Name         string
	BaseUnit     catalog.BaseUnit
	Policy       catalog.LeftoverPolicy
	// Total is what the ledger holds, counted or not.
	Total int64
	// UsableToday is what a batch made today may count on (§12).
	UsableToday int64
	// Lots are those with something left, oldest first.
	Lots []Lot
}

// Stock returns every ingredient's stock, by name. Staff only.
func (s *Service) Stock(ctx context.Context) ([]IngredientStock, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return nil, err
	}
	return s.stock(ctx, p.TenantID, nil)
}

func (s *Service) stock(ctx context.Context, tenant uuid.UUID, only []uuid.UUID) ([]IngredientStock, error) {
	labels, err := s.catalog.Labels(ctx, tenant)
	if err != nil {
		return nil, err
	}
	lots, err := s.repo.Lots(ctx, tenant, only, counted)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	byIngredient := map[uuid.UUID]*IngredientStock{}
	for id, ing := range labels.Ingredients {
		if len(only) > 0 && !slices.Contains(only, id) {
			continue
		}
		byIngredient[id] = &IngredientStock{IngredientID: id, Name: ing.Name, BaseUnit: ing.BaseUnit, Policy: ing.LeftoverPolicy}
	}
	for _, l := range lots {
		st, ok := byIngredient[l.IngredientID]
		if !ok || l.Balance <= 0 {
			continue
		}
		st.Total += l.Balance
		st.Lots = append(st.Lots, l)
		if Countable(l, st.Policy, clock.DateOf(now), now) {
			st.UsableToday += l.Balance
		}
	}
	out := make([]IngredientStock, 0, len(byIngredient))
	for _, st := range byIngredient {
		out = append(out, *st)
	}
	slices.SortFunc(out, func(a, b IngredientStock) int { return cmp.Compare(a.Name, b.Name) })
	return out, nil
}

// LotToCheck is a lot the kitchen should look at before shopping (§12).
type LotToCheck struct {
	Lot
	IngredientName string
	BaseUnit       catalog.BaseUnit
	// Expired lots only wait to be thrown away: they never count again.
	Expired bool
}

// LotsToCheck lists what is left that needs a look: perishable lots without
// a "masih bagus" check in the last 24 hours, and expired lots, by
// ingredient then age. Staff only.
func (s *Service) LotsToCheck(ctx context.Context) ([]LotToCheck, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return nil, err
	}
	labels, err := s.catalog.Labels(ctx, p.TenantID)
	if err != nil {
		return nil, err
	}
	lots, err := s.repo.Lots(ctx, p.TenantID, nil, counted)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	var out []LotToCheck
	for _, l := range lots {
		ing, ok := labels.Ingredients[l.IngredientID]
		if !ok || l.Balance <= 0 {
			continue
		}
		expired := l.Status == LotExpired || (l.ExpiresAt != nil && !l.ExpiresAt.After(now))
		fresh := l.LastOKAt != nil && now.Sub(*l.LastOKAt) <= CheckValidFor
		if expired || (ing.LeftoverPolicy == catalog.LeftoverConfirm && !fresh) {
			out = append(out, LotToCheck{Lot: l, IngredientName: ing.Name, BaseUnit: ing.BaseUnit, Expired: expired})
		}
	}
	slices.SortStableFunc(out, func(a, b LotToCheck) int { return cmp.Compare(a.IngredientName, b.IngredientName) })
	return out, nil
}

// CheckInput is the kitchen's verdict on a lot.
type CheckInput struct {
	// OK is "Masih bagus"; otherwise "Buang", with Reason.
	OK     bool
	Reason DiscardReason
	// Note is required for DiscardOther.
	Note string
}

// Check records the kitchen's look at a lot (§12). "Masih bagus" lets a
// perishable lot count for 24 hours; an expired lot cannot be passed.
// "Buang" throws away everything left: a waste movement with the reason,
// and the lot is discarded. Staff only.
func (s *Service) Check(ctx context.Context, lotID uuid.UUID, in CheckInput) (Lot, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return Lot{}, err
	}
	in.Note = strings.TrimSpace(in.Note)
	f := apperr.Fields{}
	if !in.OK {
		_, known := discardLabels[in.Reason]
		f.Check(known, "reason", "Pilih alasan dibuang.")
		f.Check(in.Reason != DiscardOther || in.Note != "", "note", "Tulis alasannya.")
	}
	f.Check(utf8.RuneCountInString(in.Note) <= maxNote, "note", "Catatan paling panjang 500 karakter.")
	if err := f.Err(); err != nil {
		return Lot{}, err
	}
	lot, err := s.repo.GetLot(ctx, p.TenantID, lotID)
	if err != nil {
		return Lot{}, err
	}
	now := s.clock.Now()
	err = s.tx.Tx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockIngredient(ctx, lot.IngredientID); err != nil {
			return err
		}
		lot, err = s.repo.GetLot(ctx, p.TenantID, lotID) // what is left now, under the lock
		if err != nil {
			return err
		}
		if lot.Balance <= 0 || (lot.Status != LotAvailable && lot.Status != LotExpired) {
			return ErrLotEmpty
		}
		expired := lot.Status == LotExpired || (lot.ExpiresAt != nil && !lot.ExpiresAt.After(now))
		if in.OK && expired {
			return ErrLotExpired
		}
		check := Check{LotID: lot.ID, OK: in.OK, Note: in.Note, CheckedBy: p.AuthUserID, At: now}
		if !in.OK {
			check.Reason = in.Reason
		}
		if err := s.repo.InsertCheck(ctx, p.TenantID, check); err != nil {
			return err
		}
		if !in.OK {
			if err := s.discard(ctx, p, lot, in, now); err != nil {
				return err
			}
		}
		return s.repo.Publish(ctx, stockChanged(p.TenantID, lot.IngredientID, now))
	})
	if err != nil {
		return Lot{}, err
	}
	return s.repo.GetLot(ctx, p.TenantID, lotID)
}

func (s *Service) discard(ctx context.Context, p identity.Principal, lot Lot, in CheckInput, now time.Time) error {
	reason := discardLabels[in.Reason]
	if in.Note != "" {
		reason += ": " + in.Note
	}
	if err := s.repo.InsertMovement(ctx, p.TenantID, Movement{
		LotID: lot.ID, IngredientID: lot.IngredientID, Kind: MoveWaste, Qty: -lot.Balance, Reason: reason,
		ActorID: p.AuthUserID, At: now,
	}); err != nil {
		return err
	}
	if err := s.repo.SetLotStatus(ctx, p.TenantID, lot.ID, LotDiscarded, now); err != nil {
		return err
	}
	return s.repo.Audit(ctx, audit.Entry{
		TenantID: p.TenantID, ActorID: p.AuthUserID, Action: "inventory.stock.discarded", Entity: "stock_lot", EntityID: lot.ID,
		Before: map[string]any{"ingredient_id": lot.IngredientID, "balance": lot.Balance, "status": lot.Status},
		After:  map[string]any{"balance": 0, "status": LotDiscarded},
		Reason: reason, At: now,
	})
}

// Count sets an ingredient's stock to what the kitchen counted (stok
// opname, decided per ingredient on 2026-09-29). Less than recorded comes
// out of the oldest lots first; more goes into the newest available lot, or
// a new one when there is none. A reason is required. Counting what is
// already recorded changes nothing. Staff only.
func (s *Service) Count(ctx context.Context, ingredientID uuid.UUID, actual int64, reason string) (IngredientStock, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return IngredientStock{}, err
	}
	labels, err := s.catalog.Labels(ctx, p.TenantID)
	if err != nil {
		return IngredientStock{}, err
	}
	reason = strings.TrimSpace(reason)
	ing, known := labels.Ingredients[ingredientID]
	f := apperr.Fields{}
	f.Check(known, "ingredient_id", "Bahan tidak dikenal.")
	f.Check(actual >= 0 && actual <= maxQty, "actual_qty", "Isi jumlah yang ada sekarang.")
	f.Check(reason != "", "reason", "Isi alasan penyesuaian.")
	f.Check(utf8.RuneCountInString(reason) <= maxNote, "reason", "Alasan paling panjang 500 karakter.")
	if err := f.Err(); err != nil {
		return IngredientStock{}, err
	}
	now := s.clock.Now()
	err = s.tx.Tx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockIngredient(ctx, ingredientID); err != nil {
			return err
		}
		lots, err := s.repo.Lots(ctx, p.TenantID, []uuid.UUID{ingredientID}, counted)
		if err != nil {
			return err
		}
		var total int64
		for _, l := range lots {
			total += max(l.Balance, 0)
		}
		diff := actual - total
		if diff == 0 {
			return nil
		}
		move := func(l Lot, qty int64) error {
			return s.repo.InsertMovement(ctx, p.TenantID, Movement{
				LotID: l.ID, IngredientID: ingredientID, Kind: MoveAdjust, Qty: qty, Reason: reason, ActorID: p.AuthUserID, At: now,
			})
		}
		if diff < 0 {
			remaining := -diff
			for _, l := range lots { // oldest first
				if remaining == 0 {
					break
				}
				take := min(l.Balance, remaining)
				if take <= 0 {
					continue
				}
				if err := move(l, -take); err != nil {
					return err
				}
				if take == l.Balance {
					if err := s.repo.SetLotStatus(ctx, p.TenantID, l.ID, LotExhausted, now); err != nil {
						return err
					}
				}
				remaining -= take
			}
		} else {
			target, ok := newestAvailable(lots)
			if !ok {
				target = Lot{ID: uuid.New(), IngredientID: ingredientID, ReceivedAt: now, Status: LotAvailable, Source: SourceAdjustment}
				if ing.ShelfLifeDays != nil {
					e := now.AddDate(0, 0, int(*ing.ShelfLifeDays))
					target.ExpiresAt = &e
				}
				if err := s.repo.InsertLot(ctx, p.TenantID, target, p.AuthUserID, now); err != nil {
					return err
				}
			}
			if err := move(target, diff); err != nil {
				return err
			}
		}
		if err := s.repo.Audit(ctx, audit.Entry{
			TenantID: p.TenantID, ActorID: p.AuthUserID, Action: "inventory.stock.counted", Entity: "ingredient", EntityID: ingredientID,
			Before: map[string]any{"total": total}, After: map[string]any{"total": actual}, Reason: reason, At: now,
		}); err != nil {
			return err
		}
		return s.repo.Publish(ctx, stockChanged(p.TenantID, ingredientID, now))
	})
	if err != nil {
		return IngredientStock{}, err
	}
	st, err := s.stock(ctx, p.TenantID, []uuid.UUID{ingredientID})
	if err != nil || len(st) == 0 {
		return IngredientStock{}, err
	}
	return st[0], nil
}

// newestAvailable returns the newest available lot with something left.
func newestAvailable(lots []Lot) (Lot, bool) {
	for i := len(lots) - 1; i >= 0; i-- {
		if lots[i].Status == LotAvailable && lots[i].Balance > 0 {
			return lots[i], true
		}
	}
	return Lot{}, false
}

// Use is how much of an ingredient a batch used.
type Use struct {
	IngredientID uuid.UUID
	Qty          int64
}

// Used is what a consumption took from the ledger, and what it could not
// because the ledger held less than the batch used: a delivery nobody
// recorded, for the kitchen to put right with a stock count.
type Used struct {
	IngredientID uuid.UUID
	Consumed     int64
	Missing      int64
}

// Consumer takes what a finished batch used out of the stock (§12, M3.3).
// It acts for a person another module already authorized, in that module's
// transaction.
type Consumer struct {
	repo Repository
}

// NewConsumer returns a Consumer over repo.
func NewConsumer(repo Repository) *Consumer {
	return &Consumer{repo: repo}
}

// Consume records what batch used of each ingredient as consume movements,
// oldest available lot first (FIFO), and empties the lots it uses up. It
// never takes more than a lot holds: what the ledger lacks comes back as
// Missing. Ingredients are locked in id order, so two consumptions never
// wait on each other in a circle. It publishes one inventory.stock_changed.
func (c *Consumer) Consume(ctx context.Context, tenant, batchID uuid.UUID, uses []Use, actorID uuid.UUID, at time.Time) ([]Used, error) {
	sorted := slices.Clone(uses)
	slices.SortFunc(sorted, func(a, b Use) int { return slices.Compare(a.IngredientID[:], b.IngredientID[:]) })
	out := make([]Used, 0, len(sorted))
	var changed []uuid.UUID
	for _, u := range sorted {
		if u.Qty <= 0 {
			continue
		}
		if err := c.repo.LockIngredient(ctx, u.IngredientID); err != nil {
			return nil, err
		}
		lots, err := c.repo.Lots(ctx, tenant, []uuid.UUID{u.IngredientID}, []LotStatus{LotAvailable})
		if err != nil {
			return nil, err
		}
		remaining := u.Qty
		for _, l := range lots {
			take := min(l.Balance, remaining)
			if take <= 0 {
				continue
			}
			if err := c.repo.InsertMovement(ctx, tenant, Movement{
				LotID: l.ID, IngredientID: u.IngredientID, Kind: MoveConsume, Qty: -take, BatchID: &batchID, ActorID: actorID, At: at,
			}); err != nil {
				return nil, err
			}
			if take == l.Balance {
				if err := c.repo.SetLotStatus(ctx, tenant, l.ID, LotExhausted, at); err != nil {
					return nil, err
				}
			}
			remaining -= take
			if remaining == 0 {
				break
			}
		}
		if remaining < u.Qty {
			changed = append(changed, u.IngredientID)
		}
		out = append(out, Used{IngredientID: u.IngredientID, Consumed: u.Qty - remaining, Missing: remaining})
	}
	if len(changed) == 0 {
		return out, nil
	}
	return out, c.repo.Publish(ctx, outbox.Event{
		TenantID: tenant, Aggregate: "batch", Type: "inventory.stock_changed", At: at,
		Payload: map[string]any{"batch_id": batchID, "ingredient_ids": changed},
	})
}

// stockChanged tells aggregation to compute the upcoming batches again.
func stockChanged(tenant, ingredientID uuid.UUID, at time.Time) outbox.Event {
	return outbox.Event{
		TenantID: tenant, Aggregate: "ingredient", Type: "inventory.stock_changed", At: at,
		Payload: map[string]any{"ingredient_id": ingredientID},
	}
}

// Reader lets aggregation read the stock in-process for a tenant; it acts
// for no person and checks no roles.
type Reader struct {
	repo    Repository
	catalog Catalog
	clock   clock.Clock
}

// NewReader returns a Reader over repo.
func NewReader(repo Repository, cat Catalog, clk clock.Clock) *Reader {
	return &Reader{repo: repo, catalog: cat, clock: clk}
}

// Usable returns what a batch made on date may count on of each ingredient,
// as of now (§12). It implements aggregation.Stock.
func (r *Reader) Usable(ctx context.Context, tenantID uuid.UUID, date clock.Date, ingredientIDs []uuid.UUID) (map[uuid.UUID]int64, error) {
	if len(ingredientIDs) == 0 {
		return map[uuid.UUID]int64{}, nil
	}
	labels, err := r.catalog.Labels(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	lots, err := r.repo.Lots(ctx, tenantID, ingredientIDs, []LotStatus{LotAvailable})
	if err != nil {
		return nil, err
	}
	policies := make(map[uuid.UUID]catalog.LeftoverPolicy, len(labels.Ingredients))
	for id, ing := range labels.Ingredients {
		policies[id] = ing.LeftoverPolicy
	}
	return Usable(lots, policies, date, r.clock.Now()), nil
}

// ExpireLots marks the tenant's lots past their expiry, for the worker's
// daily job; it returns how many. Idempotent.
func (r *Reader) ExpireLots(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	return r.repo.ExpireLots(ctx, tenantID, r.clock.Now())
}
