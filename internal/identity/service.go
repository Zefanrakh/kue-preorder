package identity

import (
	"context"
	"errors"
	"fmt"
)

// Service tells who the caller of a request is.
type Service struct {
	repo    Repository
	tenants TenantResolver
}

// NewService returns a Service reading from repo.
func NewService(repo Repository, tenants TenantResolver) *Service {
	return &Service{repo: repo, tenants: tenants}
}

// Principal returns the caller of the request in ctx, or ErrUnauthenticated
// when the request carries no verified user.
func (s *Service) Principal(ctx context.Context) (Principal, error) {
	user, ok := AuthUserFrom(ctx)
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	tenantID := s.tenants.TenantID(ctx)

	roles, err := s.repo.ListStaffRoles(ctx, tenantID, user.ID)
	if err != nil {
		return Principal{}, fmt.Errorf("list staff roles of %s: %w", user.ID, err)
	}
	p := Principal{AuthUserID: user.ID, TenantID: tenantID, Roles: roles}

	customerID, err := s.repo.FindCustomerID(ctx, tenantID, user.ID)
	switch {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return Principal{}, fmt.Errorf("find customer of %s: %w", user.ID, err)
	default:
		p.CustomerID = &customerID
	}
	return p, nil
}
