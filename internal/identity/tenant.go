package identity

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// TenantResolver decides which tenant a request belongs to (§6).
type TenantResolver interface {
	TenantID(ctx context.Context) uuid.UUID
}

// SingleTenant resolves every request to the one tenant in the database, until
// tenants are resolved from the login or domain (§25).
type SingleTenant struct {
	id uuid.UUID
}

// TenantID returns the single tenant.
func (s SingleTenant) TenantID(context.Context) uuid.UUID {
	return s.id
}

// ResolveSingleTenant loads the only tenant. It fails when there is none or
// more than one: guessing would mix up the data of different shops.
func ResolveSingleTenant(ctx context.Context, repo Repository) (SingleTenant, error) {
	ids, err := repo.ListTenantIDs(ctx, 2)
	if err != nil {
		return SingleTenant{}, fmt.Errorf("list tenants: %w", err)
	}
	switch len(ids) {
	case 0:
		return SingleTenant{}, fmt.Errorf("%w: no tenant found; run the migrations", ErrTenantSetup)
	case 1:
		return SingleTenant{id: ids[0]}, nil
	default:
		return SingleTenant{}, fmt.Errorf("%w: more than one tenant found; single-tenant mode needs exactly one", ErrTenantSetup)
	}
}
