package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
)

// Publisher defaults.
const (
	DefaultBatch    = 100
	DefaultInterval = 2 * time.Second
)

// Stored is an event as the outbox keeps it.
type Stored struct {
	// Seq orders the events as they were written (migration 00010).
	Seq       int64
	ID        uuid.UUID
	TenantID  uuid.UUID
	Aggregate string
	Type      string
	Payload   json.RawMessage
	CreatedAt time.Time
}

// Dispatch hands one event to its consumers inside the publisher's
// transaction tx: what it writes commits together with the event marked
// published, or not at all. An error rolls the whole batch back to be tried
// again, so it is for failures a retry can fix.
type Dispatch func(ctx context.Context, tx pgx.Tx, e Stored) error

// Publisher delivers the outbox's events to Dispatch in the order they were
// written (§9.1, §21), in the worker. Delivery is exactly once as far as
// the database goes: an event is marked published in the transaction that
// dispatches it. Only one publisher works at a time, across processes, so
// the order holds with more than one worker running.
type Publisher struct {
	db       *db.DB
	dispatch Dispatch
	clock    clock.Clock
	logger   *slog.Logger
	batch    int
	interval time.Duration
}

// NewPublisher returns a publisher over d that sends up to DefaultBatch
// events per transaction and looks for new ones every DefaultInterval.
func NewPublisher(d *db.DB, dispatch Dispatch, clk clock.Clock, logger *slog.Logger) *Publisher {
	return &Publisher{db: d, dispatch: dispatch, clock: clk, logger: logger, batch: DefaultBatch, interval: DefaultInterval}
}

// PublishBatch dispatches up to one batch of unpublished events, oldest
// first, marks them published, and returns how many it published. While
// another publisher holds the lock it returns 0 at once.
func (p *Publisher) PublishBatch(ctx context.Context) (int, error) {
	n := 0
	err := p.db.InTx(ctx, func(tx pgx.Tx) error {
		var mine bool
		if err := tx.QueryRow(ctx, "select pg_try_advisory_xact_lock(hashtextextended('outbox:publish', 0))").Scan(&mine); err != nil {
			return fmt.Errorf("lock the outbox: %w", err)
		}
		if !mine {
			return nil
		}
		rows, err := tx.Query(ctx, `
			select seq, id, tenant_id, aggregate, event_type, payload, created_at
			from outbox where published_at is null
			order by seq
			limit $1`, p.batch)
		if err != nil {
			return fmt.Errorf("read the outbox: %w", err)
		}
		events, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Stored, error) {
			var e Stored
			err := row.Scan(&e.Seq, &e.ID, &e.TenantID, &e.Aggregate, &e.Type, &e.Payload, &e.CreatedAt)
			return e, err
		})
		if err != nil {
			return fmt.Errorf("read the outbox: %w", err)
		}
		seqs := make([]int64, len(events))
		for i, e := range events {
			if err := p.dispatch(ctx, tx, e); err != nil {
				return fmt.Errorf("dispatch outbox event %d (%s): %w", e.Seq, e.Type, err)
			}
			seqs[i] = e.Seq
		}
		if len(seqs) > 0 {
			if _, err := tx.Exec(ctx, "update outbox set published_at = $1 where seq = any($2)", p.clock.Now(), seqs); err != nil {
				return fmt.Errorf("mark outbox events published: %w", err)
			}
		}
		n = len(events)
		return nil
	})
	return n, err
}

// Run publishes until ctx ends: batch after batch while events are waiting,
// then every interval. A failure is logged at ERROR, which alerts, and the
// batch is tried again after the interval.
func (p *Publisher) Run(ctx context.Context) {
	for {
		n, err := p.PublishBatch(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			p.logger.ErrorContext(ctx, "publish outbox", slog.Any("error", err))
		} else if n == p.batch {
			continue // more are waiting
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.interval):
		}
	}
}
