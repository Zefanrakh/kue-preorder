// Package connect serves scheduling over ConnectRPC as
// kuepreorder.scheduling.v1.ScheduleAdminService. Handlers only translate
// between protobuf and the domain; every rule lives in scheduling.Service.
package connect

import (
	"context"
	"log/slog"
	"math"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	schedulingv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/scheduling/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/scheduling/v1/schedulingv1connect"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/clock"
	"github.com/Zefanrakh/kue-preorder/internal/platform/rpcerr"
	"github.com/Zefanrakh/kue-preorder/internal/scheduling"
)

// Handler serves kuepreorder.scheduling.v1.ScheduleAdminService.
type Handler struct {
	svc    *scheduling.Service
	logger *slog.Logger
}

var _ schedulingv1connect.ScheduleAdminServiceHandler = (*Handler)(nil)

// NewHandler returns a handler backed by svc.
func NewHandler(svc *scheduling.Service, logger *slog.Logger) *Handler {
	return &Handler{svc: svc, logger: logger}
}

func (h *Handler) fail(ctx context.Context, err error) error {
	return rpcerr.Wrap(ctx, h.logger, err, nil)
}

// GetScheduleSettings implements schedulingv1connect.ScheduleAdminServiceHandler.
func (h *Handler) GetScheduleSettings(ctx context.Context, _ *connect.Request[schedulingv1.GetScheduleSettingsRequest]) (*connect.Response[schedulingv1.GetScheduleSettingsResponse], error) {
	s, err := h.svc.Settings(ctx)
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&schedulingv1.GetScheduleSettingsResponse{Settings: settingsToProto(s)}), nil
}

// UpdateScheduleSettings implements schedulingv1connect.ScheduleAdminServiceHandler.
func (h *Handler) UpdateScheduleSettings(ctx context.Context, req *connect.Request[schedulingv1.UpdateScheduleSettingsRequest]) (*connect.Response[schedulingv1.UpdateScheduleSettingsResponse], error) {
	m := req.Msg
	var p parser
	in := scheduling.Settings{
		ShoppingBufferHours: m.GetShoppingBufferHours(), DailyCapacityMinutes: m.DailyCapacityMinutes,
		PickupStart: p.timeOfDay("pickup_window_start", m.GetPickupWindowStart()),
		PickupEnd:   p.timeOfDay("pickup_window_end", m.GetPickupWindowEnd()),
	}
	if err := p.err(); err != nil {
		return nil, h.fail(ctx, err)
	}
	s, err := h.svc.UpdateSettings(ctx, in)
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&schedulingv1.UpdateScheduleSettingsResponse{Settings: settingsToProto(s)}), nil
}

// ListClosedDates implements schedulingv1connect.ScheduleAdminServiceHandler.
func (h *Handler) ListClosedDates(ctx context.Context, req *connect.Request[schedulingv1.ListClosedDatesRequest]) (*connect.Response[schedulingv1.ListClosedDatesResponse], error) {
	var p parser
	from, to := p.date("from_date", req.Msg.GetFromDate()), p.date("to_date", req.Msg.GetToDate())
	if err := p.err(); err != nil {
		return nil, h.fail(ctx, err)
	}
	dates, err := h.svc.ClosedDates(ctx, from, to)
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	out := make([]*schedulingv1.ClosedDate, len(dates))
	for i, d := range dates {
		out[i] = closedDateToProto(d)
	}
	return connect.NewResponse(&schedulingv1.ListClosedDatesResponse{ClosedDates: out}), nil
}

// AddClosedDate implements schedulingv1connect.ScheduleAdminServiceHandler.
func (h *Handler) AddClosedDate(ctx context.Context, req *connect.Request[schedulingv1.AddClosedDateRequest]) (*connect.Response[schedulingv1.AddClosedDateResponse], error) {
	var p parser
	d := p.date("date", req.Msg.GetDate())
	if err := p.err(); err != nil {
		return nil, h.fail(ctx, err)
	}
	closed, err := h.svc.AddClosedDate(ctx, d, req.Msg.GetReason())
	if err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&schedulingv1.AddClosedDateResponse{ClosedDate: closedDateToProto(closed)}), nil
}

// RemoveClosedDate implements schedulingv1connect.ScheduleAdminServiceHandler.
func (h *Handler) RemoveClosedDate(ctx context.Context, req *connect.Request[schedulingv1.RemoveClosedDateRequest]) (*connect.Response[schedulingv1.RemoveClosedDateResponse], error) {
	var p parser
	d := p.date("date", req.Msg.GetDate())
	if err := p.err(); err != nil {
		return nil, h.fail(ctx, err)
	}
	if err := h.svc.RemoveClosedDate(ctx, d); err != nil {
		return nil, h.fail(ctx, err)
	}
	return connect.NewResponse(&schedulingv1.RemoveClosedDateResponse{}), nil
}

func settingsToProto(s scheduling.Settings) *schedulingv1.ScheduleSettings {
	out := &schedulingv1.ScheduleSettings{
		ShoppingBufferHours: s.ShoppingBufferHours, DailyCapacityMinutes: s.DailyCapacityMinutes,
		PickupWindowStart: s.PickupStart.String(), PickupWindowEnd: s.PickupEnd.String(),
	}
	if !s.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(s.UpdatedAt)
	}
	return out
}

func closedDateToProto(d scheduling.ClosedDate) *schedulingv1.ClosedDate {
	active := d.ActiveOrders
	if active > math.MaxInt32 || active < 0 {
		active = math.MaxInt32
	}
	return &schedulingv1.ClosedDate{
		Date: d.Date.String(), Reason: d.Reason, CreatedAt: timestamppb.New(d.CreatedAt), ActiveOrders: int32(active),
	}
}

// parser reads the date and time fields of one request, collecting every
// malformed one into a single validation error.
type parser struct {
	bad apperr.Fields
}

func (p *parser) fail(field, msg string) {
	if p.bad == nil {
		p.bad = apperr.Fields{}
	}
	p.bad.Check(false, field, msg)
}

func (p *parser) date(field, s string) clock.Date {
	d, err := clock.ParseDate(s)
	if err != nil {
		p.fail(field, "Tanggal tidak valid, tulis seperti 2026-10-05.")
	}
	return d
}

func (p *parser) timeOfDay(field, s string) scheduling.TimeOfDay {
	t, err := scheduling.ParseTimeOfDay(s)
	if err != nil {
		p.fail(field, "Jam tidak valid, tulis seperti 09:00.")
	}
	return t
}

func (p *parser) err() error { return p.bad.Err() }
