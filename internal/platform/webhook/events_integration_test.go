//go:build integration

package webhook_test

import (
	"testing"

	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
	"github.com/Zefanrakh/kue-preorder/internal/platform/webhook"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

func TestEvents(t *testing.T) {
	d := dbtest.New(t)
	events := webhook.NewEvents(d)
	ctx := t.Context()

	claim := func(id string) bool {
		t.Helper()
		ok, err := events.Claim(ctx, "supabase_auth", id, now)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	if !claim("msg_1") || claim("msg_1") {
		t.Fatal("want the first claim new and the second not")
	}
	if !claim("msg_2") {
		t.Error("another delivery is new")
	}

	// A failed effect is released and can be tried again.
	if err := events.Release(ctx, "supabase_auth", "msg_2"); err != nil {
		t.Fatal(err)
	}
	if !claim("msg_2") {
		t.Error("after Release, want the retry claimed as new")
	}

	// A handled delivery stays handled, even if released by mistake.
	if err := events.Done(ctx, "supabase_auth", "msg_1", now); err != nil {
		t.Fatal(err)
	}
	if err := events.Release(ctx, "supabase_auth", "msg_1"); err != nil {
		t.Fatal(err)
	}
	if claim("msg_1") {
		t.Error("a handled delivery was claimed again")
	}
	var processed int
	if err := d.Pool().QueryRow(ctx, "select count(*) from webhook_events where processed_at is not null").Scan(&processed); err != nil || processed != 1 {
		t.Errorf("processed deliveries = %d, %v; want 1", processed, err)
	}
}
