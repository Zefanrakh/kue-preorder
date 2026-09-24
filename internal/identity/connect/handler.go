package connect

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"

	identityv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/identity/v1"
	"github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/identity/v1/identityv1connect"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
)

// Handler serves kuepreorder.identity.v1.IdentityService.
type Handler struct {
	svc    *identity.Service
	logger *slog.Logger
}

var _ identityv1connect.IdentityServiceHandler = (*Handler)(nil)

// NewHandler returns a handler backed by svc.
func NewHandler(svc *identity.Service, logger *slog.Logger) *Handler {
	return &Handler{svc: svc, logger: logger}
}

// WhoAmI implements identityv1connect.IdentityServiceHandler.
func (h *Handler) WhoAmI(ctx context.Context, _ *connect.Request[identityv1.WhoAmIRequest]) (*connect.Response[identityv1.WhoAmIResponse], error) {
	p, err := h.svc.Principal(ctx)
	if err != nil {
		return nil, h.connectError(ctx, err)
	}
	res := &identityv1.WhoAmIResponse{
		TenantId:   p.TenantID.String(),
		AuthUserId: p.AuthUserID.String(),
		Roles:      make([]identityv1.StaffRole, 0, len(p.Roles)),
	}
	for _, role := range p.Roles {
		res.Roles = append(res.Roles, toProtoRole(role))
	}
	if p.CustomerID != nil {
		id := p.CustomerID.String()
		res.CustomerId = &id
	}
	return connect.NewResponse(res), nil
}

func toProtoRole(role identity.Role) identityv1.StaffRole {
	switch role {
	case identity.RoleOwner:
		return identityv1.StaffRole_STAFF_ROLE_OWNER
	case identity.RoleKitchen:
		return identityv1.StaffRole_STAFF_ROLE_KITCHEN
	default:
		return identityv1.StaffRole_STAFF_ROLE_UNSPECIFIED
	}
}

// connectError maps domain errors to Connect codes. Unexpected errors are
// logged and hidden from the client.
func (h *Handler) connectError(ctx context.Context, err error) error {
	if errors.Is(err, identity.ErrUnauthenticated) {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("sign in required"))
	}
	h.logger.ErrorContext(ctx, "identity request failed", slog.Any("error", err))
	return connect.NewError(connect.CodeInternal, errors.New("internal error"))
}
