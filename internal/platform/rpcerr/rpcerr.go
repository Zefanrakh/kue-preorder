// Package rpcerr turns service errors into Connect errors the same way for
// every module (docs/architecture.md §7). Validation problems carry a
// kuepreorder.validation.v1.FieldErrors detail for the form; unexpected
// errors are logged at ERROR, which alerts, and hidden from the client.
package rpcerr

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	validationv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/validation/v1"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
)

// Wrap maps err to a Connect error. rename turns the service's field names
// into request field names where they differ, so a form can mark the field
// it sent.
func Wrap(ctx context.Context, logger *slog.Logger, err error, rename map[string]string) error {
	var invalid *apperr.ValidationError
	switch {
	case errors.As(err, &invalid):
		code, msg := connect.CodeInvalidArgument, "invalid input"
		if invalid.Conflict {
			code, msg = connect.CodeAlreadyExists, "conflicts with an existing record"
		}
		fields := make(map[string]string, len(invalid.Fields))
		for f, m := range invalid.Fields {
			if renamed, ok := rename[f]; ok {
				f = renamed
			}
			fields[f] = m
		}
		cerr := connect.NewError(code, errors.New(msg))
		if detail, derr := connect.NewErrorDetail(&validationv1.FieldErrors{Fields: fields}); derr == nil {
			cerr.AddDetail(detail)
		}
		return cerr
	case errors.Is(err, apperr.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("not found"))
	case errors.Is(err, apperr.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, errors.New("your role may not do this"))
	case errors.Is(err, apperr.ErrUnauthenticated):
		return connect.NewError(connect.CodeUnauthenticated, errors.New("sign in required"))
	default:
		logger.ErrorContext(ctx, "request failed", slog.Any("error", err))
		return connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
}

// IDs parses the UUID fields of one request, collecting every malformed one
// into a single validation error. An empty field parses as uuid.Nil, which
// the service reports in its own words ("Pilih produknya.").
type IDs struct {
	bad apperr.Fields
}

// Parse returns the UUID in s, uuid.Nil when s is empty, and records field
// as malformed otherwise.
func (p *IDs) Parse(field, s string) uuid.UUID {
	if s == "" {
		return uuid.Nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		if p.bad == nil {
			p.bad = apperr.Fields{}
		}
		p.bad.Check(false, field, "ID tidak valid.")
	}
	return id
}

// Err returns the malformed fields as a *apperr.ValidationError, or nil.
func (p *IDs) Err() error {
	return p.bad.Err()
}
