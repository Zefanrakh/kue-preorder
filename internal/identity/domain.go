// Package identity verifies Supabase Auth access tokens, resolves the tenant of
// a request, and tells who the caller is to the shop: staff roles and customer
// record (docs/architecture.md §6, §8).
//
// Authentication happens once per request in the Connect interceptor, which
// puts the verified AuthUser in the context. Authorization belongs to each
// module's service, which asks Service.Principal who the caller is.
package identity

import (
	"context"
	"errors"
	"slices"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
)

// Role is a staff role in a tenant, stored in staff_roles.
type Role string

// Staff roles (§8). Customers have no role.
const (
	RoleOwner   Role = "owner"
	RoleKitchen Role = "kitchen"
)

var (
	// ErrUnauthenticated means the request carries no verified user. It is
	// the shared apperr.ErrUnauthenticated, which every module maps alike.
	ErrUnauthenticated = apperr.ErrUnauthenticated
	// ErrInvalidToken means an access token failed verification.
	ErrInvalidToken = errors.New("invalid access token")
	// ErrNotFound means the requested record does not exist.
	ErrNotFound = errors.New("not found")
	// ErrTenantSetup means the tenants table does not fit single-tenant mode.
	ErrTenantSetup = errors.New("tenant setup")
)

// AuthUser is the Supabase Auth user proven by a verified access token.
type AuthUser struct {
	ID    uuid.UUID
	Email string
}

// Principal is the caller of a request as the shop sees them.
type Principal struct {
	AuthUserID uuid.UUID
	TenantID   uuid.UUID
	// Roles are the staff roles in TenantID, sorted; empty for customers.
	Roles []Role
	// CustomerID is nil until the user has a customer record in TenantID.
	CustomerID *uuid.UUID
}

// HasRole reports whether the principal holds role.
func (p Principal) HasRole(role Role) bool {
	return slices.Contains(p.Roles, role)
}

type authUserKey struct{}

// WithAuthUser returns a copy of ctx carrying the verified user.
func WithAuthUser(ctx context.Context, user AuthUser) context.Context {
	return context.WithValue(ctx, authUserKey{}, user)
}

// AuthUserFrom returns the verified user in ctx, if any.
func AuthUserFrom(ctx context.Context) (AuthUser, bool) {
	user, ok := ctx.Value(authUserKey{}).(AuthUser)
	return user, ok
}
