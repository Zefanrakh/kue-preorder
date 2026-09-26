package connect

import (
	"errors"
	"log/slog"
	"testing"

	"connectrpc.com/connect"

	validationv1 "github.com/Zefanrakh/kue-preorder/api/gen/go/kuepreorder/validation/v1"
	"github.com/Zefanrakh/kue-preorder/internal/catalog"
)

// The service calls it params; SetRecipeLineRequest calls it params_json.
func TestConnectError_NamesRequestFields(t *testing.T) {
	err := connectError(t.Context(), slog.New(slog.DiscardHandler), &catalog.ValidationError{
		Fields: map[string]string{"params": "Parameter resep tidak valid."},
	})

	var cerr *connect.Error
	if !errors.As(err, &cerr) || len(cerr.Details()) != 1 {
		t.Fatalf("error = %v, want one detail", err)
	}
	v, _ := cerr.Details()[0].Value()
	if fe, ok := v.(*validationv1.FieldErrors); !ok || fe.GetFields()["params_json"] == "" {
		t.Errorf("detail = %v, want params_json", v)
	}
}
