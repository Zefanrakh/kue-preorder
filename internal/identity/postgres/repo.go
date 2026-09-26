package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Zefanrakh/kue-preorder/internal/identity"
)

// Repository implements identity.Repository on the sqlc queries.
type Repository struct {
	q *Queries
}

var _ identity.Repository = (*Repository)(nil)

// NewRepository returns a repository over db, a pool or a transaction.
func NewRepository(db DBTX) *Repository {
	return &Repository{q: New(db)}
}

// ListTenantIDs implements identity.Repository.
func (r *Repository) ListTenantIDs(ctx context.Context, limit int32) ([]uuid.UUID, error) {
	tenants, err := r.q.ListTenants(ctx, limit)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(tenants))
	for i, t := range tenants {
		ids[i] = t.ID
	}
	return ids, nil
}

// ListStaffRoles implements identity.Repository.
func (r *Repository) ListStaffRoles(ctx context.Context, tenantID, authUserID uuid.UUID) ([]identity.Role, error) {
	rows, err := r.q.ListStaffRoles(ctx, ListStaffRolesParams{TenantID: tenantID, AuthUserID: authUserID})
	if err != nil {
		return nil, err
	}
	roles := make([]identity.Role, len(rows))
	for i, role := range rows {
		roles[i] = identity.Role(role)
	}
	return roles, nil
}

// FindCustomerID implements identity.Repository.
func (r *Repository) FindCustomerID(ctx context.Context, tenantID, authUserID uuid.UUID) (uuid.UUID, error) {
	c, err := r.q.GetCustomerByAuthUser(ctx, GetCustomerByAuthUserParams{TenantID: tenantID, AuthUserID: authUserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.UUID{}, identity.ErrNotFound
	}
	if err != nil {
		return uuid.UUID{}, err
	}
	return c.ID, nil
}

// UpsertCustomer implements identity.Repository.
func (r *Repository) UpsertCustomer(ctx context.Context, tenantID, authUserID uuid.UUID, c identity.Customer, at time.Time) (identity.Customer, error) {
	var email *string
	if c.Email != "" {
		email = &c.Email
	}
	row, err := r.q.UpsertCustomer(ctx, UpsertCustomerParams{
		TenantID: tenantID, AuthUserID: &authUserID, Name: c.Name, Email: email, Phone: &c.Phone, CreatedAt: at,
	})
	if err != nil {
		return identity.Customer{}, err
	}
	out := identity.Customer{ID: row.ID, Name: row.Name}
	if row.Email != nil {
		out.Email = *row.Email
	}
	if row.Phone != nil {
		out.Phone = *row.Phone
	}
	return out, nil
}
