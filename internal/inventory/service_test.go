package inventory_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"pgregory.net/rapid"

	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/inventory"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/audit"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
)

var tenant = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// memStock keeps lots, the ledger, and checks in memory; balances are
// always the sum of the ledger, as in Postgres.
type memStock struct {
	lots      map[uuid.UUID]inventory.Lot
	order     []uuid.UUID // insertion order, which is received order in these tests
	movements []inventory.Movement
	checks    []inventory.Check
	audits    []audit.Entry
	events    []outbox.Event
	locks     []uuid.UUID
}

func newMemStock() *memStock { return &memStock{lots: map[uuid.UUID]inventory.Lot{}} }

func (m *memStock) LockIngredient(_ context.Context, id uuid.UUID) error {
	m.locks = append(m.locks, id)
	return nil
}

func (m *memStock) InsertLot(_ context.Context, _ uuid.UUID, l inventory.Lot, _ uuid.UUID, at time.Time) error {
	l.CreatedAt, l.Balance, l.LastOKAt = at, 0, nil
	m.lots[l.ID] = l
	m.order = append(m.order, l.ID)
	return nil
}

func (m *memStock) InsertMovement(_ context.Context, _ uuid.UUID, mv inventory.Movement) error {
	if l, ok := m.lots[mv.LotID]; !ok || l.IngredientID != mv.IngredientID {
		return errors.New("movement's ingredient is not its lot's")
	}
	m.movements = append(m.movements, mv)
	return nil
}

func (m *memStock) InsertCheck(_ context.Context, _ uuid.UUID, c inventory.Check) error {
	m.checks = append(m.checks, c)
	return nil
}

func (m *memStock) SetLotStatus(_ context.Context, _, id uuid.UUID, s inventory.LotStatus, _ time.Time) error {
	l := m.lots[id]
	l.Status = s
	m.lots[id] = l
	return nil
}

func (m *memStock) view(l inventory.Lot) inventory.Lot {
	l.Balance = 0
	for _, mv := range m.movements {
		if mv.LotID == l.ID {
			l.Balance += mv.Qty
		}
	}
	for _, c := range m.checks {
		if c.LotID == l.ID && c.OK && (l.LastOKAt == nil || c.At.After(*l.LastOKAt)) {
			t := c.At
			l.LastOKAt = &t
		}
	}
	return l
}

func (m *memStock) Lots(_ context.Context, _ uuid.UUID, ids []uuid.UUID, statuses []inventory.LotStatus) ([]inventory.Lot, error) {
	var out []inventory.Lot
	for _, id := range m.order {
		l := m.lots[id]
		if (len(ids) == 0 || slices.Contains(ids, l.IngredientID)) && slices.Contains(statuses, l.Status) {
			out = append(out, m.view(l))
		}
	}
	return out, nil
}

func (m *memStock) GetLot(_ context.Context, _, id uuid.UUID) (inventory.Lot, error) {
	l, ok := m.lots[id]
	if !ok {
		return inventory.Lot{}, apperr.ErrNotFound
	}
	return m.view(l), nil
}

func (m *memStock) ExpireLots(_ context.Context, _ uuid.UUID, now time.Time) (int64, error) {
	var n int64
	for id, l := range m.lots {
		if l.Status == inventory.LotAvailable && l.ExpiresAt != nil && !l.ExpiresAt.After(now) {
			l.Status = inventory.LotExpired
			m.lots[id] = l
			n++
		}
	}
	return n, nil
}

func (m *memStock) Audit(_ context.Context, e audit.Entry) error {
	m.audits = append(m.audits, e)
	return nil
}

func (m *memStock) Publish(_ context.Context, e outbox.Event) error {
	m.events = append(m.events, e)
	return nil
}

// ledger sums an ingredient's movements.
func (m *memStock) ledger(ingredient uuid.UUID) int64 {
	var sum int64
	for _, mv := range m.movements {
		if mv.IngredientID == ingredient {
			sum += mv.Qty
		}
	}
	return sum
}

type fixedCatalog struct{ labels catalog.Labels }

func (c fixedCatalog) Labels(context.Context, uuid.UUID) (catalog.Labels, error) {
	return c.labels, nil
}

type caller struct {
	roles []identity.Role
	user  uuid.UUID
}

func (c *caller) Principal(context.Context) (identity.Principal, error) {
	return identity.Principal{AuthUserID: c.user, TenantID: tenant, Roles: c.roles}, nil
}

type directTx struct{}

func (directTx) Tx(ctx context.Context, fn func(ctx context.Context) error) error { return fn(ctx) }

type pantry struct {
	repo       *memStock
	caller     *caller
	clock      *clock.Fake
	service    *inventory.Service
	reader     *inventory.Reader
	flour, egg uuid.UUID
}

func newPantry() *pantry {
	p := &pantry{repo: newMemStock(), caller: &caller{roles: []identity.Role{identity.RoleKitchen}, user: uuid.New()}, clock: clock.NewFake(wib(6, 10, 0)), flour: uuid.New(), egg: uuid.New()}
	months, days := int32(180), int32(14)
	cat := fixedCatalog{labels: catalog.Labels{Ingredients: map[uuid.UUID]catalog.Ingredient{
		p.flour: {ID: p.flour, Name: "Tepung", BaseUnit: catalog.Gram, ShelfLifeDays: &months, LeftoverPolicy: catalog.LeftoverAuto},
		p.egg:   {ID: p.egg, Name: "Telur", BaseUnit: catalog.Piece, Perishable: true, ShelfLifeDays: &days, LeftoverPolicy: catalog.LeftoverConfirm},
	}}}
	p.service = inventory.NewService(inventory.ServiceDeps{Repo: p.repo, Catalog: cat, Principals: p.caller, Tx: directTx{}, Clock: p.clock})
	p.reader = inventory.NewReader(p.repo, cat, p.clock)
	return p
}

func (p *pantry) receive(t *testing.T, ingredient uuid.UUID, qty int64) inventory.Lot {
	t.Helper()
	l, err := p.service.Receive(t.Context(), inventory.ReceiveInput{IngredientID: ingredient, Qty: qty})
	if err != nil {
		t.Fatalf("Receive(%d) error = %v", qty, err)
	}
	p.clock.Advance(time.Minute) // lots keep a received order
	return l
}

func fieldError(err error, field string) string {
	var v *apperr.ValidationError
	if errors.As(err, &v) {
		return v.Fields[field]
	}
	return ""
}

func TestReceive(t *testing.T) {
	p := newPantry()

	l := p.receive(t, p.egg, 30)

	if l.Balance != 30 || l.Status != inventory.LotAvailable || l.Source != inventory.SourceManual || !l.ReceivedAt.Equal(wib(6, 10, 0)) ||
		l.ExpiresAt == nil || !l.ExpiresAt.Equal(wib(20, 10, 0)) {
		t.Errorf("lot = %+v, want 30 eggs, expiring after their 14-day shelf life", l)
	}
	if a := p.repo.audits; len(a) != 1 || a[0].Action != "inventory.stock.received" || a[0].Reason != "Belanja masuk" || a[0].ActorID != p.caller.user {
		t.Errorf("audit = %+v", a)
	}
	if e := p.repo.events; len(e) != 1 || e[0].Type != "inventory.stock_changed" {
		t.Errorf("events = %+v, want the stock changed", e)
	}
	if !slices.Equal(p.repo.locks, []uuid.UUID{p.egg}) {
		t.Errorf("locks = %v, want the egg's stock locked", p.repo.locks)
	}
}

func TestReceive_Rejects(t *testing.T) {
	p := newPantry()
	now := p.clock.Now()
	before := now.Add(-time.Hour)
	for field, in := range map[string]inventory.ReceiveInput{
		"ingredient_id": {IngredientID: uuid.New(), Qty: 1},
		"qty":           {IngredientID: p.flour},
		"received_at":   {IngredientID: p.flour, Qty: 1, ReceivedAt: now.Add(time.Hour)},
		"expires_at":    {IngredientID: p.flour, Qty: 1, ExpiresAt: &before},
		"note":          {IngredientID: p.flour, Qty: 1, Note: strings.Repeat("x", 501)},
	} {
		if _, err := p.service.Receive(t.Context(), in); fieldError(err, field) == "" {
			t.Errorf("Receive(%+v) error = %v, want a message on %s", in, err, field)
		}
	}
	if _, err := p.service.Receive(t.Context(), inventory.ReceiveInput{IngredientID: p.flour, Qty: 1, ReceivedAt: now.Add(-31 * 24 * time.Hour)}); fieldError(err, "received_at") == "" {
		t.Errorf("a delivery 31 days back: error = %v", err)
	}
	if len(p.repo.movements) != 0 {
		t.Error("a refused delivery reached the ledger")
	}
}

// "Masih bagus" lets eggs count for 24 hours; "Buang" empties the lot into
// the ledger as waste.
func TestCheck(t *testing.T) {
	p := newPantry()
	eggs := p.receive(t, p.egg, 12)
	date := clock.DateOf(p.clock.Now()).AddDays(1)
	usable := func() int64 {
		got, _ := p.reader.Usable(t.Context(), tenant, date, []uuid.UUID{p.egg})
		return got[p.egg]
	}
	if usable() != 0 {
		t.Fatal("unchecked eggs counted")
	}

	if _, err := p.service.Check(t.Context(), eggs.ID, inventory.CheckInput{OK: true}); err != nil {
		t.Fatal(err)
	}
	if usable() != 12 {
		t.Errorf("checked eggs count %d, want 12", usable())
	}
	p.clock.Advance(24*time.Hour + time.Minute)
	if usable() != 0 {
		t.Error("eggs checked a day ago still count")
	}

	l, err := p.service.Check(t.Context(), eggs.ID, inventory.CheckInput{Reason: inventory.DiscardSmell, Note: "baunya aneh"})
	if err != nil {
		t.Fatal(err)
	}
	if l.Balance != 0 || l.Status != inventory.LotDiscarded {
		t.Errorf("lot = %+v, want nothing left, discarded", l)
	}
	w := p.repo.movements[len(p.repo.movements)-1]
	if w.Kind != inventory.MoveWaste || w.Qty != -12 || w.Reason != "bau: baunya aneh" {
		t.Errorf("waste = %+v", w)
	}
	if a := p.repo.audits[len(p.repo.audits)-1]; a.Action != "inventory.stock.discarded" || a.Reason != "bau: baunya aneh" {
		t.Errorf("audit = %+v", a)
	}
	var pe *apperr.PreconditionError
	if _, err := p.service.Check(t.Context(), eggs.ID, inventory.CheckInput{OK: true}); !errors.As(err, &pe) || pe.Reason != "lot_empty" {
		t.Errorf("checking a discarded lot: error = %v, want lot_empty", err)
	}
}

func TestCheck_Rejects(t *testing.T) {
	p := newPantry()
	eggs := p.receive(t, p.egg, 6)
	if _, err := p.service.Check(t.Context(), eggs.ID, inventory.CheckInput{}); fieldError(err, "reason") == "" {
		t.Errorf("Buang without a reason: error = %v", err)
	}
	if _, err := p.service.Check(t.Context(), eggs.ID, inventory.CheckInput{Reason: inventory.DiscardOther}); fieldError(err, "note") == "" {
		t.Errorf("Buang for another reason without a note: error = %v", err)
	}
	if _, err := p.service.Check(t.Context(), uuid.New(), inventory.CheckInput{OK: true}); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("unknown lot: error = %v, want ErrNotFound", err)
	}

	p.clock.Advance(15 * 24 * time.Hour) // past the eggs' 14 days
	var pe *apperr.PreconditionError
	if _, err := p.service.Check(t.Context(), eggs.ID, inventory.CheckInput{OK: true}); !errors.As(err, &pe) || pe.Reason != "lot_expired" {
		t.Errorf("passing expired eggs: error = %v, want lot_expired", err)
	}
	if _, err := p.service.Check(t.Context(), eggs.ID, inventory.CheckInput{Reason: inventory.DiscardExpired}); err != nil {
		t.Errorf("throwing expired eggs away: error = %v", err)
	}
}

// A count below the ledger comes out of the oldest lots first; above it
// goes into the newest available lot.
func TestCount(t *testing.T) {
	p := newPantry()
	old, mid, fresh := p.receive(t, p.flour, 300), p.receive(t, p.flour, 500), p.receive(t, p.flour, 1000)

	st, err := p.service.Count(t.Context(), p.flour, 1200, "Stok opname akhir pekan")

	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 1200 || p.repo.ledger(p.flour) != 1200 {
		t.Errorf("total = %d (ledger %d), want 1,200", st.Total, p.repo.ledger(p.flour))
	}
	got := map[uuid.UUID]int64{}
	for _, l := range st.Lots {
		got[l.ID] = l.Balance
	}
	if _, ok := got[old.ID]; ok || got[mid.ID] != 200 || got[fresh.ID] != 1000 {
		t.Errorf("lots = %v, want the oldest emptied, 200 left of the next, the newest untouched", got)
	}
	if l, _ := p.repo.GetLot(t.Context(), tenant, old.ID); l.Status != inventory.LotExhausted {
		t.Errorf("oldest lot = %s, want exhausted", l.Status)
	}
	if a := p.repo.audits[len(p.repo.audits)-1]; a.Action != "inventory.stock.counted" || a.Reason != "Stok opname akhir pekan" {
		t.Errorf("audit = %+v", a)
	}

	st, err = p.service.Count(t.Context(), p.flour, 1500, "Salah hitung kemarin")
	if err != nil || st.Total != 1500 {
		t.Fatalf("Count(1500) = %+v, %v", st, err)
	}
	if l, _ := p.repo.GetLot(t.Context(), tenant, fresh.ID); l.Balance != 1300 {
		t.Errorf("newest lot = %d, want the 300 more there", l.Balance)
	}

	audits := len(p.repo.audits)
	if _, err := p.service.Count(t.Context(), p.flour, 1500, "Sama"); err != nil || len(p.repo.audits) != audits {
		t.Errorf("counting what is recorded: %v, %d new audits; want nothing", err, len(p.repo.audits)-audits)
	}
}

// Counting stock that is not recorded at all opens an adjustment lot.
func TestCount_FromNothing(t *testing.T) {
	p := newPantry()

	st, err := p.service.Count(t.Context(), p.egg, 8, "Ternyata masih ada")

	if err != nil || st.Total != 8 || len(st.Lots) != 1 || st.Lots[0].Source != inventory.SourceAdjustment || st.Lots[0].ExpiresAt == nil {
		t.Errorf("Count() = %+v, %v; want one adjustment lot of 8 with an expiry", st, err)
	}
	if _, err := p.service.Count(t.Context(), p.egg, -1, "x"); fieldError(err, "actual_qty") == "" {
		t.Errorf("negative count: error = %v", err)
	}
	if _, err := p.service.Count(t.Context(), p.egg, 1, " "); fieldError(err, "reason") == "" {
		t.Errorf("count without a reason: error = %v", err)
	}
}

func TestLotsToCheck(t *testing.T) {
	p := newPantry()
	eggs := p.receive(t, p.egg, 12)
	p.receive(t, p.flour, 1000) // flour needs no check
	checked := p.receive(t, p.egg, 6)
	if _, err := p.service.Check(t.Context(), checked.ID, inventory.CheckInput{OK: true}); err != nil {
		t.Fatal(err)
	}

	got, err := p.service.LotsToCheck(t.Context())

	if err != nil || len(got) != 1 || got[0].ID != eggs.ID || got[0].IngredientName != "Telur" || got[0].Expired {
		t.Fatalf("LotsToCheck() = %+v, %v; want the unchecked eggs only", got, err)
	}
	p.clock.Advance(15 * 24 * time.Hour) // past the eggs' 14 days, not the flour's 180
	got, _ = p.service.LotsToCheck(t.Context())
	if len(got) != 2 || !got[0].Expired || !got[1].Expired {
		t.Errorf("after expiry: %+v, want both egg lots, expired, and no flour", got)
	}
}

func TestService_Roles(t *testing.T) {
	p := newPantry()
	p.caller.roles = nil
	if _, err := p.service.Stock(t.Context()); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("customer Stock() error = %v, want ErrForbidden", err)
	}
	if _, err := p.service.Receive(t.Context(), inventory.ReceiveInput{IngredientID: p.flour, Qty: 1}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("customer Receive() error = %v, want ErrForbidden", err)
	}
	p.caller.roles = []identity.Role{identity.RoleOwner}
	if _, err := p.service.Stock(t.Context()); err != nil {
		t.Errorf("owner Stock() error = %v", err)
	}
}

func TestReader_ExpireLots(t *testing.T) {
	p := newPantry()
	eggs := p.receive(t, p.egg, 12)
	p.receive(t, p.flour, 1000)
	p.clock.Advance(15 * 24 * time.Hour)

	n, err := p.reader.ExpireLots(t.Context(), tenant)

	if err != nil || n != 1 {
		t.Errorf("ExpireLots() = %d, %v; want the eggs", n, err)
	}
	if l, _ := p.repo.GetLot(t.Context(), tenant, eggs.ID); l.Status != inventory.LotExpired || l.Balance != 12 {
		t.Errorf("eggs = %+v, want expired, still in the ledger until thrown away", l)
	}
	if n, _ := p.reader.ExpireLots(t.Context(), tenant); n != 0 {
		t.Errorf("second run expired %d, want 0", n)
	}
}

// Whatever the kitchen does, a lot never goes below zero, what the stock
// shows is the sum of the ledger, and a count leaves exactly what was
// counted.
func TestProperty_StockIsTheLedger(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		p := newPantry()
		for range rapid.IntRange(1, 25).Draw(rt, "steps") {
			ing := rapid.SampledFrom([]uuid.UUID{p.flour, p.egg}).Draw(rt, "ingredient")
			switch rapid.IntRange(0, 3).Draw(rt, "action") {
			case 0:
				_, _ = p.service.Receive(t.Context(), inventory.ReceiveInput{IngredientID: ing, Qty: rapid.Int64Range(1, 2000).Draw(rt, "qty")})
				p.clock.Advance(time.Minute)
			case 1:
				actual := rapid.Int64Range(0, 3000).Draw(rt, "actual")
				if st, err := p.service.Count(t.Context(), ing, actual, "opname"); err == nil && st.Total != actual {
					rt.Fatalf("count to %d left %d", actual, st.Total)
				}
			case 2:
				lots, _ := p.repo.Lots(t.Context(), tenant, []uuid.UUID{ing}, []inventory.LotStatus{inventory.LotAvailable, inventory.LotExpired})
				if len(lots) > 0 {
					l := rapid.SampledFrom(lots).Draw(rt, "lot")
					_, _ = p.service.Check(t.Context(), l.ID, inventory.CheckInput{OK: rapid.Bool().Draw(rt, "ok"), Reason: inventory.DiscardMold})
				}
			case 3:
				p.clock.Advance(rapid.SampledFrom([]time.Duration{time.Hour, 3 * 24 * time.Hour, 20 * 24 * time.Hour}).Draw(rt, "wait"))
				_, _ = p.reader.ExpireLots(t.Context(), tenant)
			}
			stock, err := p.service.Stock(t.Context())
			if err != nil {
				rt.Fatal(err)
			}
			for _, st := range stock {
				if st.Total != p.repo.ledger(st.IngredientID) {
					rt.Fatalf("%s shows %d, ledger holds %d", st.Name, st.Total, p.repo.ledger(st.IngredientID))
				}
				if st.UsableToday > st.Total {
					rt.Fatalf("%s: %d usable of %d", st.Name, st.UsableToday, st.Total)
				}
			}
			for _, l := range p.repo.lots {
				if b := p.repo.view(l).Balance; b < 0 {
					rt.Fatalf("lot %s went to %d", l.ID, b)
				}
			}
		}
	})
}

// A finished batch takes its flour from the oldest lot first, empties the
// lots it uses up, and reports what the ledger lacked instead of going
// below zero; expired lots are never used.
func TestConsumer_FIFO(t *testing.T) {
	p := newPantry()
	old, fresh := p.receive(t, p.flour, 300), p.receive(t, p.flour, 1000)
	eggs := p.receive(t, p.egg, 4)
	p.clock.Advance(15 * 24 * time.Hour) // the eggs expire; the flour keeps
	if _, err := p.reader.ExpireLots(t.Context(), tenant); err != nil {
		t.Fatal(err)
	}
	batch, cook := uuid.New(), uuid.New()
	events := len(p.repo.events)

	used, err := inventory.NewConsumer(p.repo).Consume(t.Context(), tenant, batch,
		[]inventory.Use{{IngredientID: p.flour, Qty: 800}, {IngredientID: p.egg, Qty: 6}}, cook, p.clock.Now())

	if err != nil {
		t.Fatal(err)
	}
	got := map[uuid.UUID]inventory.Used{}
	for _, u := range used {
		got[u.IngredientID] = u
	}
	if f := got[p.flour]; f.Consumed != 800 || f.Missing != 0 {
		t.Errorf("flour = %+v, want 800 taken", f)
	}
	if e := got[p.egg]; e.Consumed != 0 || e.Missing != 6 {
		t.Errorf("eggs = %+v, want none taken from the expired lot and 6 missing", e)
	}
	if l, _ := p.repo.GetLot(t.Context(), tenant, old.ID); l.Balance != 0 || l.Status != inventory.LotExhausted {
		t.Errorf("oldest flour = %+v, want emptied first", l)
	}
	if l, _ := p.repo.GetLot(t.Context(), tenant, fresh.ID); l.Balance != 500 {
		t.Errorf("newer flour = %d, want 500 left", l.Balance)
	}
	if l, _ := p.repo.GetLot(t.Context(), tenant, eggs.ID); l.Balance != 4 {
		t.Errorf("expired eggs = %d, want untouched", l.Balance)
	}
	for _, m := range p.repo.movements[len(p.repo.movements)-2:] {
		if m.Kind != inventory.MoveConsume || m.BatchID == nil || *m.BatchID != batch || m.ActorID != cook {
			t.Errorf("movement = %+v, want a consume for the batch by the cook", m)
		}
	}
	if len(p.repo.events) != events+1 || p.repo.events[len(p.repo.events)-1].Type != "inventory.stock_changed" {
		t.Errorf("events = %+v, want one stock change", p.repo.events[events:])
	}
}

// Ingredients lock in id order whatever order the uses come in, so two
// consumptions never wait on each other in a circle.
func TestConsumer_LocksInIDOrder(t *testing.T) {
	p := newPantry()
	p.receive(t, p.flour, 100)
	p.receive(t, p.egg, 10)
	p.repo.locks = nil

	_, err := inventory.NewConsumer(p.repo).Consume(t.Context(), tenant, uuid.New(),
		[]inventory.Use{{IngredientID: p.egg, Qty: 1}, {IngredientID: p.flour, Qty: 1}}, uuid.New(), p.clock.Now())

	if err != nil {
		t.Fatal(err)
	}
	want := []uuid.UUID{p.flour, p.egg}
	if slices.Compare(p.egg[:], p.flour[:]) < 0 {
		want = []uuid.UUID{p.egg, p.flour}
	}
	if !slices.Equal(p.repo.locks, want) {
		t.Errorf("locks = %v, want %v", p.repo.locks, want)
	}
}
