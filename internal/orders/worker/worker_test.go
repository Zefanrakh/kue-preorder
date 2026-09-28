package worker_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/orders/worker"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
)

// A confirmed order and money recorded for one each start the balance
// invoice job for that order; an event without an order is refused.
func TestSubscriptions_BillTheBalance(t *testing.T) {
	tenant, order := uuid.New(), uuid.New()
	payload, _ := json.Marshal(map[string]any{"order_id": order, "code": "K7M3QX"})
	subs := worker.Subscriptions()

	for _, typ := range []string{"order.confirmed", "order.payment_received"} {
		if len(subs[typ]) != 1 {
			t.Fatalf("%s has %d subscribers, want 1", typ, len(subs[typ]))
		}
		args, err := subs[typ][0](outbox.Stored{Seq: 3, TenantID: tenant, Type: typ, Payload: payload})
		if got, ok := args.(worker.BalanceInvoiceArgs); err != nil || !ok || got.TenantID != tenant || got.OrderID != order {
			t.Errorf("%s started %#v, %v; want the balance invoice of the order", typ, args, err)
		}
	}
	for _, bad := range []string{`{"code":"K7M3QX"}`, `not json`} {
		if _, err := subs["order.confirmed"][0](outbox.Stored{Seq: 4, Payload: json.RawMessage(bad)}); err == nil {
			t.Errorf("payload %s started a job, want an error", bad)
		}
	}
	if opts := (worker.BalanceInvoiceArgs{}).InsertOpts(); opts.MaxAttempts != 10 {
		t.Errorf("MaxAttempts = %d, want 10", opts.MaxAttempts)
	}
}
