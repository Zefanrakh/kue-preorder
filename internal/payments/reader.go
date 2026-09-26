package payments

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
)

// Repository is the persistence port of payments.
type Repository interface {
	// Policy returns apperr.ErrNotFound when the tenant never changed its policy.
	Policy(ctx context.Context, tenantID uuid.UUID) (Policy, error)
}

// Reader lets other modules read payment settings in-process. Checkout acts
// for a tenant, not for a person, so Reader checks no roles.
type Reader struct {
	repo Repository
}

// NewReader returns a Reader over repo.
func NewReader(repo Repository) *Reader {
	return &Reader{repo: repo}
}

// Policy returns the tenant's payment policy, or the defaults when it has none.
func (r *Reader) Policy(ctx context.Context, tenantID uuid.UUID) (Policy, error) {
	p, err := r.repo.Policy(ctx, tenantID)
	if errors.Is(err, apperr.ErrNotFound) {
		return DefaultPolicy(), nil
	}
	return p, err
}
