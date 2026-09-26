package webhook

import (
	"context"
	"fmt"
	"time"

	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
)

// Events remembers deliveries in webhook_events (§9.1, §18), so a delivery
// the provider retries takes effect once. The table is global: a provider
// delivers before anyone knows the tenant.
type Events struct {
	db *db.DB
}

// NewEvents returns Events over d. It joins the caller's transaction when
// there is one (platform/db.Tx), so a claim commits with the effect.
func NewEvents(d *db.DB) *Events {
	return &Events{db: d}
}

// Claim records the delivery and reports whether it is new. A delivery
// already claimed, handled or not, returns false: its effect happened or is
// happening, and must not happen twice.
func (e *Events) Claim(ctx context.Context, provider, id string, at time.Time) (bool, error) {
	tag, err := e.db.Conn(ctx).Exec(ctx, `
		insert into webhook_events (provider, event_id, received_at)
		values ($1, $2, $3)
		on conflict (provider, event_id) do nothing`, provider, id, at)
	if err != nil {
		return false, fmt.Errorf("claim %s webhook %s: %w", provider, id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// Done marks a claimed delivery as handled.
func (e *Events) Done(ctx context.Context, provider, id string, at time.Time) error {
	_, err := e.db.Conn(ctx).Exec(ctx, `
		update webhook_events set processed_at = $3
		where provider = $1 and event_id = $2`, provider, id, at)
	if err != nil {
		return fmt.Errorf("finish %s webhook %s: %w", provider, id, err)
	}
	return nil
}

// Release forgets a claimed delivery whose effect failed outside the
// database, such as a message that could not be sent, so the provider's
// retry can try again. A handled delivery stays.
func (e *Events) Release(ctx context.Context, provider, id string) error {
	_, err := e.db.Conn(ctx).Exec(ctx, `
		delete from webhook_events
		where provider = $1 and event_id = $2 and processed_at is null`, provider, id)
	if err != nil {
		return fmt.Errorf("release %s webhook %s: %w", provider, id, err)
	}
	return nil
}
