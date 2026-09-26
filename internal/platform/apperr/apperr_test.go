package apperr_test

import (
	"errors"
	"maps"
	"testing"

	"github.com/Zefanrakh/kue-preorder/internal/platform/apperr"
)

func TestFields_FirstMessageWins(t *testing.T) {
	f := apperr.Fields{}
	f.Check(true, "name", "never recorded")
	f.Check(false, "name", "Nama wajib diisi.")
	f.Check(false, "name", "Nama terlalu panjang.")

	var v *apperr.ValidationError
	if err := f.Err(); !errors.As(err, &v) || !maps.Equal(v.Fields, map[string]string{"name": "Nama wajib diisi."}) {
		t.Errorf("Err() = %v, want only the first message for name", err)
	}
	if err := (apperr.Fields{}).Err(); err != nil {
		t.Errorf("Err() of no fields = %v, want nil", err)
	}
}

func TestJoin(t *testing.T) {
	a := &apperr.ValidationError{Fields: map[string]string{"name": "a"}}
	b := &apperr.ValidationError{Fields: map[string]string{"name": "b", "slug": "c"}}

	var v *apperr.ValidationError
	if err := apperr.Join(a, nil, b); !errors.As(err, &v) || !maps.Equal(v.Fields, map[string]string{"name": "a", "slug": "c"}) {
		t.Errorf("Join() = %v, want name from the first and slug from the second", err)
	}
	if err := apperr.Join(nil, nil); err != nil {
		t.Errorf("Join(nil, nil) = %v, want nil", err)
	}
	boom := errors.New("boom")
	if err := apperr.Join(a, boom); !errors.Is(err, boom) {
		t.Errorf("Join(validation, boom) = %v, want boom", err)
	}
}
