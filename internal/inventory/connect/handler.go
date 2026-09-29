// Package connect serves the inventory module's Connect RPCs.
package connect

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	catalogv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/catalog/v1"
	inventoryv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/inventory/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/inventory/v1/inventoryv1connect"
	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/inventory"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/rpcerr"
)

// Handler serves kuepreorder.inventory.v1.InventoryService.
type Handler struct {
	svc    *inventory.Service
	logger *slog.Logger
}

var _ inventoryv1connect.InventoryServiceHandler = (*Handler)(nil)

// NewHandler returns a handler backed by svc.
func NewHandler(svc *inventory.Service, logger *slog.Logger) *Handler {
	return &Handler{svc: svc, logger: logger}
}

// ReceiveStock implements inventoryv1connect.InventoryServiceHandler.
func (h *Handler) ReceiveStock(ctx context.Context, req *connect.Request[inventoryv1.ReceiveStockRequest]) (*connect.Response[inventoryv1.ReceiveStockResponse], error) {
	m := req.Msg
	var ids rpcerr.IDs
	in := inventory.ReceiveInput{IngredientID: ids.Parse("ingredient_id", m.GetIngredientId()), Qty: m.GetQty(), Note: m.GetNote()}
	if err := ids.Err(); err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	if m.GetReceivedAt() != nil {
		in.ReceivedAt = m.GetReceivedAt().AsTime()
	}
	if m.GetExpiresAt() != nil {
		t := m.GetExpiresAt().AsTime()
		in.ExpiresAt = &t
	}
	l, err := h.svc.Receive(ctx, in)
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	return connect.NewResponse(&inventoryv1.ReceiveStockResponse{Lot: lotToProto(l)}), nil
}

// ListStock implements inventoryv1connect.InventoryServiceHandler.
func (h *Handler) ListStock(ctx context.Context, _ *connect.Request[inventoryv1.ListStockRequest]) (*connect.Response[inventoryv1.ListStockResponse], error) {
	stock, err := h.svc.Stock(ctx)
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	out := make([]*inventoryv1.IngredientStock, len(stock))
	for i, st := range stock {
		out[i] = stockToProto(st)
	}
	return connect.NewResponse(&inventoryv1.ListStockResponse{Ingredients: out}), nil
}

// ListLotsToCheck implements inventoryv1connect.InventoryServiceHandler.
func (h *Handler) ListLotsToCheck(ctx context.Context, _ *connect.Request[inventoryv1.ListLotsToCheckRequest]) (*connect.Response[inventoryv1.ListLotsToCheckResponse], error) {
	lots, err := h.svc.LotsToCheck(ctx)
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	out := make([]*inventoryv1.LotToCheck, len(lots))
	for i, l := range lots {
		out[i] = &inventoryv1.LotToCheck{Lot: lotToProto(l.Lot), IngredientName: l.IngredientName, BaseUnit: baseUnits[l.BaseUnit], Expired: l.Expired}
	}
	return connect.NewResponse(&inventoryv1.ListLotsToCheckResponse{Lots: out}), nil
}

// CheckLot implements inventoryv1connect.InventoryServiceHandler.
func (h *Handler) CheckLot(ctx context.Context, req *connect.Request[inventoryv1.CheckLotRequest]) (*connect.Response[inventoryv1.CheckLotResponse], error) {
	m := req.Msg
	var ids rpcerr.IDs
	lotID := ids.Parse("lot_id", m.GetLotId())
	if err := ids.Err(); err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	if m.GetResult() == inventoryv1.CheckResult_CHECK_RESULT_UNSPECIFIED {
		return nil, rpcerr.Wrap(ctx, h.logger, &apperr.ValidationError{Fields: map[string]string{"result": "Pilih Masih bagus atau Buang."}}, nil)
	}
	in := inventory.CheckInput{OK: m.GetResult() == inventoryv1.CheckResult_CHECK_RESULT_OK, Reason: discardReasons[m.GetReason()], Note: m.GetNote()}
	l, err := h.svc.Check(ctx, lotID, in)
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	return connect.NewResponse(&inventoryv1.CheckLotResponse{Lot: lotToProto(l)}), nil
}

// CountStock implements inventoryv1connect.InventoryServiceHandler.
func (h *Handler) CountStock(ctx context.Context, req *connect.Request[inventoryv1.CountStockRequest]) (*connect.Response[inventoryv1.CountStockResponse], error) {
	m := req.Msg
	var ids rpcerr.IDs
	ingredientID := ids.Parse("ingredient_id", m.GetIngredientId())
	if err := ids.Err(); err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	st, err := h.svc.Count(ctx, ingredientID, m.GetActualQty(), m.GetReason())
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	return connect.NewResponse(&inventoryv1.CountStockResponse{Ingredient: stockToProto(st)}), nil
}

// discardReasons maps an unspecified reason to "", which CheckLot refuses in
// its own words.
var discardReasons = map[inventoryv1.DiscardReason]inventory.DiscardReason{
	inventoryv1.DiscardReason_DISCARD_REASON_SMELL:   inventory.DiscardSmell,
	inventoryv1.DiscardReason_DISCARD_REASON_MOLD:    inventory.DiscardMold,
	inventoryv1.DiscardReason_DISCARD_REASON_EXPIRED: inventory.DiscardExpired,
	inventoryv1.DiscardReason_DISCARD_REASON_OTHER:   inventory.DiscardOther,
}

var lotStatuses = map[inventory.LotStatus]inventoryv1.LotStatus{
	inventory.LotAvailable: inventoryv1.LotStatus_LOT_STATUS_AVAILABLE,
	inventory.LotExhausted: inventoryv1.LotStatus_LOT_STATUS_EXHAUSTED,
	inventory.LotDiscarded: inventoryv1.LotStatus_LOT_STATUS_DISCARDED,
	inventory.LotExpired:   inventoryv1.LotStatus_LOT_STATUS_EXPIRED,
}

var lotSources = map[inventory.Source]inventoryv1.LotSource{
	inventory.SourceManual:      inventoryv1.LotSource_LOT_SOURCE_MANUAL,
	inventory.SourceProcurement: inventoryv1.LotSource_LOT_SOURCE_PROCUREMENT,
	inventory.SourceAdjustment:  inventoryv1.LotSource_LOT_SOURCE_ADJUSTMENT,
}

var baseUnits = map[catalog.BaseUnit]catalogv1.BaseUnit{
	catalog.Gram:       catalogv1.BaseUnit_BASE_UNIT_GRAM,
	catalog.Millilitre: catalogv1.BaseUnit_BASE_UNIT_MILLILITRE,
	catalog.Piece:      catalogv1.BaseUnit_BASE_UNIT_PIECE,
}

var leftoverPolicies = map[catalog.LeftoverPolicy]catalogv1.LeftoverPolicy{
	catalog.LeftoverAuto:    catalogv1.LeftoverPolicy_LEFTOVER_POLICY_AUTO,
	catalog.LeftoverConfirm: catalogv1.LeftoverPolicy_LEFTOVER_POLICY_CONFIRM,
	catalog.LeftoverNever:   catalogv1.LeftoverPolicy_LEFTOVER_POLICY_NEVER,
}

func lotToProto(l inventory.Lot) *inventoryv1.Lot {
	out := &inventoryv1.Lot{
		Id: l.ID.String(), IngredientId: l.IngredientID.String(), ReceivedAt: timestamppb.New(l.ReceivedAt),
		Status: lotStatuses[l.Status], Source: lotSources[l.Source], Note: l.Note, Balance: l.Balance,
	}
	if l.ExpiresAt != nil {
		out.ExpiresAt = timestamppb.New(*l.ExpiresAt)
	}
	if l.LastOKAt != nil {
		out.LastOkAt = timestamppb.New(*l.LastOKAt)
	}
	return out
}

func stockToProto(st inventory.IngredientStock) *inventoryv1.IngredientStock {
	out := &inventoryv1.IngredientStock{
		IngredientId: st.IngredientID.String(), Name: st.Name, BaseUnit: baseUnits[st.BaseUnit],
		LeftoverPolicy: leftoverPolicies[st.Policy], Total: st.Total, UsableToday: st.UsableToday,
	}
	for _, l := range st.Lots {
		out.Lots = append(out.Lots, lotToProto(l))
	}
	return out
}
