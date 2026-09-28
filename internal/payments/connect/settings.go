// Package connect serves the payments module's Connect RPCs.
package connect

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	ordersv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/orders/v1"
	paymentsv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/payments/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/payments/v1/paymentsv1connect"
	"github.com/Zefanrakh/kue-preorder/internal/payments"
	"github.com/Zefanrakh/kue-preorder/internal/platform/rpcerr"
)

// SettingsHandler serves kuepreorder.payments.v1.PaymentSettingsService.
type SettingsHandler struct {
	settings *payments.Settings
	logger   *slog.Logger
}

var _ paymentsv1connect.PaymentSettingsServiceHandler = (*SettingsHandler)(nil)

// NewSettingsHandler returns a handler backed by settings.
func NewSettingsHandler(settings *payments.Settings, logger *slog.Logger) *SettingsHandler {
	return &SettingsHandler{settings: settings, logger: logger}
}

// GetPaymentSettings implements paymentsv1connect.PaymentSettingsServiceHandler.
func (h *SettingsHandler) GetPaymentSettings(ctx context.Context, _ *connect.Request[paymentsv1.GetPaymentSettingsRequest]) (*connect.Response[paymentsv1.GetPaymentSettingsResponse], error) {
	v, err := h.settings.Get(ctx)
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	return connect.NewResponse(&paymentsv1.GetPaymentSettingsResponse{Settings: settingsToProto(v)}), nil
}

// UpdatePaymentPolicy implements paymentsv1connect.PaymentSettingsServiceHandler.
func (h *SettingsHandler) UpdatePaymentPolicy(ctx context.Context, req *connect.Request[paymentsv1.UpdatePaymentPolicyRequest]) (*connect.Response[paymentsv1.UpdatePaymentPolicyResponse], error) {
	p := req.Msg.GetPolicy()
	v, err := h.settings.UpdatePolicy(ctx, payments.Policy{
		DPMinPercent: p.GetDpMinPercent(), DPCoversIngredientCost: p.GetDpCoversIngredientCost(),
		BalanceDueHoursBefore: p.GetBalanceDueHoursBefore(), DPInvoiceValidMinutes: p.GetDpInvoiceValidMinutes(),
		DPMinTotalIDR: p.GetDpMinTotalIdr(), MinOrderIDR: p.GetMinOrderIdr(),
	}, req.Msg.GetReason())
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	return connect.NewResponse(&paymentsv1.UpdatePaymentPolicyResponse{Settings: settingsToProto(v)}), nil
}

// UpdatePaymentMethod implements paymentsv1connect.PaymentSettingsServiceHandler.
func (h *SettingsHandler) UpdatePaymentMethod(ctx context.Context, req *connect.Request[paymentsv1.UpdatePaymentMethodRequest]) (*connect.Response[paymentsv1.UpdatePaymentMethodResponse], error) {
	r := req.Msg.GetRule()
	v, err := h.settings.UpdateMethod(ctx, payments.FeeRule{
		Method: methodFromProto(r.GetMethod()), FixedIDR: r.GetFixedIdr(), RateBPS: r.GetRateBps(),
		VATIncluded: r.GetVatIncluded(), Enabled: r.GetEnabled(),
	}, req.Msg.GetReason())
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	return connect.NewResponse(&paymentsv1.UpdatePaymentMethodResponse{Settings: settingsToProto(v)}), nil
}

// ResetPaymentMethod implements paymentsv1connect.PaymentSettingsServiceHandler.
func (h *SettingsHandler) ResetPaymentMethod(ctx context.Context, req *connect.Request[paymentsv1.ResetPaymentMethodRequest]) (*connect.Response[paymentsv1.ResetPaymentMethodResponse], error) {
	v, err := h.settings.ResetMethod(ctx, methodFromProto(req.Msg.GetMethod()), req.Msg.GetReason())
	if err != nil {
		return nil, rpcerr.Wrap(ctx, h.logger, err, nil)
	}
	return connect.NewResponse(&paymentsv1.ResetPaymentMethodResponse{Settings: settingsToProto(v)}), nil
}

var methods = map[payments.Method]ordersv1.PaymentMethod{
	payments.MethodQRIS:         ordersv1.PaymentMethod_PAYMENT_METHOD_QRIS,
	payments.MethodBankTransfer: ordersv1.PaymentMethod_PAYMENT_METHOD_BANK_TRANSFER,
	payments.MethodEWallet:      ordersv1.PaymentMethod_PAYMENT_METHOD_EWALLET,
	payments.MethodMinimarket:   ordersv1.PaymentMethod_PAYMENT_METHOD_MINIMARKET,
}

// methodFromProto maps an unspecified or unknown method to "", which the
// service refuses in its own words.
func methodFromProto(m ordersv1.PaymentMethod) payments.Method {
	for d, p := range methods {
		if p == m {
			return d
		}
	}
	return ""
}

func settingsToProto(v payments.SettingsView) *paymentsv1.PaymentSettings {
	p := v.Policy
	out := &paymentsv1.PaymentSettings{Policy: &paymentsv1.PaymentPolicy{
		DpMinPercent: p.DPMinPercent, DpCoversIngredientCost: p.DPCoversIngredientCost,
		BalanceDueHoursBefore: p.BalanceDueHoursBefore, DpInvoiceValidMinutes: p.DPInvoiceValidMinutes,
		DpMinTotalIdr: p.DPMinTotalIDR, MinOrderIdr: p.MinOrderIDR,
	}}
	if !p.UpdatedAt.IsZero() {
		out.Policy.UpdatedAt = timestamppb.New(p.UpdatedAt)
	}
	for _, m := range v.Methods {
		ms := &paymentsv1.PaymentMethodSettings{Rule: ruleToProto(m.Rule), DefaultRule: ruleToProto(m.Default)}
		for _, e := range m.Examples {
			ms.Examples = append(ms.Examples, &paymentsv1.ExampleFee{AmountIdr: e.AmountIDR, FeeIdr: e.FeeIDR})
		}
		out.Methods = append(out.Methods, ms)
	}
	return out
}

func ruleToProto(r payments.FeeRule) *paymentsv1.FeeRule {
	out := &paymentsv1.FeeRule{
		Method: methods[r.Method], FixedIdr: r.FixedIDR, RateBps: r.RateBPS, VatIncluded: r.VATIncluded, Enabled: r.Enabled,
	}
	if !r.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(r.UpdatedAt)
	}
	return out
}
