package scheduling

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

// Reader lets checkout read a tenant's scheduling rules in-process. Checkout
// acts for a tenant, not for a signed-in person, so Reader checks no roles.
type Reader struct {
	repo Repository
}

// NewReader returns a Reader over repo.
func NewReader(repo Repository) *Reader {
	return &Reader{repo: repo}
}

// Settings returns the tenant's settings, or the defaults when it has none.
func (r *Reader) Settings(ctx context.Context, tenantID uuid.UUID) (Settings, error) {
	s, err := r.repo.Settings(ctx, tenantID)
	if errors.Is(err, ErrNotFound) {
		return DefaultSettings(), nil
	}
	return s, err
}

// ClosedDates returns the closed dates from..to, both included, with their
// reasons, as Request.Closed takes them.
func (r *Reader) ClosedDates(ctx context.Context, tenantID uuid.UUID, from, to clock.Date) (map[clock.Date]string, error) {
	dates, err := r.repo.ClosedDates(ctx, tenantID, from, to)
	if err != nil {
		return nil, err
	}
	out := make(map[clock.Date]string, len(dates))
	for _, d := range dates {
		out[d.Date] = d.Reason
	}
	return out, nil
}
