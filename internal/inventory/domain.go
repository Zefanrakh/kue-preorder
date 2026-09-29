// Package inventory keeps the kitchen's stock (docs/architecture.md §9.4,
// §12): lots of each ingredient, an append-only ledger of what moved in and
// out of them, and the checks of what is left. Stock is the sum of the
// ledger; no balance is ever stored. Quantities are whole units of the
// ingredient's base unit (g, ml, pcs).
package inventory

import (
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

// LotStatus is what is left of a lot.
type LotStatus string

// Lot statuses.
const (
	LotAvailable LotStatus = "available" // may count as stock (§12)
	LotExhausted LotStatus = "exhausted" // nothing left
	LotDiscarded LotStatus = "discarded" // thrown away after a check
	LotExpired   LotStatus = "expired"   // past its expiry; never counted
)

// Source is where a lot came from.
type Source string

// Lot sources.
const (
	SourceManual      Source = "manual"      // "Belanja masuk" in the PWA
	SourceProcurement Source = "procurement" // M4
	SourceAdjustment  Source = "adjustment"  // a stock count found more than there was
)

// MovementKind is what moved stock in or out of a lot.
type MovementKind string

// Movement kinds.
const (
	MoveReceive MovementKind = "receive"
	MoveConsume MovementKind = "consume" // M3.3
	MoveWaste   MovementKind = "waste"
	MoveAdjust  MovementKind = "adjust"
)

// Lot is one delivery of an ingredient and what is left of it.
type Lot struct {
	ID           uuid.UUID
	IngredientID uuid.UUID
	ReceivedAt   time.Time
	// ExpiresAt is nil when the ingredient's shelf life is unknown.
	ExpiresAt *time.Time
	Status    LotStatus
	Source    Source
	Note      string
	// Balance is the sum of the lot's movements.
	Balance int64
	// LastOKAt is the latest "masih bagus" check; nil without one.
	LastOKAt  *time.Time
	CreatedAt time.Time
}

// CheckValidFor is how long a "masih bagus" check lets a perishable lot
// count (§12).
const CheckValidFor = 24 * time.Hour

// Countable reports whether what is left of l counts as stock for a batch
// made on date, as of now (§12). When in doubt it does not: a shortage on
// the day costs far more than buying a little too much.
//
//   - Only an available lot with something left counts.
//   - A lot that expires before date ends does not count.
//   - A perishable ingredient (confirm) counts only with a "masih bagus"
//     check in the last 24 hours; unchecked means absent.
//   - An ingredient always bought fresh (never) never counts.
func Countable(l Lot, policy catalog.LeftoverPolicy, date clock.Date, now time.Time) bool {
	if l.Status != LotAvailable || l.Balance <= 0 {
		return false
	}
	if l.ExpiresAt != nil && l.ExpiresAt.Before(date.AddDays(1).At(0)) {
		return false
	}
	switch policy {
	case catalog.LeftoverAuto:
		return true
	case catalog.LeftoverConfirm:
		return l.LastOKAt != nil && !l.LastOKAt.After(now) && now.Sub(*l.LastOKAt) <= CheckValidFor
	default:
		return false
	}
}

// Usable sums, per ingredient, what the lots may count for a batch made on
// date (§12). An ingredient without a known policy counts nothing.
func Usable(lots []Lot, policies map[uuid.UUID]catalog.LeftoverPolicy, date clock.Date, now time.Time) map[uuid.UUID]int64 {
	out := map[uuid.UUID]int64{}
	for _, l := range lots {
		policy, ok := policies[l.IngredientID]
		if ok && Countable(l, policy, date, now) {
			out[l.IngredientID] += l.Balance
		}
	}
	return out
}

// DiscardReason is why a lot was thrown away.
type DiscardReason string

// Discard reasons, as the PWA offers them.
const (
	DiscardSmell   DiscardReason = "smell"   // bau
	DiscardMold    DiscardReason = "mold"    // berjamur
	DiscardExpired DiscardReason = "expired" // kedaluwarsa
	DiscardOther   DiscardReason = "other"   // lainnya, with a note
)

// discardLabels name the reasons in the ledger, in Indonesian.
var discardLabels = map[DiscardReason]string{
	DiscardSmell: "bau", DiscardMold: "berjamur", DiscardExpired: "kedaluwarsa", DiscardOther: "lainnya",
}

// maxQty bounds a quantity: 100 tonnes of grams, far beyond any kitchen.
const maxQty = 100_000_000

// maxNote bounds a note or a reason.
const maxNote = 500

// Errors of stock actions, in words the PWA shows as they are.
var (
	ErrLotEmpty = &apperr.PreconditionError{
		Reason:  "lot_empty",
		Message: "Bahan ini sudah habis atau sudah dibuang.",
	}
	ErrLotExpired = &apperr.PreconditionError{
		Reason:  "lot_expired",
		Message: "Bahan ini sudah lewat tanggal kedaluwarsa, jadi tidak dihitung lagi. Pilih Buang kalau sudah dibuang.",
	}
)
