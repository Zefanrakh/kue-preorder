package identity

import (
	"context"

	"github.com/google/uuid"
)

// Repository is the persistence port of the identity module.
type Repository interface {
	// ListTenantIDs returns up to limit tenant ids, oldest first.
	ListTenantIDs(ctx context.Context, limit int32) ([]uuid.UUID, error)
	// ListStaffRoles returns the user's roles in the tenant, sorted.
	ListStaffRoles(ctx context.Context, tenantID, authUserID uuid.UUID) ([]Role, error)
	// FindCustomerID returns ErrNotFound when the user has no customer
	// record in the tenant.
	FindCustomerID(ctx context.Context, tenantID, authUserID uuid.UUID) (uuid.UUID, error)
}
