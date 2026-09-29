// Package connect serves the aggregation module's Connect RPCs.
package connect

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	aggregationv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/aggregation/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/aggregation/v1/aggregationv1connect"
	catalogv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/catalog/v1"
	"github.com/Zefanrakh/kue-preorder/internal/aggregation"
	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/rpcerr"
)

// Handler serves kuepreorder.aggregation.v1.BatchService.
type Handler struct {
	svc    *aggregation.Service
	logger *slog.Logger
}

var _ aggregationv1connect.BatchServiceHandler = (*Handler)(nil)

// NewHandler returns a handler backed by svc.
func NewHandler(svc *aggregation.Service, logger *slog.Logger) *Handler {
	return &Handler{svc: svc, logger: logger}
}

// ListBatches implements aggregationv1connect.BatchServiceHandler.
func (h *Handler) ListBatches(ctx context.Context, req *connect.Request[aggregationv1.ListBatchesRequest]) (*connect.Response[aggregationv1.ListBatchesResponse], error) {
	bad := apperr.Fields{}
	from := date(bad, "from_date", req.Msg.GetFromDate())
	to := date(bad, "to_date", req.Msg.GetToDate())
	if err := bad.Err(); err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	list, err := h.svc.List(ctx, from, to)
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	out := make([]*aggregationv1.BatchSummary, len(list))
	for i, s := range list {
		out[i] = &aggregationv1.BatchSummary{Batch: batchToProto(s.Batch), LinesToBuy: s.LinesToBuy, CostIdr: s.CostIDR}
	}
	return connect.NewResponse(&aggregationv1.ListBatchesResponse{Batches: out}), nil
}

// GetBatch implements aggregationv1connect.BatchServiceHandler.
func (h *Handler) GetBatch(ctx context.Context, req *connect.Request[aggregationv1.GetBatchRequest]) (*connect.Response[aggregationv1.GetBatchResponse], error) {
	bad := apperr.Fields{}
	d := date(bad, "date", req.Msg.GetDate())
	if err := bad.Err(); err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	v, err := h.svc.Get(ctx, d)
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	return connect.NewResponse(&aggregationv1.GetBatchResponse{Batch: viewToProto(v)}), nil
}

// RecomputeBatch implements aggregationv1connect.BatchServiceHandler.
func (h *Handler) RecomputeBatch(ctx context.Context, req *connect.Request[aggregationv1.RecomputeBatchRequest]) (*connect.Response[aggregationv1.RecomputeBatchResponse], error) {
	bad := apperr.Fields{}
	d := date(bad, "date", req.Msg.GetDate())
	if err := bad.Err(); err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	v, err := h.svc.Recompute(ctx, d)
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	return connect.NewResponse(&aggregationv1.RecomputeBatchResponse{Batch: viewToProto(v)}), nil
}

func date(bad apperr.Fields, field, s string) clock.Date {
	d, err := clock.ParseDate(s)
	bad.Check(err == nil, field, "Tanggal tidak valid, tulis seperti 2026-10-05.")
	return d
}

var batchStatuses = map[aggregation.BatchStatus]aggregationv1.BatchStatus{
	aggregation.BatchOpen:         aggregationv1.BatchStatus_BATCH_STATUS_OPEN,
	aggregation.BatchLocked:       aggregationv1.BatchStatus_BATCH_STATUS_LOCKED,
	aggregation.BatchInProduction: aggregationv1.BatchStatus_BATCH_STATUS_IN_PRODUCTION,
	aggregation.BatchDone:         aggregationv1.BatchStatus_BATCH_STATUS_DONE,
}

var lineStatuses = map[aggregation.LineStatus]aggregationv1.LineStatus{
	aggregation.LineNeeded:   aggregationv1.LineStatus_LINE_STATUS_NEEDED,
	aggregation.LineOrdered:  aggregationv1.LineStatus_LINE_STATUS_ORDERED,
	aggregation.LineReceived: aggregationv1.LineStatus_LINE_STATUS_RECEIVED,
}

var baseUnits = map[catalog.BaseUnit]catalogv1.BaseUnit{
	catalog.Gram:       catalogv1.BaseUnit_BASE_UNIT_GRAM,
	catalog.Millilitre: catalogv1.BaseUnit_BASE_UNIT_MILLILITRE,
	catalog.Piece:      catalogv1.BaseUnit_BASE_UNIT_PIECE,
}

func batchToProto(b aggregation.Batch) *aggregationv1.Batch {
	out := &aggregationv1.Batch{Date: b.Date.String(), Status: batchStatuses[b.Status], Error: b.Error}
	if b.ComputedAt != nil {
		out.ComputedAt = timestamppb.New(*b.ComputedAt)
	}
	return out
}

func viewToProto(v aggregation.View) *aggregationv1.BatchDetail {
	out := &aggregationv1.BatchDetail{Batch: batchToProto(v.Batch), CostIdr: v.CostIDR, Unpriced: v.Unpriced}
	for _, c := range v.Components {
		out.Components = append(out.Components, &aggregationv1.ComponentTotal{
			ComponentId: c.ComponentID.String(), Name: c.Name, UnitLabel: c.UnitLabel, Units: c.Units,
		})
	}
	for _, l := range v.Lines {
		line := &aggregationv1.ShoppingLine{
			IngredientId: l.IngredientID.String(), IngredientName: l.IngredientName, BaseUnit: baseUnits[l.BaseUnit],
			Needed: l.Needed, UsableStock: l.UsableStock, Ordered: l.Ordered, ToBuy: l.ToBuy, Packs: l.Packs,
			Status: lineStatuses[l.Status],
		}
		if l.Pack != nil {
			line.Pack = &aggregationv1.Pack{
				SupplierId: l.Pack.SupplierID.String(), SupplierName: l.SupplierName, Size: l.Pack.Size, Unit: l.Pack.Unit,
				PriceIdr: l.Pack.PriceIDR,
			}
		}
		if cost, ok := l.CostIDR(); ok {
			line.CostIdr = &cost
		}
		out.Lines = append(out.Lines, line)
	}
	return out
}
