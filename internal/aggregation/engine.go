package aggregation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/orders"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
)

// Orders is what aggregation reads from orders; orders.Reader implements it.
type Orders interface {
	CommittedItems(ctx context.Context, tenantID uuid.UUID, date clock.Date) ([]orders.CommittedItem, error)
}

// Catalog is what aggregation reads from the catalog; catalog.Reader
// implements it.
type Catalog interface {
	ComponentUses(ctx context.Context, tenantID uuid.UUID, variantIDs []uuid.UUID) ([]catalog.ComponentUse, error)
	RecipeModels(ctx context.Context, tenantID uuid.UUID, componentIDs []uuid.UUID) ([]catalog.RecipeModel, error)
	DefaultPacks(ctx context.Context, tenantID uuid.UUID, ingredientIDs []uuid.UUID) ([]catalog.DefaultPack, error)
	Labels(ctx context.Context, tenantID uuid.UUID) (catalog.Labels, error)
}

// Stock tells how much of each ingredient a batch may count on (§12);
// inventory implements it from M3.2.
type Stock interface {
	Usable(ctx context.Context, tenantID uuid.UUID, date clock.Date, ingredientIDs []uuid.UUID) (map[uuid.UUID]int64, error)
}

// NoStock counts no stock at all: everything is bought. It stands in until
// inventory arrives (M3.2), and is the safe answer anyway (§12: when in
// doubt, do not count it).
type NoStock struct{}

// Usable implements Stock.
func (NoStock) Usable(context.Context, uuid.UUID, clock.Date, []uuid.UUID) (map[uuid.UUID]int64, error) {
	return map[uuid.UUID]int64{}, nil
}

// Transactor runs a unit of work in one transaction; *db.DB implements it.
type Transactor interface {
	Tx(ctx context.Context, fn func(ctx context.Context) error) error
}

// BatchStatus is where a batch is in production.
type BatchStatus string

// Batch statuses (§9.7). M3.1 only opens batches; M3.3 locks and closes them.
const (
	BatchOpen         BatchStatus = "open"
	BatchLocked       BatchStatus = "locked"
	BatchInProduction BatchStatus = "in_production"
	BatchDone         BatchStatus = "done"
)

// Batch is the production of one date.
type Batch struct {
	ID     uuid.UUID
	Date   clock.Date
	Status BatchStatus
	// ComputedAt is when the last computation succeeded; nil before one has.
	ComputedAt *time.Time
	// Error says why the last computation failed, for the CMS; empty once
	// one succeeds.
	Error     string
	UpdatedAt time.Time
}

// Summary is a batch as the list of batches shows it.
type Summary struct {
	Batch
	LinesToBuy int32
	// CostIDR sums the lines whose pack has a price.
	CostIDR int64
}

// Repository is the persistence port of aggregation. It works in the
// caller's transaction.
type Repository interface {
	// EnsureBatch creates the batch of date unless it exists.
	EnsureBatch(ctx context.Context, tenantID uuid.UUID, date clock.Date, at time.Time) error
	// LockBatch returns the batch of date and holds its row until the
	// transaction ends; GetBatch reads it without a lock. Both return
	// apperr.ErrNotFound.
	LockBatch(ctx context.Context, tenantID uuid.UUID, date clock.Date) (Batch, error)
	GetBatch(ctx context.Context, tenantID uuid.UUID, date clock.Date) (Batch, error)
	ListBatches(ctx context.Context, tenantID uuid.UUID, from, to clock.Date) ([]Summary, error)
	// UpcomingDates returns the dates from on whose batch is not done.
	UpcomingDates(ctx context.Context, tenantID uuid.UUID, from clock.Date) ([]clock.Date, error)
	Components(ctx context.Context, tenantID, batchID uuid.UUID) ([]ComponentTotal, error)
	Lines(ctx context.Context, tenantID, batchID uuid.UUID) ([]Line, error)
	// SaveResult replaces the batch's component totals, writes its lines
	// without touching what procurement set, drops the lines no longer
	// needed that nobody ordered, and marks the batch computed.
	SaveResult(ctx context.Context, tenantID, batchID uuid.UUID, components []ComponentTotal, lines []Line, at time.Time) error
	// MarkFailed records why the batch of date could not be computed,
	// creating it when needed.
	MarkFailed(ctx context.Context, tenantID uuid.UUID, date clock.Date, reason string, at time.Time) error
}

// EngineDeps are what Engine works with.
type EngineDeps struct {
	Repo    Repository
	Orders  Orders
	Catalog Catalog
	Stock   Stock
	Tx      Transactor
	Clock   clock.Clock
	Logger  *slog.Logger
}

// Engine computes batches (§11). A computation of a date runs in one
// transaction with the batch's row locked, reads everything again, and
// writes the whole result, so it is idempotent: running it twice, or twice
// at once, leaves the same shopping list.
type Engine struct {
	repo    Repository
	orders  Orders
	catalog Catalog
	stock   Stock
	tx      Transactor
	clock   clock.Clock
	logger  *slog.Logger
}

// NewEngine returns an Engine over d.
func NewEngine(d EngineDeps) *Engine {
	return &Engine{repo: d.Repo, orders: d.Orders, catalog: d.Catalog, stock: d.Stock, tx: d.Tx, clock: d.Clock, logger: d.Logger}
}

// Recompute computes the batch of date again. When a recipe cannot be
// evaluated, the batch keeps its last shopping list, is marked failed with a
// reason naming the component and the ingredient, and the returned error
// wraps a *RecipeError; nothing is guessed (§11).
func (e *Engine) Recompute(ctx context.Context, tenant uuid.UUID, date clock.Date) error {
	now := e.clock.Now()
	err := e.tx.Tx(ctx, func(ctx context.Context) error {
		if err := e.repo.EnsureBatch(ctx, tenant, date, now); err != nil {
			return err
		}
		b, err := e.repo.LockBatch(ctx, tenant, date)
		if err != nil {
			return err
		}
		components, needs, err := e.totals(ctx, tenant, date)
		if err != nil {
			return err
		}
		existing, err := e.repo.Lines(ctx, tenant, b.ID)
		if err != nil {
			return err
		}
		ids := ingredientIDs(needs, existing)
		stock, err := e.stock.Usable(ctx, tenant, date, ids)
		if err != nil {
			return err
		}
		packs, err := e.catalog.DefaultPacks(ctx, tenant, ids)
		if err != nil {
			return err
		}
		byIngredient := make(map[uuid.UUID]catalog.DefaultPack, len(packs))
		for _, p := range packs {
			byIngredient[p.IngredientID] = p
		}
		return e.repo.SaveResult(ctx, tenant, b.ID, components, Shop(needs, stock, existing, byIngredient), now)
	})
	if err == nil {
		return nil
	}
	var re *RecipeError
	if errors.As(err, &re) {
		reason := e.describe(ctx, tenant, re)
		if mErr := e.repo.MarkFailed(ctx, tenant, date, reason, now); mErr != nil {
			err = errors.Join(err, fmt.Errorf("mark batch failed: %w", mErr))
		}
	}
	return fmt.Errorf("recompute batch %s: %w", date, err)
}

// totals reads the batch's items and recipes and computes levels 1 and 2.
func (e *Engine) totals(ctx context.Context, tenant uuid.UUID, date clock.Date) ([]ComponentTotal, []Need, error) {
	items, err := e.orders.CommittedItems(ctx, tenant, date)
	if err != nil || len(items) == 0 {
		return nil, nil, err
	}
	variants := make([]uuid.UUID, len(items))
	for i, it := range items {
		variants[i] = it.VariantID
	}
	uses, err := e.catalog.ComponentUses(ctx, tenant, variants)
	if err != nil || len(uses) == 0 {
		return nil, nil, err
	}
	var components []uuid.UUID
	for _, u := range uses {
		components = append(components, u.ComponentID)
	}
	models, err := e.catalog.RecipeModels(ctx, tenant, components)
	var broken *catalog.BrokenRecipeError
	if errors.As(err, &broken) {
		return nil, nil, &RecipeError{ComponentID: broken.ComponentID, IngredientID: broken.IngredientID, Err: err}
	}
	if err != nil {
		return nil, nil, err
	}
	return Totals(items, uses, models)
}

// describe says in the CMS's words which recipe failed.
func (e *Engine) describe(ctx context.Context, tenant uuid.UUID, re *RecipeError) string {
	component, ingredient := re.ComponentID.String(), re.IngredientID.String()
	if labels, err := e.catalog.Labels(ctx, tenant); err == nil {
		if c, ok := labels.Components[re.ComponentID]; ok {
			component = c.Name
		}
		if i, ok := labels.Ingredients[re.IngredientID]; ok {
			ingredient = i.Name
		}
	}
	return fmt.Sprintf("Resep %s untuk bahan %s tidak bisa dihitung. Periksa resepnya di katalog.", component, ingredient)
}

// RecomputeUpcoming computes again every batch from today on that is not
// done, after a recipe changed. It returns how many it computed and every
// failure; one failing batch does not stop the others.
func (e *Engine) RecomputeUpcoming(ctx context.Context, tenant uuid.UUID) (int, error) {
	dates, err := e.repo.UpcomingDates(ctx, tenant, clock.DateOf(e.clock.Now()))
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, d := range dates {
		if err := e.Recompute(ctx, tenant, d); err != nil {
			errs = append(errs, err)
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// ErrBrokenRecipe refuses a manual recomputation that hit a broken recipe;
// the batch shows the reason.
var ErrBrokenRecipe = &apperr.PreconditionError{
	Reason:  "broken_recipe",
	Message: "Ada resep yang tidak bisa dihitung. Periksa resepnya di katalog, lalu hitung ulang.",
}

func ingredientIDs(needs []Need, lines []Line) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	var ids []uuid.UUID
	add := func(id uuid.UUID) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, n := range needs {
		add(n.IngredientID)
	}
	for _, l := range lines {
		add(l.IngredientID)
	}
	return ids
}
