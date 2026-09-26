package identity

import (
	"context"
	"time"

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
	// UpsertCustomer creates the user's customer record in the tenant, or
	// updates its name, email, and phone, and returns it.
	UpsertCustomer(ctx context.Context, tenantID, authUserID uuid.UUID, c Customer, at time.Time) (Customer, error)
}
