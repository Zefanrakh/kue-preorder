//go:build integration

package postgres_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Zefanrakh/kue-preorder/internal/identity/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

var createdAt = time.Date(2026, 9, 24, 3, 0, 0, 0, time.UTC)

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func ptr[T any](v T) *T { return &v }

func TestListTenants_ReturnsSeededDefaultTenant(t *testing.T) {
	q := postgres.New(dbtest.New(t).Pool())

	tenants, err := q.ListTenants(t.Context(), 2)
	if err != nil {
		t.Fatalf("ListTenants() error = %v", err)
	}
	if len(tenants) != 1 || tenants[0].ID != dbtest.DefaultTenantID {
		t.Errorf("ListTenants() = %+v, want only the default tenant %s", tenants, dbtest.DefaultTenantID)
	}
}

func TestListStaffRoles_ScopedByTenant(t *testing.T) {
	d := dbtest.New(t)
	q := postgres.New(d.Pool())
	tenantA := dbtest.DefaultTenantID
	tenantB := dbtest.CreateTenant(t, d, "Toko Lain")
	user := uuid.New()
	for _, g := range []postgres.GrantStaffRoleParams{
		{TenantID: tenantA, AuthUserID: user, Role: "owner", CreatedAt: createdAt},
		{TenantID: tenantA, AuthUserID: user, Role: "kitchen", CreatedAt: createdAt},
		{TenantID: tenantB, AuthUserID: user, Role: "kitchen", CreatedAt: createdAt},
	} {
		if err := q.GrantStaffRole(t.Context(), g); err != nil {
			t.Fatalf("GrantStaffRole(%+v) error = %v", g, err)
		}
	}

	tests := []struct {
		name   string
		tenant uuid.UUID
		user   uuid.UUID
		want   []string
	}{
		{"both roles in tenant A", tenantA, user, []string{"kitchen", "owner"}},
		{"only tenant B's role", tenantB, user, []string{"kitchen"}},
		{"unknown user", tenantA, uuid.New(), []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := q.ListStaffRoles(t.Context(), postgres.ListStaffRolesParams{TenantID: tt.tenant, AuthUserID: tt.user})
			if err != nil {
				t.Fatalf("ListStaffRoles() error = %v", err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("ListStaffRoles() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGrantStaffRole_IsIdempotent(t *testing.T) {
	q := postgres.New(dbtest.New(t).Pool())
	grant := postgres.GrantStaffRoleParams{TenantID: dbtest.DefaultTenantID, AuthUserID: uuid.New(), Role: "owner", CreatedAt: createdAt}

	for i := range 2 {
		if err := q.GrantStaffRole(t.Context(), grant); err != nil {
			t.Fatalf("GrantStaffRole() call %d error = %v", i+1, err)
		}
	}

	got, err := q.ListStaffRoles(t.Context(), postgres.ListStaffRolesParams{TenantID: grant.TenantID, AuthUserID: grant.AuthUserID})
	if err != nil {
		t.Fatalf("ListStaffRoles() error = %v", err)
	}
	if !slices.Equal(got, []string{"owner"}) {
		t.Errorf("ListStaffRoles() = %q, want one owner role", got)
	}
}

func TestGrantStaffRole_RejectsUnknownRole(t *testing.T) {
	q := postgres.New(dbtest.New(t).Pool())

	err := q.GrantStaffRole(t.Context(), postgres.GrantStaffRoleParams{
		TenantID: dbtest.DefaultTenantID, AuthUserID: uuid.New(), Role: "admin", CreatedAt: createdAt,
	})

	if code := pgCode(err); code != "23514" { // check_violation
		t.Errorf("GrantStaffRole(role=admin) error = %v (code %q), want check_violation", err, code)
	}
}

func TestGetCustomerByAuthUser_ScopedByTenant(t *testing.T) {
	d := dbtest.New(t)
	q := postgres.New(d.Pool())
	otherTenant := dbtest.CreateTenant(t, d, "Toko Lain")
	user := uuid.New()
	created, err := q.CreateCustomer(t.Context(), postgres.CreateCustomerParams{
		TenantID: dbtest.DefaultTenantID, AuthUserID: &user, Name: "Sari",
		Email: ptr("sari@example.com"), Phone: ptr("+6281200000000"), CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatalf("CreateCustomer() error = %v", err)
	}

	got, err := q.GetCustomerByAuthUser(t.Context(), postgres.GetCustomerByAuthUserParams{TenantID: dbtest.DefaultTenantID, AuthUserID: user})
	if err != nil {
		t.Fatalf("GetCustomerByAuthUser(own tenant) error = %v", err)
	}
	if got.ID != created.ID || got.Name != "Sari" || !got.CreatedAt.Equal(createdAt) {
		t.Errorf("GetCustomerByAuthUser(own tenant) = %+v, want %+v", got, created)
	}

	_, err = q.GetCustomerByAuthUser(t.Context(), postgres.GetCustomerByAuthUserParams{TenantID: otherTenant, AuthUserID: user})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("GetCustomerByAuthUser(other tenant) error = %v, want pgx.ErrNoRows", err)
	}
}

func TestCreateCustomer_OneMemberRowPerTenantButManyGuests(t *testing.T) {
	d := dbtest.New(t)
	q := postgres.New(d.Pool())
	otherTenant := dbtest.CreateTenant(t, d, "Toko Lain")
	user := uuid.New()
	member := func(tenant uuid.UUID) postgres.CreateCustomerParams {
		return postgres.CreateCustomerParams{TenantID: tenant, AuthUserID: &user, Name: "Sari", CreatedAt: createdAt}
	}
	guest := postgres.CreateCustomerParams{TenantID: dbtest.DefaultTenantID, Name: "Tamu", Phone: ptr("+6281300000000"), CreatedAt: createdAt}

	if _, err := q.CreateCustomer(t.Context(), member(dbtest.DefaultTenantID)); err != nil {
		t.Fatalf("first member row: %v", err)
	}
	if _, err := q.CreateCustomer(t.Context(), member(otherTenant)); err != nil {
		t.Errorf("same member in another tenant: %v, want accepted", err)
	}
	for i := range 2 {
		if _, err := q.CreateCustomer(t.Context(), guest); err != nil {
			t.Errorf("guest row %d: %v, want accepted", i+1, err)
		}
	}

	_, err := q.CreateCustomer(t.Context(), member(dbtest.DefaultTenantID))
	if code := pgCode(err); code != "23505" { // unique_violation
		t.Errorf("second member row in one tenant: error = %v (code %q), want unique_violation", err, code)
	}
}
