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
	// FeeRules returns the rules the tenant set, by method; none when it
	// set none.
	FeeRules(ctx context.Context, tenantID uuid.UUID) (map[Method]FeeRule, error)
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

// FeeRules returns the fee rule of every method: the tenant's own where it
// set one, the defaults otherwise.
func (r *Reader) FeeRules(ctx context.Context, tenantID uuid.UUID) (map[Method]FeeRule, error) {
	own, err := r.repo.FeeRules(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	rules := DefaultFeeRules()
	for m, rule := range own {
		rules[m] = rule
	}
	return rules, nil
}
