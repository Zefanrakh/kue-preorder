// Package audit records staff actions that change money, stock, or schedules
// (docs/architecture.md §22) in the append-only audit_log table.
//
// Record runs inside the transaction that makes the change, so a change and
// its audit entry commit or roll back together: there is never a change
// without its entry, nor an entry for a change that did not happen.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrReasonRequired means an audited change came without a reason.
var ErrReasonRequired = errors.New("a reason is required for this change")

var actionPattern = regexp.MustCompile(`^[a-z_]+(\.[a-z_]+)+$`)

// Entry is one audited change.
type Entry struct {
	TenantID uuid.UUID
	ActorID  uuid.UUID // Supabase Auth user id of the staff member
	Action   string    // dotted, e.g. "catalog.variant.price_changed"
	Entity   string    // e.g. "product_variant"
	EntityID uuid.UUID
	Before   any // encoded as JSON; nil stores NULL
	After    any
	Reason   string
	At       time.Time // from platform/clock
}

// Execer is satisfied by pgx.Tx; Record needs nothing more.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Record writes e through tx, which must be the transaction making the change.
func Record(ctx context.Context, tx Execer, e Entry) error {
	if strings.TrimSpace(e.Reason) == "" {
		return ErrReasonRequired
	}
	if !actionPattern.MatchString(e.Action) {
		return fmt.Errorf("audit action %q must be dotted lowercase, like catalog.variant.price_changed", e.Action)
	}
	before, err := encode(e.Before)
	if err != nil {
		return fmt.Errorf("encode audit before-state: %w", err)
	}
	after, err := encode(e.After)
	if err != nil {
		return fmt.Errorf("encode audit after-state: %w", err)
	}
	_, err = tx.Exec(ctx, `
		insert into audit_log (tenant_id, actor_id, action, entity, entity_id, before, after, reason, created_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		e.TenantID, e.ActorID, e.Action, e.Entity, e.EntityID, before, after, strings.TrimSpace(e.Reason), e.At)
	if err != nil {
		return fmt.Errorf("record audit entry %s: %w", e.Action, err)
	}
	return nil
}

func encode(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}
