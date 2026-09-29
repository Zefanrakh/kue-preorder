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

// Only an order moved into production marks its batch; other steps start
// nothing.
func TestSubscriptions_InProduction(t *testing.T) {
	tenant := uuid.New()
	sub := worker.Subscriptions()["order.status_changed"][0]
	event := func(to string) outbox.Stored {
		payload, _ := json.Marshal(map[string]any{"from": "confirmed", "to": to, "production_date": "2026-10-08"})
		return outbox.Stored{Seq: 5, TenantID: tenant, Type: "order.status_changed", Payload: payload}
	}

	args, err := sub(event("in_production"))
	if got, ok := args.(worker.MarkInProductionArgs); err != nil || !ok || got.TenantID != tenant || got.Date != "2026-10-08" {
		t.Errorf("in_production started %#v, %v; want the batch of 8 October marked", args, err)
	}
	if args, err := sub(event("ready")); err != nil || args != nil {
		t.Errorf("ready started %#v, %v; want nothing", args, err)
	}
	if _, err := sub(outbox.Stored{Seq: 6, Payload: json.RawMessage(`{"to":"in_production"}`)}); err == nil {
		t.Error("an event without a date started a job")
	}
}
