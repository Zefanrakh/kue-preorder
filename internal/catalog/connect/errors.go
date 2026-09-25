// Package connect serves the catalog over ConnectRPC: the CMS as
// kuepreorder.catalog.v1.CatalogAdminService and the public shop as
// kuepreorder.catalog.v1.StorefrontService. Handlers only translate between
// protobuf and the catalog's domain types; every rule lives in catalog.Service
// and catalog.Storefront.
package connect

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	validationv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/validation/v1"
	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
)

// requestField renames the service's field names that differ from the
// request messages, so a form can mark the field it sent.
var requestField = map[string]string{
	"params": "params_json",
}

// connectError maps catalog errors to Connect codes. Validation problems carry
// a FieldErrors detail for the form; unexpected errors are logged and hidden.
func connectError(ctx context.Context, logger *slog.Logger, err error) error {
	var invalid *catalog.ValidationError
	switch {
	case errors.As(err, &invalid):
		code, msg := connect.CodeInvalidArgument, "invalid input"
		if invalid.Conflict {
			code, msg = connect.CodeAlreadyExists, "conflicts with an existing record"
		}
		fields := make(map[string]string, len(invalid.Fields))
		for f, m := range invalid.Fields {
			if renamed, ok := requestField[f]; ok {
				f = renamed
			}
			fields[f] = m
		}
		cerr := connect.NewError(code, errors.New(msg))
		if detail, derr := connect.NewErrorDetail(&validationv1.FieldErrors{Fields: fields}); derr == nil {
			cerr.AddDetail(detail)
		}
		return cerr
	case errors.Is(err, catalog.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("not found"))
	case errors.Is(err, catalog.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, errors.New("your role may not do this"))
	case errors.Is(err, identity.ErrUnauthenticated):
		return connect.NewError(connect.CodeUnauthenticated, errors.New("sign in required"))
	default:
		logger.ErrorContext(ctx, "catalog request failed", slog.Any("error", err))
		return connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
}

// ids parses the UUID fields of one request, collecting every malformed one
// into a single validation error. An empty field parses as uuid.Nil, which
// the service reports in its own words ("Pilih produknya.").
type ids struct {
	bad map[string]string
}

func (p *ids) parse(field, s string) uuid.UUID {
	if s == "" {
		return uuid.Nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		if p.bad == nil {
			p.bad = map[string]string{}
		}
		p.bad[field] = "ID tidak valid."
	}
	return id
}

func (p *ids) err() error {
	if len(p.bad) == 0 {
		return nil
	}
	return &catalog.ValidationError{Fields: p.bad}
}
