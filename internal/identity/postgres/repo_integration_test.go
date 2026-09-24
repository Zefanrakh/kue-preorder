//go:build integration

package postgres_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/identity/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
)

func TestResolveSingleTenant_FindsSeededTenant(t *testing.T) {
	repo := postgres.NewRepository(dbtest.New(t).Pool())

	tenants, err := identity.ResolveSingleTenant(t.Context(), repo)

	if err != nil {
		t.Fatalf("ResolveSingleTenant() error = %v", err)
	}
	if got := tenants.TenantID(t.Context()); got != dbtest.DefaultTenantID {
		t.Errorf("TenantID() = %s, want the seeded %s", got, dbtest.DefaultTenantID)
	}
}

func TestResolveSingleTenant_RefusesSecondTenant(t *testing.T) {
	d := dbtest.New(t)
	dbtest.CreateTenant(t, d, "Toko Lain")

	_, err := identity.ResolveSingleTenant(t.Context(), postgres.NewRepository(d.Pool()))

	if !errors.Is(err, identity.ErrTenantSetup) {
		t.Errorf("ResolveSingleTenant() error = %v, want ErrTenantSetup", err)
	}
}

func TestRepository_ListStaffRoles(t *testing.T) {
	d := dbtest.New(t)
	q := postgres.New(d.Pool())
	user := uuid.New()
	for _, role := range []string{"owner", "kitchen"} {
		if err := q.GrantStaffRole(t.Context(), postgres.GrantStaffRoleParams{
			TenantID: dbtest.DefaultTenantID, AuthUserID: user, Role: role, CreatedAt: createdAt,
		}); err != nil {
			t.Fatalf("GrantStaffRole(%s) error = %v", role, err)
		}
	}

	got, err := postgres.NewRepository(d.Pool()).ListStaffRoles(t.Context(), dbtest.DefaultTenantID, user)

	if err != nil {
		t.Fatalf("ListStaffRoles() error = %v", err)
	}
	if want := []identity.Role{identity.RoleKitchen, identity.RoleOwner}; !slices.Equal(got, want) {
		t.Errorf("ListStaffRoles() = %v, want %v", got, want)
	}
}

func TestRepository_FindCustomerID(t *testing.T) {
	d := dbtest.New(t)
	repo := postgres.NewRepository(d.Pool())
	user := uuid.New()
	created, err := postgres.New(d.Pool()).CreateCustomer(t.Context(), postgres.CreateCustomerParams{
		TenantID: dbtest.DefaultTenantID, AuthUserID: &user, Name: "Sari", CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatalf("CreateCustomer() error = %v", err)
	}

	got, err := repo.FindCustomerID(t.Context(), dbtest.DefaultTenantID, user)
	if err != nil || got != created.ID {
		t.Errorf("FindCustomerID(member) = %s, %v; want %s", got, err, created.ID)
	}

	_, err = repo.FindCustomerID(t.Context(), dbtest.DefaultTenantID, uuid.New())
	if !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("FindCustomerID(stranger) error = %v, want identity.ErrNotFound", err)
	}
}
