package connect

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"

	ordersv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/orders/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/orders/v1/ordersv1connect"
	"github.com/Zefanrakh/kue-preorder/internal/orders"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/rpcerr"
)

// AdminHandler serves kuepreorder.orders.v1.OrderAdminService.
type AdminHandler struct {
	admin  *orders.Admin
	logger *slog.Logger
}

var _ ordersv1connect.OrderAdminServiceHandler = (*AdminHandler)(nil)

// NewAdminHandler returns a handler backed by admin.
func NewAdminHandler(admin *orders.Admin, logger *slog.Logger) *AdminHandler {
	return &AdminHandler{admin: admin, logger: logger}
}

// ListOrders implements ordersv1connect.OrderAdminServiceHandler.
func (h *AdminHandler) ListOrders(ctx context.Context, req *connect.Request[ordersv1.ListOrdersRequest]) (*connect.Response[ordersv1.ListOrdersResponse], error) {
	m := req.Msg
	bad := apperr.Fields{}
	from, err := clock.ParseDate(m.GetFromDate())
	bad.Check(err == nil, "from_date", "Tanggal tidak valid, tulis seperti 2026-10-05.")
	to, err := clock.ParseDate(m.GetToDate())
	bad.Check(err == nil, "to_date", "Tanggal tidak valid, tulis seperti 2026-10-05.")
	f := orders.StaffFilter{From: from, To: to, Query: m.GetQuery()}
	for _, s := range m.GetStatuses() {
		status := statusFromProto(s)
		bad.Check(status != "", "statuses", "Status tidak dikenal.")
		f.Statuses = append(f.Statuses, status)
	}
	if err := bad.Err(); err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	list, err := h.admin.List(ctx, f)
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	out := make([]*ordersv1.StaffOrderSummary, len(list))
	for i, s := range list {
		out[i] = &ordersv1.StaffOrderSummary{
			Summary: summaryToProto(s.Summary), ProductionDate: s.ProductionDate.String(),
			CustomerName: s.CustomerName, CustomerPhone: s.CustomerPhone,
		}
	}
	return connect.NewResponse(&ordersv1.ListOrdersResponse{Orders: out}), nil
}

// GetOrder implements ordersv1connect.OrderAdminServiceHandler.
func (h *AdminHandler) GetOrder(ctx context.Context, req *connect.Request[ordersv1.GetOrderRequest]) (*connect.Response[ordersv1.GetOrderResponse], error) {
	o, err := h.admin.Get(ctx, req.Msg.GetCode())
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	return connect.NewResponse(&ordersv1.GetOrderResponse{Order: staffOrderToProto(o)}), nil
}

// AdvanceOrder implements ordersv1connect.OrderAdminServiceHandler.
func (h *AdminHandler) AdvanceOrder(ctx context.Context, req *connect.Request[ordersv1.AdvanceOrderRequest]) (*connect.Response[ordersv1.AdvanceOrderResponse], error) {
	o, err := h.admin.Advance(ctx, req.Msg.GetCode(), statusFromProto(req.Msg.GetStatus()))
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	return connect.NewResponse(&ordersv1.AdvanceOrderResponse{Order: staffOrderToProto(o)}), nil
}

// RecordManualPayment implements ordersv1connect.OrderAdminServiceHandler.
func (h *AdminHandler) RecordManualPayment(ctx context.Context, req *connect.Request[ordersv1.RecordManualPaymentRequest]) (*connect.Response[ordersv1.RecordManualPaymentResponse], error) {
	m := req.Msg
	o, err := h.admin.RecordPayment(ctx, m.GetCode(), orders.ManualPayment{
		AmountIDR: m.GetAmountIdr(), Reference: m.GetReference(), Note: m.GetNote(),
	})
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	return connect.NewResponse(&ordersv1.RecordManualPaymentResponse{Order: staffOrderToProto(o)}), nil
}

// CancelOrder implements ordersv1connect.OrderAdminServiceHandler.
func (h *AdminHandler) CancelOrder(ctx context.Context, req *connect.Request[ordersv1.CancelOrderRequest]) (*connect.Response[ordersv1.CancelOrderResponse], error) {
	m := req.Msg
	o, err := h.admin.Cancel(ctx, m.GetCode(), orders.CancelRequest{
		Mode: cancelModes[m.GetMode()], Reason: m.GetReason(), RefundReference: m.GetRefundReference(),
	})
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	return connect.NewResponse(&ordersv1.CancelOrderResponse{Order: staffOrderToProto(o)}), nil
}

// cancelModes maps an unspecified mode to "", which CancelOrder refuses in
// its own words.
var cancelModes = map[ordersv1.CancelMode]orders.CancelMode{
	ordersv1.CancelMode_CANCEL_MODE_UNPAID:  orders.CancelUnpaid,
	ordersv1.CancelMode_CANCEL_MODE_FORFEIT: orders.CancelForfeit,
	ordersv1.CancelMode_CANCEL_MODE_REFUND:  orders.CancelRefund,
}

// statusFromProto maps an unspecified or unknown status to "".
func statusFromProto(s ordersv1.OrderStatus) orders.Status {
	for d, p := range orderStatuses {
		if p == s {
			return d
		}
	}
	return ""
}

// staffOrderToProto is orderToProto with what only staff see: the proof of
// the manual payments.
func staffOrderToProto(o orders.Order) *ordersv1.Order {
	out := orderToProto(o)
	for i, p := range o.Payments {
		if p.Manual != nil {
			out.Payments[i].Manual = &ordersv1.ManualProof{
				Reference: p.Manual.Reference, Note: p.Manual.Note, RecordedBy: p.Manual.RecordedBy.String(),
			}
		}
	}
	return out
}
