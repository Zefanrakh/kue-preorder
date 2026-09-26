package identity

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
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

// EnsureCustomer creates or updates the caller's customer record at
// checkout. The caller must have signed in with a phone number: the number
// is taken from the token, the name and email from what they typed.
func (s *Service) EnsureCustomer(ctx context.Context, in CustomerInput, at time.Time) (Customer, error) {
	user, ok := AuthUserFrom(ctx)
	if !ok {
		return Customer{}, ErrUnauthenticated
	}
	if user.Phone == "" {
		return Customer{}, ErrPhoneRequired
	}
	c := Customer{Name: strings.TrimSpace(in.Name), Email: strings.TrimSpace(in.Email), Phone: user.Phone}
	f := apperr.Fields{}
	f.Check(c.Name != "", "customer_name", "Nama wajib diisi.")
	f.Check(utf8.RuneCountInString(c.Name) <= 100, "customer_name", "Nama paling panjang 100 karakter.")
	f.Check(c.Email == "" || validEmail(c.Email), "customer_email", "Email tidak valid, misalnya nama@contoh.com.")
	if err := f.Err(); err != nil {
		return Customer{}, err
	}
	return s.repo.UpsertCustomer(ctx, s.tenants.TenantID(ctx), user.ID, c, at)
}

func validEmail(s string) bool {
	addr, err := mail.ParseAddress(s)
	return err == nil && addr.Address == s && len(s) <= 254
}
