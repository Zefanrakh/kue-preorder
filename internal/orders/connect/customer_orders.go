package connect

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	ordersv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/orders/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/orders/v1/ordersv1connect"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
	"github.com/Zefanrakh/kue-preorder/internal/orders"
	"github.com/Zefanrakh/kue-preorder/internal/payments"
	"github.com/Zefanrakh/kue-preorder/internal/platform/rpcerr"
)

// CustomerOrderHandler serves kuepreorder.orders.v1.CustomerOrderService.
type CustomerOrderHandler struct {
	checkout *orders.Checkout
	logger   *slog.Logger
}

var _ ordersv1connect.CustomerOrderServiceHandler = (*CustomerOrderHandler)(nil)

// NewCustomerOrderHandler returns a handler backed by checkout.
func NewCustomerOrderHandler(checkout *orders.Checkout, logger *slog.Logger) *CustomerOrderHandler {
	return &CustomerOrderHandler{checkout: checkout, logger: logger}
}

// PlaceOrder implements ordersv1connect.CustomerOrderServiceHandler.
func (h *CustomerOrderHandler) PlaceOrder(ctx context.Context, req *connect.Request[ordersv1.PlaceOrderRequest]) (*connect.Response[ordersv1.PlaceOrderResponse], error) {
	m := req.Msg
	var ids rpcerr.IDs
	in := orders.PlaceRequest{
		PayInFull: m.GetPayInFull(), TermsVersion: m.GetTermsVersion(), Notes: m.GetNotes(),
		Customer:       identity.CustomerInput{Name: m.GetCustomerName(), Email: m.GetCustomerEmail()},
		IdempotencyKey: ids.Parse("idempotency_key", m.GetIdempotencyKey()),
	}
	if m.GetPickupAt() != nil {
		in.PickupAt = m.GetPickupAt().AsTime()
	}
	for _, it := range m.GetItems() {
		in.Items = append(in.Items, orders.ItemRequest{VariantID: ids.Parse("items", it.GetVariantId()), Quantity: it.GetQuantity()})
	}
	if err := ids.Err(); err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	o, err := h.checkout.Place(ctx, in)
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	return connect.NewResponse(&ordersv1.PlaceOrderResponse{Order: orderToProto(o)}), nil
}

// ListMyOrders implements ordersv1connect.CustomerOrderServiceHandler.
func (h *CustomerOrderHandler) ListMyOrders(ctx context.Context, _ *connect.Request[ordersv1.ListMyOrdersRequest]) (*connect.Response[ordersv1.ListMyOrdersResponse], error) {
	list, err := h.checkout.MyOrders(ctx)
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	out := make([]*ordersv1.OrderSummary, len(list))
	for i, s := range list {
		out[i] = &ordersv1.OrderSummary{
			Id: s.ID.String(), Code: s.Code, Status: orderStatuses[s.Status], PaymentStatus: paymentStatuses[s.Payment],
			PickupAt: timestamppb.New(s.PickupAt), TotalIdr: s.TotalIDR, FirstItem: s.FirstItem, ItemCount: s.ItemCount,
			CreatedAt: timestamppb.New(s.CreatedAt),
		}
	}
	return connect.NewResponse(&ordersv1.ListMyOrdersResponse{Orders: out}), nil
}

// GetMyOrder implements ordersv1connect.CustomerOrderServiceHandler.
func (h *CustomerOrderHandler) GetMyOrder(ctx context.Context, req *connect.Request[ordersv1.GetMyOrderRequest]) (*connect.Response[ordersv1.GetMyOrderResponse], error) {
	o, err := h.checkout.MyOrder(ctx, req.Msg.GetCode())
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	return connect.NewResponse(&ordersv1.GetMyOrderResponse{Order: orderToProto(o)}), nil
}

var orderStatuses = map[orders.Status]ordersv1.OrderStatus{
	orders.AwaitingDP:     ordersv1.OrderStatus_ORDER_STATUS_AWAITING_DP,
	orders.Confirmed:      ordersv1.OrderStatus_ORDER_STATUS_CONFIRMED,
	orders.Expired:        ordersv1.OrderStatus_ORDER_STATUS_EXPIRED,
	orders.InProduction:   ordersv1.OrderStatus_ORDER_STATUS_IN_PRODUCTION,
	orders.Ready:          ordersv1.OrderStatus_ORDER_STATUS_READY,
	orders.OutForDelivery: ordersv1.OrderStatus_ORDER_STATUS_OUT_FOR_DELIVERY,
	orders.Completed:      ordersv1.OrderStatus_ORDER_STATUS_COMPLETED,
	orders.Cancelled:      ordersv1.OrderStatus_ORDER_STATUS_CANCELLED,
}

var paymentStatuses = map[payments.Status]ordersv1.PaymentStatus{
	payments.Unpaid:     ordersv1.PaymentStatus_PAYMENT_STATUS_UNPAID,
	payments.DPPaid:     ordersv1.PaymentStatus_PAYMENT_STATUS_DP_PAID,
	payments.PaidInFull: ordersv1.PaymentStatus_PAYMENT_STATUS_PAID_IN_FULL,
	payments.Forfeited:  ordersv1.PaymentStatus_PAYMENT_STATUS_FORFEITED,
	payments.Refunded:   ordersv1.PaymentStatus_PAYMENT_STATUS_REFUNDED,
}

var paymentKinds = map[payments.Kind]ordersv1.PaymentKind{
	payments.KindDP:      ordersv1.PaymentKind_PAYMENT_KIND_DP,
	payments.KindBalance: ordersv1.PaymentKind_PAYMENT_KIND_BALANCE,
	payments.KindFull:    ordersv1.PaymentKind_PAYMENT_KIND_FULL,
	payments.KindRefund:  ordersv1.PaymentKind_PAYMENT_KIND_REFUND,
}

var paymentStates = map[payments.State]ordersv1.PaymentState{
	payments.StatePending: ordersv1.PaymentState_PAYMENT_STATE_PENDING,
	payments.StatePaid:    ordersv1.PaymentState_PAYMENT_STATE_PAID,
	payments.StateExpired: ordersv1.PaymentState_PAYMENT_STATE_EXPIRED,
	payments.StateFailed:  ordersv1.PaymentState_PAYMENT_STATE_FAILED,
}

func orderToProto(o orders.Order) *ordersv1.Order {
	out := &ordersv1.Order{
		Id: o.ID.String(), Code: o.Code, Status: orderStatuses[o.Status], PaymentStatus: paymentStatuses[o.Payment],
		Items:       itemsToProto(o.Items),
		SubtotalIdr: o.SubtotalIDR, TaxIdr: o.TaxIDR, TotalIdr: o.TotalIDR, DpRequiredIdr: o.DPRequiredIDR,
		Schedule: &ordersv1.Schedule{
			PickupAt: timestamppb.New(o.PickupAt), ProductionDate: o.ProductionDate.String(),
			DpDueAt: timestamppb.New(o.DPDueAt), BalanceDueAt: timestamppb.New(o.BalanceDueAt),
			FullPaymentRequired: o.FullPaymentRequired,
		},
		Notes: o.Notes, CustomerName: o.CustomerName, CreatedAt: timestamppb.New(o.CreatedAt),
	}
	for _, p := range o.Payments {
		pp := &ordersv1.Payment{
			Id: p.ID.String(), Kind: paymentKinds[p.Kind], AmountIdr: p.AmountIDR, FeeIdr: p.FeeIDR,
			State: paymentStates[p.State], CheckoutUrl: p.CheckoutURL,
		}
		if p.ExpiresAt != nil {
			pp.ExpiresAt = timestamppb.New(*p.ExpiresAt)
		}
		if p.PaidAt != nil {
			pp.PaidAt = timestamppb.New(*p.PaidAt)
		}
		out.Payments = append(out.Payments, pp)
	}
	return out
}

func itemsToProto(items []orders.QuotedItem) []*ordersv1.QuotedItem {
	out := make([]*ordersv1.QuotedItem, len(items))
	for i, it := range items {
		out[i] = &ordersv1.QuotedItem{
			VariantId: it.VariantID.String(), ProductName: it.ProductName, VariantName: it.VariantName,
			Quantity: it.Quantity, UnitPriceIdr: it.UnitPriceIDR, LineTotalIdr: it.LineTotalIDR,
		}
	}
	return out
}
