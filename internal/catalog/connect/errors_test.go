package connect

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
	"github.com/Zefanrakh/kue-preorder/internal/catalog"
	"github.com/Zefanrakh/kue-preorder/internal/identity"
)

func TestConnectError_Codes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want connect.Code
	}{
		{"invalid input", &catalog.ValidationError{Fields: map[string]string{"name": "x"}}, connect.CodeInvalidArgument},
		{"conflict", &catalog.ValidationError{Fields: map[string]string{"sku": "x"}, Conflict: true}, connect.CodeAlreadyExists},
		{"wrapped not found", fmt.Errorf("load: %w", catalog.ErrNotFound), connect.CodeNotFound},
		{"forbidden", catalog.ErrForbidden, connect.CodePermissionDenied},
		{"unauthenticated", identity.ErrUnauthenticated, connect.CodeUnauthenticated},
		{"anything else", errors.New("boom"), connect.CodeInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := connectError(t.Context(), slog.New(slog.DiscardHandler), tt.err)
			if connect.CodeOf(got) != tt.want {
				t.Errorf("code = %v, want %v", connect.CodeOf(got), tt.want)
			}
		})
	}
}

func TestConnectError_FieldErrorsNameRequestFields(t *testing.T) {
	err := connectError(t.Context(), slog.New(slog.DiscardHandler), &catalog.ValidationError{
		Fields: map[string]string{"params": "Parameter resep tidak valid.", "waste_factor": "Faktor susut minimal 1."},
	})

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
func TestConnectError_HidesAndLogsUnexpectedErrors(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	err := connectError(t.Context(), logger, errors.New(`relation "products" does not exist`))

	if strings.Contains(err.Error(), "products") {
		t.Errorf("client sees %q, want no detail", err)
	}
	if !strings.Contains(logs.String(), `"level":"ERROR"`) || !strings.Contains(logs.String(), `relation \"products\" does not exist`) {
		t.Errorf("log = %s, want the detail at ERROR", logs.String())
	}
}

func TestIDs(t *testing.T) {
	id := uuid.New()
	var p ids

	if got := p.parse("id", id.String()); got != id {
		t.Errorf("parse(valid) = %v, want %v", got, id)
	}
	if got := p.parse("product_id", ""); got != uuid.Nil {
		t.Errorf("parse(empty) = %v, want uuid.Nil", got)
	}
	if err := p.err(); err != nil {
		t.Fatalf("err() = %v after valid and empty IDs, want nil", err)
	}

	p.parse("supplier_id", "toko")
	p.parse("ingredient_id", "12345")
	var v *catalog.ValidationError
	if err := p.err(); !errors.As(err, &v) || len(v.Fields) != 2 || v.Fields["supplier_id"] == "" || v.Fields["ingredient_id"] == "" {
		t.Errorf("err() = %v, want both malformed fields", p.err())
	}
}
