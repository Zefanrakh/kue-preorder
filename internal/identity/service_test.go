package identity_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/identity/identitytest"
)

func newService(t *testing.T, tenantID uuid.UUID, repo *identitytest.Repository) *identity.Service {
	t.Helper()
	tenants, err := identity.ResolveSingleTenant(t.Context(), repo)
	if err != nil {
		t.Fatalf("ResolveSingleTenant() error = %v", err)
	}
	if got := tenants.TenantID(t.Context()); got != tenantID {
		t.Fatalf("resolved tenant %s, want %s", got, tenantID)
	}
	return identity.NewService(repo, tenants)
}

func TestPrincipal_WithoutUserIsUnauthenticated(t *testing.T) {
	tenant := uuid.New()
	svc := newService(t, tenant, identitytest.NewRepository(tenant))

	_, err := svc.Principal(t.Context())

	if !errors.Is(err, identity.ErrUnauthenticated) {
		t.Errorf("Principal() error = %v, want ErrUnauthenticated", err)
	}
}

func TestPrincipal_StaffMember(t *testing.T) {
	tenant, user := uuid.New(), uuid.New()
	repo := identitytest.NewRepository(tenant)
	repo.Grant(tenant, user, identity.RoleOwner, identity.RoleKitchen)
	svc := newService(t, tenant, repo)

	p, err := svc.Principal(identity.WithAuthUser(t.Context(), identity.AuthUser{ID: user}))

	if err != nil {
		t.Fatalf("Principal() error = %v", err)
	}
	if p.AuthUserID != user || p.TenantID != tenant {
		t.Errorf("Principal() = %+v, want user %s in tenant %s", p, user, tenant)
	}
	if want := []identity.Role{identity.RoleKitchen, identity.RoleOwner}; !slices.Equal(p.Roles, want) {
		t.Errorf("Roles = %v, want %v", p.Roles, want)
	}
	if !p.HasRole(identity.RoleOwner) {
		t.Error("HasRole(owner) = false, want true")
	}
	if p.CustomerID != nil {
		t.Errorf("CustomerID = %s, want nil for staff without a customer record", p.CustomerID)
	}
}

func TestPrincipal_Customer(t *testing.T) {
	tenant, user := uuid.New(), uuid.New()
	repo := identitytest.NewRepository(tenant)
	customerID := repo.AddCustomer(tenant, user)
	svc := newService(t, tenant, repo)

	p, err := svc.Principal(identity.WithAuthUser(t.Context(), identity.AuthUser{ID: user}))

	if err != nil {
		t.Fatalf("Principal() error = %v", err)
	}
	if p.CustomerID == nil || *p.CustomerID != customerID {
		t.Errorf("CustomerID = %v, want %s", p.CustomerID, customerID)
	}
	if len(p.Roles) != 0 || p.HasRole(identity.RoleOwner) {
		t.Errorf("Roles = %v, want none for a customer", p.Roles)
	}
}

func TestPrincipal_IgnoresRolesInOtherTenants(t *testing.T) {
	tenant, user := uuid.New(), uuid.New()
	repo := identitytest.NewRepository(tenant)
	repo.Grant(uuid.New(), user, identity.RoleOwner) // a tenant this request does not belong to
	svc := newService(t, tenant, repo)

	p, err := svc.Principal(identity.WithAuthUser(t.Context(), identity.AuthUser{ID: user}))

	if err != nil {
		t.Fatalf("Principal() error = %v", err)
	}
	if p.HasRole(identity.RoleOwner) {
		t.Error("HasRole(owner) = true, want false: the role belongs to another tenant")
	}
}

func TestPrincipal_PropagatesRepositoryError(t *testing.T) {
	tenant := uuid.New()
	repo := identitytest.NewRepository(tenant)
	svc := newService(t, tenant, repo)
	errDown := errors.New("database down")
	repo.FailWith(errDown)

	_, err := svc.Principal(identity.WithAuthUser(t.Context(), identity.AuthUser{ID: uuid.New()}))

	if !errors.Is(err, errDown) || errors.Is(err, identity.ErrUnauthenticated) {
		t.Errorf("Principal() error = %v, want the repository error", err)
	}
}
