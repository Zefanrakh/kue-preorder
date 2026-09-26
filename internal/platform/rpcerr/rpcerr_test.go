package rpcerr_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	validationv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/validation/v1"
	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
	"github.com/Zefanrakh/kue-preorder/internal/platform/rpcerr"
)

func TestWrap_Codes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want connect.Code
	}{
		{"invalid input", &apperr.ValidationError{Fields: map[string]string{"name": "x"}}, connect.CodeInvalidArgument},
		{"conflict", &apperr.ValidationError{Fields: map[string]string{"sku": "x"}, Conflict: true}, connect.CodeAlreadyExists},
		{"wrapped not found", fmt.Errorf("load: %w", apperr.ErrNotFound), connect.CodeNotFound},
		{"forbidden", apperr.ErrForbidden, connect.CodePermissionDenied},
		{"unauthenticated", apperr.ErrUnauthenticated, connect.CodeUnauthenticated},
		{"anything else", errors.New("boom"), connect.CodeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rpcerr.Wrap(t.Context(), slog.New(slog.DiscardHandler), tt.err, nil)
			if connect.CodeOf(got) != tt.want {
				t.Errorf("code = %v, want %v", connect.CodeOf(got), tt.want)
			}
		})
	}
}

func TestWrap_RenamesFields(t *testing.T) {
	err := rpcerr.Wrap(t.Context(), slog.New(slog.DiscardHandler), &apperr.ValidationError{
		Fields: map[string]string{"params": "Parameter resep tidak valid.", "waste_factor": "Faktor susut minimal 1."},
	}, map[string]string{"params": "params_json"})

	var cerr *connect.Error
	if !errors.As(err, &cerr) || len(cerr.Details()) != 1 {
		t.Fatalf("error = %v, want one detail", err)
	}
	v, derr := cerr.Details()[0].Value()
	fe, ok := v.(*validationv1.FieldErrors)
	if derr != nil || !ok {
		t.Fatalf("detail = %v (%v), want FieldErrors", v, derr)
	}
	want := map[string]string{"params_json": "Parameter resep tidak valid.", "waste_factor": "Faktor susut minimal 1."}
	if !maps.Equal(fe.GetFields(), want) {
		t.Errorf("fields = %v, want %v", fe.GetFields(), want)
	}
}

// Unexpected errors can hold SQL or connection details: they go to the log
// (and Sentry, at ERROR), never to the client.
func TestWrap_HidesAndLogsUnexpectedErrors(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	err := rpcerr.Wrap(t.Context(), logger, errors.New(`relation "products" does not exist`), nil)

	if strings.Contains(err.Error(), "products") {
		t.Errorf("client sees %q, want no detail", err)
	}
	if !strings.Contains(logs.String(), `"level":"ERROR"`) || !strings.Contains(logs.String(), `relation \"products\" does not exist`) {
		t.Errorf("log = %s, want the detail at ERROR", logs.String())
	}
}

func TestIDs(t *testing.T) {
	id := uuid.New()
	var p rpcerr.IDs

	if got := p.Parse("id", id.String()); got != id {
		t.Errorf("parse(valid) = %v, want %v", got, id)
	}
	if got := p.Parse("product_id", ""); got != uuid.Nil {
		t.Errorf("parse(empty) = %v, want uuid.Nil", got)
	}
	if err := p.Err(); err != nil {
		t.Fatalf("err() = %v after valid and empty IDs, want nil", err)
	}

	p.Parse("supplier_id", "toko")
	p.Parse("ingredient_id", "12345")
	var v *apperr.ValidationError
	if err := p.Err(); !errors.As(err, &v) || len(v.Fields) != 2 || v.Fields["supplier_id"] == "" || v.Fields["ingredient_id"] == "" {
		t.Errorf("err() = %v, want both malformed fields", p.Err())
	}
}
