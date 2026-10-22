// Package connect serves the procurement module's Connect RPCs.
package connect

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	catalogv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/catalog/v1"
	procurementv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/procurement/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/procurement/v1/procurementv1connect"
	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/rpcerr"
	"github.com/Zefanrakh/kue-preorder/internal/procurement"
)

// Handler serves kuepreorder.procurement.v1.ProcurementService.
type Handler struct {
	svc    *procurement.Service
	logger *slog.Logger
}

var _ procurementv1connect.ProcurementServiceHandler = (*Handler)(nil)

// NewHandler returns a handler backed by svc.
func NewHandler(svc *procurement.Service, logger *slog.Logger) *Handler {
	return &Handler{svc: svc, logger: logger}
}

// CreateOrder implements procurementv1connect.ProcurementServiceHandler.
func (h *Handler) CreateOrder(ctx context.Context, req *connect.Request[procurementv1.CreateOrderRequest]) (*connect.Response[procurementv1.CreateOrderResponse], error) {
	m := req.Msg
	bad := apperr.Fields{}
	d, err := clock.ParseDate(m.GetDate())
	bad.Check(err == nil, "date", "Tanggal tidak valid, tulis seperti 2026-10-05.")
	var supplier *uuid.UUID
	if m.GetSupplierId() != "" {
		id, err := uuid.Parse(m.GetSupplierId())
		bad.Check(err == nil, "supplier_id", "Supplier tidak dikenal.")
		supplier = &id
	}
	if err := bad.Err(); err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	v, err := h.svc.Create(ctx, d, supplier, m.GetNote())
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	return connect.NewResponse(&procurementv1.CreateOrderResponse{Order: orderToProto(v)}), nil
}

// ListOrders implements procurementv1connect.ProcurementServiceHandler.
func (h *Handler) ListOrders(ctx context.Context, req *connect.Request[procurementv1.ListOrdersRequest]) (*connect.Response[procurementv1.ListOrdersResponse], error) {
	d, err := clock.ParseDate(req.Msg.GetDate())
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, &apperr.ValidationError{Fields: map[string]string{"date": "Tanggal tidak valid, tulis seperti 2026-10-05."}}, nil)
	}
	list, err := h.svc.List(ctx, d)
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	out := make([]*procurementv1.PurchaseOrder, len(list))
	for i, v := range list {
		out[i] = orderToProto(v)
	}
	return connect.NewResponse(&procurementv1.ListOrdersResponse{Orders: out}), nil
}

// GetOrder implements procurementv1connect.ProcurementServiceHandler.
func (h *Handler) GetOrder(ctx context.Context, req *connect.Request[procurementv1.GetOrderRequest]) (*connect.Response[procurementv1.GetOrderResponse], error) {
	var ids rpcerr.IDs
	id := ids.Parse("order_id", req.Msg.GetOrderId())
	if err := ids.Err(); err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	v, err := h.svc.Get(ctx, id)
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	return connect.NewResponse(&procurementv1.GetOrderResponse{Order: orderToProto(v)}), nil
}

// ReceiveOrder implements procurementv1connect.ProcurementServiceHandler.
func (h *Handler) ReceiveOrder(ctx context.Context, req *connect.Request[procurementv1.ReceiveOrderRequest]) (*connect.Response[procurementv1.ReceiveOrderResponse], error) {
	m := req.Msg
	var ids rpcerr.IDs
	id := ids.Parse("order_id", m.GetOrderId())
	in := procurement.ReceiveInput{}
	if m.GetReceivedAt() != nil {
		in.ReceivedAt = m.GetReceivedAt().AsTime()
	}
	for _, it := range m.GetItems() {
		r := procurement.Received{ItemID: ids.Parse("items", it.GetItemId()), Qty: it.GetQty()}
		if it.GetExpiresAt() != nil {
			t := it.GetExpiresAt().AsTime()
			r.ExpiresAt = &t
		}
		in.Items = append(in.Items, r)
	}
	if err := ids.Err(); err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	v, err := h.svc.Receive(ctx, id, in)
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	return connect.NewResponse(&procurementv1.ReceiveOrderResponse{Order: orderToProto(v)}), nil
}

// CancelOrder implements procurementv1connect.ProcurementServiceHandler.
func (h *Handler) CancelOrder(ctx context.Context, req *connect.Request[procurementv1.CancelOrderRequest]) (*connect.Response[procurementv1.CancelOrderResponse], error) {
	var ids rpcerr.IDs
	id := ids.Parse("order_id", req.Msg.GetOrderId())
	if err := ids.Err(); err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	v, err := h.svc.Cancel(ctx, id, req.Msg.GetReason())
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	return connect.NewResponse(&procurementv1.CancelOrderResponse{Order: orderToProto(v)}), nil
}

var orderStatuses = map[procurement.OrderStatus]procurementv1.OrderStatus{
	procurement.OrderOrdered:   procurementv1.OrderStatus_ORDER_STATUS_ORDERED,
	procurement.OrderReceived:  procurementv1.OrderStatus_ORDER_STATUS_RECEIVED,
	procurement.OrderCancelled: procurementv1.OrderStatus_ORDER_STATUS_CANCELLED,
}

var itemStatuses = map[procurement.ItemStatus]procurementv1.ItemStatus{
	procurement.ItemOrdered:   procurementv1.ItemStatus_ITEM_STATUS_ORDERED,
	procurement.ItemReceived:  procurementv1.ItemStatus_ITEM_STATUS_RECEIVED,
	procurement.ItemCancelled: procurementv1.ItemStatus_ITEM_STATUS_CANCELLED,
}

var baseUnits = map[catalog.BaseUnit]catalogv1.BaseUnit{
	catalog.Gram:       catalogv1.BaseUnit_BASE_UNIT_GRAM,
	catalog.Millilitre: catalogv1.BaseUnit_BASE_UNIT_MILLILITRE,
	catalog.Piece:      catalogv1.BaseUnit_BASE_UNIT_PIECE,
}

func orderToProto(v procurement.View) *procurementv1.PurchaseOrder {
	out := &procurementv1.PurchaseOrder{
		Id: v.ID.String(), Date: v.BatchDate.String(), SupplierName: v.SupplierName, Adapter: v.Adapter,
		Status: orderStatuses[v.Status], Note: v.Note, CreatedAt: timestamppb.New(v.CreatedAt), CostIdr: v.CostIDR,
	}
	if v.SupplierID != nil {
		out.SupplierId = v.SupplierID.String()
	}
	for _, it := range v.Items {
		item := &procurementv1.PurchaseItem{
			Id: it.ID.String(), IngredientId: it.IngredientID.String(), IngredientName: it.IngredientName,
			BaseUnit: baseUnits[it.BaseUnit], Qty: it.Qty, Status: itemStatuses[it.Status], QtyReceived: it.QtyReceived,
		}
		if it.Pack != nil {
			item.Pack = &procurementv1.Pack{Count: it.Pack.Count, Size: it.Pack.Size, Unit: it.Pack.Unit, PriceIdr: it.Pack.PriceIDR}
		}
		if it.ReceivedAt != nil {
			item.ReceivedAt = timestamppb.New(*it.ReceivedAt)
		}
		out.Items = append(out.Items, item)
	}
	return out
}
