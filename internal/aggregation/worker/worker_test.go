package worker_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/aggregation/worker"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
)

// Orders that start or stop counting recompute their day's batch; a recipe
// change recomputes every upcoming batch; an event without a date is
// refused.
func TestSubscriptions(t *testing.T) {
	tenant := uuid.New()
	payload, _ := json.Marshal(map[string]any{"order_id": uuid.New(), "production_date": "2026-10-08"})
	subs := worker.Subscriptions()

	for _, typ := range []string{"order.confirmed", "order.cancelled"} {
		args, err := subs[typ][0](outbox.Stored{Seq: 1, TenantID: tenant, Type: typ, Payload: payload})
		if got, ok := args.(worker.RecomputeBatchArgs); err != nil || !ok || got.TenantID != tenant || got.Date != "2026-10-08" {
			t.Errorf("%s started %#v, %v; want the batch of 8 October", typ, args, err)
		}
	}
	for _, typ := range []string{"catalog.recipe_changed", "inventory.stock_changed"} {
		args, err := subs[typ][0](outbox.Stored{Seq: 2, TenantID: tenant, Payload: json.RawMessage(`{"ingredient_id":"x"}`)})
		if got, ok := args.(worker.RecomputeUpcomingArgs); err != nil || !ok || got.TenantID != tenant {
			t.Errorf("%s started %#v, %v; want every upcoming batch", typ, args, err)
		}
	}
	for _, bad := range []string{`{"order_id":"x"}`, `{"production_date":"8 Oktober"}`, `nope`} {
		if _, err := subs["order.confirmed"][0](outbox.Stored{Seq: 3, Payload: json.RawMessage(bad)}); err == nil {
			t.Errorf("payload %s started a job, want an error", bad)
		}
	}
	if _, ok := subs["order.expired"]; ok {
		t.Error("order.expired recomputes a batch, but an unpaid order never counted")
	}
}
