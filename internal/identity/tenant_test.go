package identity_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/identity/identitytest"
)

func TestResolveSingleTenant(t *testing.T) {
	only := uuid.New()
	tests := []struct {
		name    string
		repo    *identitytest.Repository
		want    uuid.UUID
		wantErr error
	}{
		{"exactly one tenant", identitytest.NewRepository(only), only, nil},
		{"no tenant", identitytest.NewRepository(), uuid.Nil, identity.ErrTenantSetup},
		{"more than one tenant", identitytest.NewRepository(only, uuid.New()), uuid.Nil, identity.ErrTenantSetup},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tenants, err := identity.ResolveSingleTenant(t.Context(), tt.repo)

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ResolveSingleTenant() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil {
				if got := tenants.TenantID(t.Context()); got != tt.want {
					t.Errorf("TenantID() = %s, want %s", got, tt.want)
				}
			}
		})
	}
}

func TestResolveSingleTenant_PropagatesRepositoryError(t *testing.T) {
	errDown := errors.New("database down")
	repo := identitytest.NewRepository(uuid.New())
	repo.FailWith(errDown)

	_, err := identity.ResolveSingleTenant(t.Context(), repo)

	if !errors.Is(err, errDown) {
		t.Errorf("ResolveSingleTenant() error = %v, want %v", err, errDown)
	}
}
