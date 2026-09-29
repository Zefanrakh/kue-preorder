package aggregation

import (
	"cmp"
	"context"
	"errors"
	"slices"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

// Principals tells who is calling; identity.Service implements it.
type Principals interface {
	Principal(ctx context.Context) (identity.Principal, error)
}

// staff may see and recompute batches (§8): the kitchen shops from them.
var staff = []identity.Role{identity.RoleOwner, identity.RoleKitchen}

// maxListRange bounds one listing of batches, in days.
const maxListRange = 92

// View is a batch as the CMS shows it: what it makes and what to buy, with
// names.
type View struct {
	Batch
	Components []ComponentView
	// Lines come by supplier, then ingredient name; lines without a
	// supplier come last.
	Lines []LineView
	// CostIDR sums the lines whose pack has a price; Unpriced counts the
	// lines to buy whose cost is unknown.
	CostIDR  int64
	Unpriced int32
}

// ComponentView is a component total with its name.
type ComponentView struct {
	ComponentTotal
	Name      string
	UnitLabel string // "porsi", "loyang", "pcs"
}

// LineView is a shopping list line with its names.
type LineView struct {
	Line
	IngredientName string
	BaseUnit       catalog.BaseUnit
	SupplierName   string // empty without a default pack
}

// ServiceDeps are what Service works with.
type ServiceDeps struct {
	Engine     *Engine
	Repo       Repository
	Catalog    Catalog
	Principals Principals
}

// Service shows batches to staff in the CMS and lets them compute one
// again.
type Service struct {
	engine     *Engine
	repo       Repository
	catalog    Catalog
	principals Principals
}

// NewService returns a Service over d.
func NewService(d ServiceDeps) *Service {
	return &Service{engine: d.Engine, repo: d.Repo, catalog: d.Catalog, principals: d.Principals}
}

func (s *Service) authorize(ctx context.Context) (identity.Principal, error) {
	p, err := s.principals.Principal(ctx)
	if err != nil {
		return identity.Principal{}, err
	}
	if !slices.ContainsFunc(staff, p.HasRole) {
		return identity.Principal{}, apperr.ErrForbidden
	}
	return p, nil
}

// List returns the batches from..to, by date. Staff only.
func (s *Service) List(ctx context.Context, from, to clock.Date) ([]Summary, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return nil, err
	}
	f := apperr.Fields{}
	f.Check(!to.Before(from), "to_date", "Tanggal akhir tidak boleh sebelum tanggal awal.")
	f.Check(!to.After(from.AddDays(maxListRange)), "to_date", "Paling panjang 92 hari sekali tampil.")
	if err := f.Err(); err != nil {
		return nil, err
	}
	return s.repo.ListBatches(ctx, p.TenantID, from, to)
}

// Get returns the batch of date with its shopping list; apperr.ErrNotFound
// when no order was ever confirmed for that date. Staff only.
func (s *Service) Get(ctx context.Context, date clock.Date) (View, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, p.TenantID, date)
}

// Recompute computes the batch of date again now, such as after fixing a
// recipe or a pack price, and returns it. A broken recipe is refused with
// ErrBrokenRecipe; the batch then shows which recipe. Staff only.
func (s *Service) Recompute(ctx context.Context, date clock.Date) (View, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return View{}, err
	}
	if err := s.engine.Recompute(ctx, p.TenantID, date); err != nil {
		var re *RecipeError
		if errors.As(err, &re) {
			return View{}, ErrBrokenRecipe
		}
		return View{}, err
	}
	return s.view(ctx, p.TenantID, date)
}

func (s *Service) view(ctx context.Context, tenant uuid.UUID, date clock.Date) (View, error) {
	b, err := s.repo.GetBatch(ctx, tenant, date)
	if err != nil {
		return View{}, err
	}
	components, err := s.repo.Components(ctx, tenant, b.ID)
	if err != nil {
		return View{}, err
	}
	lines, err := s.repo.Lines(ctx, tenant, b.ID)
	if err != nil {
		return View{}, err
	}
	labels, err := s.catalog.Labels(ctx, tenant)
	if err != nil {
		return View{}, err
	}
	v := View{Batch: b}
	for _, c := range components {
		comp := labels.Components[c.ComponentID]
		v.Components = append(v.Components, ComponentView{ComponentTotal: c, Name: comp.Name, UnitLabel: comp.UnitLabel})
	}
	slices.SortFunc(v.Components, func(a, b ComponentView) int { return cmp.Compare(a.Name, b.Name) })
	for _, l := range lines {
		ing := labels.Ingredients[l.IngredientID]
		lv := LineView{Line: l, IngredientName: ing.Name, BaseUnit: ing.BaseUnit}
		if l.Pack != nil {
			lv.SupplierName = labels.Suppliers[l.Pack.SupplierID].Name
		}
		if cost, ok := l.CostIDR(); ok {
			v.CostIDR += cost
		} else if l.ToBuy > 0 {
			v.Unpriced++
		}
		v.Lines = append(v.Lines, lv)
	}
	slices.SortFunc(v.Lines, func(a, b LineView) int {
		return cmp.Or(
			cmp.Compare(noSupplier(a), noSupplier(b)), // no supplier last
			cmp.Compare(a.SupplierName, b.SupplierName),
			cmp.Compare(a.IngredientName, b.IngredientName),
		)
	})
	return v, nil
}

func noSupplier(l LineView) int {
	if l.SupplierName == "" {
		return 1
	}
	return 0
}
