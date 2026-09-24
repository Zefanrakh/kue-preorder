package identitytest

import (
	"context"
	"slices"
	"sync"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/identity"
)

// Repository is an in-memory identity.Repository. It is safe for concurrent use.
type Repository struct {
	mu        sync.Mutex
	tenantIDs []uuid.UUID
	roles     map[membership][]identity.Role
	customers map[membership]uuid.UUID
	err       error
}

type membership struct {
	tenant, user uuid.UUID
}

var _ identity.Repository = (*Repository)(nil)

// NewRepository returns a repository holding the given tenants.
func NewRepository(tenantIDs ...uuid.UUID) *Repository {
	return &Repository{
		tenantIDs: tenantIDs,
		roles:     map[membership][]identity.Role{},
		customers: map[membership]uuid.UUID{},
	}
}

// Grant gives user the roles in tenant.
func (r *Repository) Grant(tenant, user uuid.UUID, roles ...identity.Role) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := membership{tenant, user}
	r.roles[m] = append(r.roles[m], roles...)
	slices.Sort(r.roles[m])
	r.roles[m] = slices.Compact(r.roles[m])
}

// AddCustomer creates a customer record for user in tenant and returns its id.
func (r *Repository) AddCustomer(tenant, user uuid.UUID) uuid.UUID {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := uuid.New()
	r.customers[membership{tenant, user}] = id
	return id
}

// FailWith makes every method return err, as a broken database would.
func (r *Repository) FailWith(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

// ListTenantIDs implements identity.Repository.
func (r *Repository) ListTenantIDs(_ context.Context, limit int32) ([]uuid.UUID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	return slices.Clone(r.tenantIDs[:min(int(limit), len(r.tenantIDs))]), nil
}

// ListStaffRoles implements identity.Repository.
func (r *Repository) ListStaffRoles(_ context.Context, tenantID, authUserID uuid.UUID) ([]identity.Role, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	return append([]identity.Role{}, r.roles[membership{tenantID, authUserID}]...), nil
}

// FindCustomerID implements identity.Repository.
func (r *Repository) FindCustomerID(_ context.Context, tenantID, authUserID uuid.UUID) (uuid.UUID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return uuid.UUID{}, r.err
	}
	id, ok := r.customers[membership{tenantID, authUserID}]
	if !ok {
		return uuid.UUID{}, identity.ErrNotFound
	}
	return id, nil
}
