//go:build integration

package outbox_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Zefanrakh/kue-preorder/internal/platform/db"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
	"github.com/Zefanrakh/kue-preorder/internal/platform/outbox"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

var at = time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)

func recipeChanged(component uuid.UUID) outbox.Event {
	return outbox.Event{
		TenantID:  dbtest.DefaultTenantID,
		Aggregate: "component",
		Type:      "catalog.recipe_changed",
		Payload:   map[string]any{"component_id": component, "version": 2},
		At:        at,
	}
}

func count(t *testing.T, d *db.DB) int {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(t.Context(), "select count(*) from outbox").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAppend_WritesAnUnpublishedEvent(t *testing.T) {
	d := dbtest.New(t)
	component := uuid.New()

	err := d.InTx(t.Context(), func(tx pgx.Tx) error { return outbox.Append(t.Context(), tx, recipeChanged(component)) })
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	var (
		aggregate, eventType string
		payload              []byte
		createdAt            time.Time
		publishedAt          *time.Time
	)
	err = d.Pool().QueryRow(t.Context(), "select aggregate, event_type, payload, created_at, published_at from outbox").
		Scan(&aggregate, &eventType, &payload, &createdAt, &publishedAt)
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	if err := json.Unmarshal(payload, &p); err != nil || p["component_id"] != component.String() || p["version"] != float64(2) {
		t.Errorf("payload = %s, want the component id and version", payload)
	}
	if aggregate != "component" || eventType != "catalog.recipe_changed" || !createdAt.Equal(at) || publishedAt != nil {
		t.Errorf("event = %q %q %v published %v; want unpublished, at the clock time", aggregate, eventType, createdAt, publishedAt)
	}
}

func TestAppend_RollsBackWithTheChange(t *testing.T) {
	d := dbtest.New(t)
	errChange := errors.New("change failed")

	err := d.InTx(t.Context(), func(tx pgx.Tx) error {
		if err := outbox.Append(t.Context(), tx, recipeChanged(uuid.New())); err != nil {
			return err
		}
		return errChange
	})

	if !errors.Is(err, errChange) || count(t, d) != 0 {
		t.Errorf("InTx() = %v with %d events, want the change's error and no event", err, count(t, d))
	}
}

func TestAppend_RejectsMalformedEvents(t *testing.T) {
	d := dbtest.New(t)
	badType := recipeChanged(uuid.New())
	badType.Type = "RecipeChanged"
	badAggregate := recipeChanged(uuid.New())
	badAggregate.Aggregate = "Component"
	badPayload := recipeChanged(uuid.New())
	badPayload.Payload = func() {}

	for name, e := range map[string]outbox.Event{"type": badType, "aggregate": badAggregate, "payload": badPayload} {
		t.Run(name, func(t *testing.T) {
			if err := d.InTx(t.Context(), func(tx pgx.Tx) error { return outbox.Append(t.Context(), tx, e) }); err == nil {
				t.Error("Append() error = nil")
			}
		})
	}
	if n := count(t, d); n != 0 {
		t.Errorf("events = %d, want 0", n)
	}
}
