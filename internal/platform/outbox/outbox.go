// Package outbox records events for other parts of the system in the outbox
// table, inside the transaction that makes the change (docs/architecture.md
// §9.1, §21). A publisher job delivers them later (M2), so a change and its
// event are either both saved or both rolled back: no lost events, and no
// events about changes that never happened.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	typePattern      = regexp.MustCompile(`^[a-z_]+(\.[a-z_]+)+$`) // catalog.recipe_changed
	aggregatePattern = regexp.MustCompile(`^[a-z_]+$`)             // component, variant
)

// Event is something other modules may react to.
type Event struct {
	TenantID  uuid.UUID
	Aggregate string // the kind of record that changed, e.g. "component"
	Type      string // dotted, e.g. "catalog.recipe_changed"
	Payload   any    // encoded as JSON
	At        time.Time
}

// Execer is satisfied by pgx.Tx; Append needs nothing more.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Append writes e through tx, which must be the transaction making the change.
func Append(ctx context.Context, tx Execer, e Event) error {
	if !typePattern.MatchString(e.Type) {
		return fmt.Errorf("outbox event type %q must be dotted lowercase, like catalog.recipe_changed", e.Type)
	}
	if !aggregatePattern.MatchString(e.Aggregate) {
		return fmt.Errorf("outbox aggregate %q must be lowercase, like component", e.Aggregate)
	}
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return fmt.Errorf("encode outbox payload of %s: %w", e.Type, err)
	}
	_, err = tx.Exec(ctx, `
		insert into outbox (tenant_id, aggregate, event_type, payload, created_at)
		values ($1, $2, $3, $4, $5)`,
		e.TenantID, e.Aggregate, e.Type, payload, e.At)
	if err != nil {
		return fmt.Errorf("append outbox event %s: %w", e.Type, err)
	}
	return nil
}
