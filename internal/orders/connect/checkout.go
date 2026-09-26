// Package connect serves orders over ConnectRPC. Handlers only translate
// between protobuf and the domain; every rule lives in package orders.
package connect

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	ordersv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/orders/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/orders/v1/ordersv1connect"
	"github.com/Zefanrakh/kue-preorder/internal/orders"
	"github.com/Zefanrakh/kue-preorder/internal/platform/rpcerr"
	"github.com/Zefanrakh/kue-preorder/internal/scheduling"
)

// CheckoutHandler serves kuepreorder.orders.v1.CheckoutService.
type CheckoutHandler struct {
	checkout *orders.Checkout
	logger   *slog.Logger
}

var _ ordersv1connect.CheckoutServiceHandler = (*CheckoutHandler)(nil)

// NewCheckoutHandler returns a handler backed by checkout.
func NewCheckoutHandler(checkout *orders.Checkout, logger *slog.Logger) *CheckoutHandler {
	return &CheckoutHandler{checkout: checkout, logger: logger}
}

// QuoteOrder implements ordersv1connect.CheckoutServiceHandler.
func (h *CheckoutHandler) QuoteOrder(ctx context.Context, req *connect.Request[ordersv1.QuoteOrderRequest]) (*connect.Response[ordersv1.QuoteOrderResponse], error) {
	m := req.Msg
	var ids rpcerr.IDs
	in := orders.QuoteRequest{}
	if m.GetPickupAt() != nil {
		in.PickupAt = m.GetPickupAt().AsTime()
	}
	for _, it := range m.GetItems() {
		in.Items = append(in.Items, orders.ItemRequest{VariantID: ids.Parse("items", it.GetVariantId()), Quantity: it.GetQuantity()})
	}
	if err := ids.Err(); err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	q, err := h.checkout.Quote(ctx, in)
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	return connect.NewResponse(quoteToProto(q)), nil
}

func quoteToProto(q orders.Quote) *ordersv1.QuoteOrderResponse {
	out := &ordersv1.QuoteOrderResponse{SubtotalIdr: q.SubtotalIDR, TaxIdr: q.TaxIDR, TotalIdr: q.TotalIDR, DpRequiredIdr: q.DPRequiredIDR}
	for _, it := range q.Items {
		out.Items = append(out.Items, &ordersv1.QuotedItem{
			VariantId: it.VariantID.String(), ProductName: it.ProductName, VariantName: it.VariantName,
			Quantity: it.Quantity, UnitPriceIdr: it.UnitPriceIDR, LineTotalIdr: it.LineTotalIDR,
		})
	}
	switch {
	case q.Schedule != nil:
		p := q.Schedule
		out.Pickup = &ordersv1.QuoteOrderResponse_Schedule{Schedule: &ordersv1.Schedule{
			PickupAt: timestamppb.New(p.PickupAt), ProductionDate: p.ProductionDate.String(),
			DpDueAt: timestamppb.New(p.DPDeadline), BalanceDueAt: timestamppb.New(p.BalanceDue),
			FullPaymentRequired: p.FullPaymentRequired,
		}}
	case q.Rejection != nil:
		r := &ordersv1.PickupRejection{Problem: problemToProto(q.Rejection), Message: q.Rejection.Message}
		if q.Suggestion != nil {
			r.SuggestedPickupAt = timestamppb.New(q.Suggestion.PickupAt)
		}
		out.Pickup = &ordersv1.QuoteOrderResponse_Rejection{Rejection: r}
	}
	return out
}

var problems = []struct {
	reason  error
	problem ordersv1.PickupProblem
}{
	{scheduling.ErrTooFar, ordersv1.PickupProblem_PICKUP_PROBLEM_TOO_FAR},
	{scheduling.ErrTooSoon, ordersv1.PickupProblem_PICKUP_PROBLEM_TOO_SOON},
	{scheduling.ErrOutsideWindow, ordersv1.PickupProblem_PICKUP_PROBLEM_OUTSIDE_HOURS},
	{scheduling.ErrClosed, ordersv1.PickupProblem_PICKUP_PROBLEM_CLOSED},
	{scheduling.ErrCutoffPassed, ordersv1.PickupProblem_PICKUP_PROBLEM_SHOPPING_CLOSED},
	{scheduling.ErrFull, ordersv1.PickupProblem_PICKUP_PROBLEM_FULL},
}

func problemToProto(r *scheduling.Rejection) ordersv1.PickupProblem {
	for _, p := range problems {
		if errors.Is(r, p.reason) {
			return p.problem
		}
	}
	return ordersv1.PickupProblem_PICKUP_PROBLEM_UNSPECIFIED
}
