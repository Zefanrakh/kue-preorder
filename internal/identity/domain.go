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
	// Phone is the number the user proved with an OTP, in E.164 (+62...);
	// empty for a user who signed in another way (§8).
	Phone string
}

// Customer is a signed-in customer's record in a tenant.
type Customer struct {
	ID    uuid.UUID
	Name  string
	Email string // optional
	Phone string // E.164, from the verified token
}

// CustomerInput is what a customer types at checkout. The phone number is
// never typed: it comes from the token.
type CustomerInput struct {
	Name, Email string
}

// ErrPhoneRequired refuses an order from a user without a verified phone
// number: they must sign in with WhatsApp first (§8).
var ErrPhoneRequired = &apperr.PreconditionError{
	Reason:  "phone_required",
	Message: "Masuk dengan nomor WhatsApp dulu untuk memesan.",
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
