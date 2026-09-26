package scheduling

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

// Repository is the persistence port of scheduling. Every method is scoped
// to one tenant.
type Repository interface {
	// Settings returns ErrNotFound when the tenant never changed its settings.
	Settings(ctx context.Context, tenantID uuid.UUID) (Settings, error)
	SaveSettings(ctx context.Context, tenantID uuid.UUID, s Settings, at time.Time) (Settings, error)
	// ClosedDates returns the closed dates from..to, both included, in order.
	ClosedDates(ctx context.Context, tenantID uuid.UUID, from, to clock.Date) ([]ClosedDate, error)
	// AddClosedDate returns a conflict *ValidationError on "date" when the
	// date is already closed.
	AddClosedDate(ctx context.Context, tenantID uuid.UUID, d clock.Date, reason string, at time.Time) (ClosedDate, error)
	// RemoveClosedDate returns ErrNotFound when the date is not closed.
	RemoveClosedDate(ctx context.Context, tenantID uuid.UUID, d clock.Date) error
}

// PrincipalSource tells who is calling; identity.Service implements it.
type PrincipalSource interface {
	Principal(ctx context.Context) (identity.Principal, error)
}

// Who may do what (§8): the owner sets the rules; the kitchen also closes
// days, since ibu knows best when she cannot bake.
var (
	owners = []identity.Role{identity.RoleOwner}
	staff  = []identity.Role{identity.RoleOwner, identity.RoleKitchen}
)

// maxClosedDateRange bounds one listing of closed dates, in days.
const maxClosedDateRange = 366

// Service manages the scheduling settings and closed dates in the CMS.
type Service struct {
	repo       Repository
	principals PrincipalSource
	clock      clock.Clock
}

// NewService returns a Service storing through repo.
func NewService(repo Repository, principals PrincipalSource, clk clock.Clock) *Service {
	return &Service{repo: repo, principals: principals, clock: clk}
}

func (s *Service) authorize(ctx context.Context, roles []identity.Role) (identity.Principal, error) {
	p, err := s.principals.Principal(ctx)
	if err != nil {
		return identity.Principal{}, err
	}
	if !slices.ContainsFunc(roles, p.HasRole) {
		return identity.Principal{}, ErrForbidden
	}
	return p, nil
}

// Settings returns the tenant's settings, or the defaults when it has none.
// Staff only.
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	p, err := s.authorize(ctx, staff)
	if err != nil {
		return Settings{}, err
	}
	settings, err := s.repo.Settings(ctx, p.TenantID)
	if errors.Is(err, ErrNotFound) {
		return DefaultSettings(), nil
	}
	return settings, err
}

// UpdateSettings replaces the settings. They apply to orders placed from
// now on; orders already placed keep the times locked at checkout. Owners
// only.
func (s *Service) UpdateSettings(ctx context.Context, in Settings) (Settings, error) {
	p, err := s.authorize(ctx, owners)
	if err != nil {
		return Settings{}, err
	}
	if err := in.validate(); err != nil {
		return Settings{}, err
	}
	return s.repo.SaveSettings(ctx, p.TenantID, in, s.clock.Now())
}

// ClosedDates returns the closed dates from..to, both included. Staff only.
func (s *Service) ClosedDates(ctx context.Context, from, to clock.Date) ([]ClosedDate, error) {
	p, err := s.authorize(ctx, staff)
	if err != nil {
		return nil, err
	}
	f := apperr.Fields{}
	f.Check(!to.Before(from), "to_date", "Tanggal akhir tidak boleh sebelum tanggal awal.")
	f.Check(!to.After(from.AddDays(maxClosedDateRange)), "to_date", "Paling panjang 366 hari sekali tampil.")
	if err := f.Err(); err != nil {
		return nil, err
	}
	return s.repo.ClosedDates(ctx, p.TenantID, from, to)
}

// AddClosedDate closes a day, from today on. Staff: the owner and the kitchen.
func (s *Service) AddClosedDate(ctx context.Context, d clock.Date, reason string) (ClosedDate, error) {
	p, err := s.authorize(ctx, staff)
	if err != nil {
		return ClosedDate{}, err
	}
	reason = strings.TrimSpace(reason)
	f := apperr.Fields{}
	f.Check(!d.Before(clock.DateOf(s.clock.Now())), "date", "Tanggal yang sudah lewat tidak bisa diliburkan.")
	f.Check(reason != "", "reason", "Alasan libur wajib diisi, misalnya Libur Lebaran.")
	f.Check(len(reason) <= 200, "reason", "Alasan libur paling panjang 200 karakter.")
	if err := f.Err(); err != nil {
		return ClosedDate{}, err
	}
	return s.repo.AddClosedDate(ctx, p.TenantID, d, reason, s.clock.Now())
}

// RemoveClosedDate opens a closed day again. Staff.
func (s *Service) RemoveClosedDate(ctx context.Context, d clock.Date) error {
	p, err := s.authorize(ctx, staff)
	if err != nil {
		return err
	}
	return s.repo.RemoveClosedDate(ctx, p.TenantID, d)
}
