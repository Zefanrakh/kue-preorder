//go:build integration

package main

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	aggregationv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/aggregation/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/aggregation/v1/aggregationv1connect"
	catalogv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/catalog/v1"
	ordersv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/orders/v1"
	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	catalogpg "github.com/Zefanrakh/kue-preorder/internal/catalog/postgres"
	"github.com/Zefanrakh/kue-preorder/internal/platform/db/dbtest"
	"github.com/Zefanrakh/kue-preorder/internal/recipe"
)

// withRecipe gives the donut one portion of dough each, the dough 50 g of
// flour per portion, and flour a default pack of 1 kg at Rp14.000. It
// returns the dough and the flour.
func (w *wired) withRecipe(t *testing.T) (dough, flour uuid.UUID) {
	t.Helper()
	ctx := t.Context()
	tenant := dbtest.DefaultTenantID
	repo := catalogpg.NewRepository(w.d)
	at := wib(5, 9, 0)
	c, err := repo.CreateComponent(ctx, tenant, catalog.ComponentInput{Name: "Adonan donut", UnitLabel: "porsi"}, at)
	noErr(t, err)
	f, err := repo.CreateIngredient(ctx, tenant, catalog.IngredientInput{Name: "Tepung", BaseUnit: catalog.Gram, LeftoverPolicy: catalog.LeftoverAuto}, at)
	noErr(t, err)
	_, err = repo.SetVariantComponents(ctx, tenant, w.donut.ID, []catalog.VariantComponent{{ComponentID: c.ID, UnitsPerItem: 1}}, at)
	noErr(t, err)
	perPortion, err := recipe.NewAffine(0, 50)
	noErr(t, err)
	params, err := recipe.Params(perPortion)
	noErr(t, err)
	_, err = repo.SetRecipeLine(ctx, tenant, catalog.RecipeLineWrite{ComponentID: c.ID, IngredientID: f.ID, ModelType: recipe.Affine, Params: params, WasteFactor: 1}, at)
	noErr(t, err)
	s, err := repo.CreateSupplier(ctx, tenant, catalog.SupplierInput{Name: "Toko Sinar", Adapter: catalog.AdapterManual}, at)
	noErr(t, err)
	kilo := int64(14000)
	p, err := repo.CreatePack(ctx, tenant, f.ID, catalog.PackInput{SupplierID: s.ID, Size: 1000, Unit: "kg", PriceIDR: &kilo}, at)
	noErr(t, err)
	_, err = repo.SetDefaultPack(ctx, tenant, p.ID, at)
	noErr(t, err)
	return c.ID, f.ID
}

func (w *wired) batches(t *testing.T, role string) aggregationv1connect.BatchServiceClient {
	t.Helper()
	_, user := w.staff(t, role)
	return aggregationv1connect.NewBatchServiceClient(w.http, w.url, w.bearer(t, user, ""))
}

// A confirmed order's ingredients reach the kitchen's shopping list, with
// packs and cost; nobody but staff sees it.
func TestBatches_ShoppingList(t *testing.T) {
	w := newWired(t)
	ctx := t.Context()
	w.withRecipe(t)
	placed, _ := w.placed(t, 8) // 20 donuts for Thursday
	owner, _ := w.staff(t, "owner")
	_, err := owner.RecordManualPayment(ctx, connect.NewRequest(&ordersv1.RecordManualPaymentRequest{
		Code: placed.GetCode(), AmountIdr: 80000, Reference: "BCA 1", Note: "DP",
	}))
	noErr(t, err)
	kitchen := w.batches(t, "kitchen")

	// The worker computes it on order.confirmed; the kitchen may as well.
	res, err := kitchen.RecomputeBatch(ctx, connect.NewRequest(&aggregationv1.RecomputeBatchRequest{Date: "2026-10-08"}))

	noErr(t, err)
	b := res.Msg.GetBatch()
	if b.GetBatch().GetStatus() != aggregationv1.BatchStatus_BATCH_STATUS_OPEN || b.GetBatch().GetComputedAt() == nil || b.GetBatch().GetError() != "" {
		t.Errorf("batch = %v", b.GetBatch())
	}
	if cs := b.GetComponents(); len(cs) != 1 || cs[0].GetName() != "Adonan donut" || cs[0].GetUnits() != 20 || cs[0].GetUnitLabel() != "porsi" {
		t.Errorf("components = %v, want 20 portions of dough", cs)
	}
	if ls := b.GetLines(); len(ls) != 1 || ls[0].GetIngredientName() != "Tepung" || ls[0].GetBaseUnit() != catalogv1.BaseUnit_BASE_UNIT_GRAM ||
		ls[0].GetNeeded() != 1000 || ls[0].GetToBuy() != 1000 || ls[0].GetPacks() != 1 || ls[0].GetPack().GetSupplierName() != "Toko Sinar" ||
		ls[0].GetCostIdr() != 14000 || ls[0].GetStatus() != aggregationv1.LineStatus_LINE_STATUS_NEEDED {
		t.Errorf("lines = %v, want 1 kg of flour from Toko Sinar at Rp14.000", ls)
	}
	if b.GetCostIdr() != 14000 || b.GetUnpriced() != 0 {
		t.Errorf("cost = %d with %d unpriced, want 14,000", b.GetCostIdr(), b.GetUnpriced())
	}

	list, err := kitchen.ListBatches(ctx, connect.NewRequest(&aggregationv1.ListBatchesRequest{FromDate: "2026-10-01", ToDate: "2026-10-31"}))
	noErr(t, err)
	if bs := list.Msg.GetBatches(); len(bs) != 1 || bs[0].GetBatch().GetDate() != "2026-10-08" || bs[0].GetLinesToBuy() != 1 || bs[0].GetCostIdr() != 14000 {
		t.Errorf("ListBatches() = %v", bs)
	}
	if _, err := kitchen.GetBatch(ctx, connect.NewRequest(&aggregationv1.GetBatchRequest{Date: "2026-10-09"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("GetBatch(a day without orders) code = %v, want NotFound", connect.CodeOf(err))
	}
	if _, err := kitchen.GetBatch(ctx, connect.NewRequest(&aggregationv1.GetBatchRequest{Date: "8 Oktober"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("GetBatch(bad date) code = %v, want InvalidArgument", connect.CodeOf(err))
	}
	customer := aggregationv1connect.NewBatchServiceClient(w.http, w.url, w.bearer(t, uuid.New(), "6281234567890"))
	if _, err := customer.GetBatch(ctx, connect.NewRequest(&aggregationv1.GetBatchRequest{Date: "2026-10-08"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("customer GetBatch() code = %v, want PermissionDenied", connect.CodeOf(err))
	}
}

// A recipe that cannot be evaluated is refused and named on the batch; the
// last good list stays.
func TestBatches_BrokenRecipe(t *testing.T) {
	w := newWired(t)
	ctx := t.Context()
	dough, flour := w.withRecipe(t)
	placed, _ := w.placed(t, 8)
	owner, _ := w.staff(t, "owner")
	_, err := owner.RecordManualPayment(ctx, connect.NewRequest(&ordersv1.RecordManualPaymentRequest{
		Code: placed.GetCode(), AmountIdr: 80000, Reference: "BCA 1", Note: "DP",
	}))
	noErr(t, err)
	kitchen := w.batches(t, "kitchen")
	_, err = kitchen.RecomputeBatch(ctx, connect.NewRequest(&aggregationv1.RecomputeBatchRequest{Date: "2026-10-08"}))
	noErr(t, err)
	// Break the stored line behind the engine's back, as a bad migration might.
	_, err = w.d.Pool().Exec(ctx, `update component_ingredients set params = '{"a": -1, "b": 50}' where component_id = $1 and ingredient_id = $2`, dough, flour)
	noErr(t, err)

	_, err = kitchen.RecomputeBatch(ctx, connect.NewRequest(&aggregationv1.RecomputeBatchRequest{Date: "2026-10-08"}))

	if reason := precondition(t, err); reason != "broken_recipe" {
		t.Errorf("reason = %q, want broken_recipe", reason)
	}
	got, err := kitchen.GetBatch(ctx, connect.NewRequest(&aggregationv1.GetBatchRequest{Date: "2026-10-08"}))
	noErr(t, err)
	b := got.Msg.GetBatch()
	if b.GetBatch().GetError() != "Resep Adonan donut untuk bahan Tepung tidak bisa dihitung. Periksa resepnya di katalog." {
		t.Errorf("batch error = %q", b.GetBatch().GetError())
	}
	if ls := b.GetLines(); len(ls) != 1 || ls[0].GetNeeded() != 1000 {
		t.Errorf("lines = %v, want the last good list kept", ls)
	}
}
