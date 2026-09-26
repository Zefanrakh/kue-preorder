package identity_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/identity/identitytest"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
)

var checkoutAt = time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC)

func TestEnsureCustomer(t *testing.T) {
	tenant := uuid.New()
	svc := newService(t, tenant, identitytest.NewRepository(tenant))
	user := identity.AuthUser{ID: uuid.New(), Phone: "+6281234567890"}
	ctx := identity.WithAuthUser(t.Context(), user)

	first, err := svc.EnsureCustomer(ctx, identity.CustomerInput{Name: "  Sari  ", Email: "sari@contoh.com"}, checkoutAt)
	if err != nil || first.Name != "Sari" || first.Phone != "+6281234567890" || first.Email != "sari@contoh.com" {
		t.Fatalf("EnsureCustomer() = %+v, %v; want Sari with the token's phone", first, err)
	}
	again, err := svc.EnsureCustomer(ctx, identity.CustomerInput{Name: "Sari W."}, checkoutAt)
	if err != nil || again.ID != first.ID || again.Name != "Sari W." || again.Email != "" {
		t.Errorf("second checkout = %+v, %v; want the same record, updated", again, err)
	}
	if p, _ := svc.Principal(ctx); p.CustomerID == nil || *p.CustomerID != first.ID {
		t.Errorf("Principal().CustomerID = %v, want %s", p.CustomerID, first.ID)
	}
}

func TestEnsureCustomer_NeedsAVerifiedPhone(t *testing.T) {
	tenant := uuid.New()
	svc := newService(t, tenant, identitytest.NewRepository(tenant))

	if _, err := svc.EnsureCustomer(t.Context(), identity.CustomerInput{Name: "Sari"}, checkoutAt); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Errorf("anonymous EnsureCustomer() error = %v, want ErrUnauthenticated", err)
	}
	emailOnly := identity.WithAuthUser(t.Context(), identity.AuthUser{ID: uuid.New(), Email: "staf@contoh.com"})
	_, err := svc.EnsureCustomer(emailOnly, identity.CustomerInput{Name: "Staf"}, checkoutAt)
	var p *apperr.PreconditionError
	if !errors.As(err, &p) || p.Reason != "phone_required" {
		t.Errorf("EnsureCustomer(no phone) error = %v, want the phone_required precondition", err)
	}
}

func TestEnsureCustomer_Validates(t *testing.T) {
	tenant := uuid.New()
	svc := newService(t, tenant, identitytest.NewRepository(tenant))
	ctx := identity.WithAuthUser(t.Context(), identity.AuthUser{ID: uuid.New(), Phone: "+6281234567890"})
	long := string(make([]rune, 101))

	tests := []struct {
		name  string
		in    identity.CustomerInput
		field string
	}{
		{"no name", identity.CustomerInput{Name: " "}, "customer_name"},
		{"name too long", identity.CustomerInput{Name: long}, "customer_name"},
		{"bad email", identity.CustomerInput{Name: "Sari", Email: "sari@"}, "customer_email"},
		{"email with a display name", identity.CustomerInput{Name: "Sari", Email: "Sari <sari@contoh.com>"}, "customer_email"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.EnsureCustomer(ctx, tt.in, checkoutAt)
			var v *apperr.ValidationError
			if !errors.As(err, &v) || v.Fields[tt.field] == "" {
				t.Errorf("EnsureCustomer() error = %v, want a message on %s", err, tt.field)
			}
		})
	}
}
