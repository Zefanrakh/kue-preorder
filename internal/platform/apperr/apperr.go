// Package apperr holds the errors every module's service returns to its
// callers. Modules re-export them under their own names (catalog.ErrNotFound
// is apperr.ErrNotFound), so one mapping to Connect codes, platform/rpcerr,
// serves every module (docs/architecture.md §7).
package apperr

import (
	"errors"
	"maps"
	"slices"
	"strings"
)

var (
	// ErrUnauthenticated means the request carries no verified user.
	ErrUnauthenticated = errors.New("unauthenticated")
	// ErrForbidden means the caller's roles do not allow the action.
	ErrForbidden = errors.New("forbidden")
	// ErrNotFound means the record does not exist in the caller's tenant.
	ErrNotFound = errors.New("not found")
	// ErrFailedPrecondition means the request is fine but the state does not
	// allow it yet; a *PreconditionError says which state and what to do.
	ErrFailedPrecondition = errors.New("failed precondition")
)

// PreconditionError refuses a request until something else happens first,
// such as signing in with a phone number. Reason is a stable code the client
// acts on; Message tells the customer what to do, in Indonesian.
type PreconditionError struct {
	Reason  string
	Message string
}

func (e *PreconditionError) Error() string {
	return "failed precondition " + e.Reason + ": " + e.Message
}

// Is makes errors.Is(err, ErrFailedPrecondition) true.
func (e *PreconditionError) Is(target error) bool { return target == ErrFailedPrecondition }

// ValidationError lists invalid fields with messages for the person editing,
// in Indonesian. Conflict marks a clash with an existing record, such as a
// SKU already in use.
type ValidationError struct {
	Fields   map[string]string
	Conflict bool
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for _, f := range slices.Sorted(maps.Keys(e.Fields)) {
		parts = append(parts, f+": "+e.Fields[f])
	}
	return "invalid input: " + strings.Join(parts, "; ")
}

// Fields collects field errors. The first message for a field wins, so
// checks go from the most basic to the most specific.
type Fields map[string]string

// Check records msg for field unless ok.
func (f Fields) Check(ok bool, field, msg string) {
	if !ok {
		if _, seen := f[field]; !seen {
			f[field] = msg
		}
	}
}

// Err returns the collected errors as a *ValidationError, or nil.
func (f Fields) Err() error {
	if len(f) == 0 {
		return nil
	}
	return &ValidationError{Fields: f}
}

// Join merges the field errors of several checks into one ValidationError.
// Any other error is returned as it is, before anything is merged.
func Join(errs ...error) error {
	merged := Fields{}
	for _, err := range errs {
		if err == nil {
			continue
		}
		var v *ValidationError
		if !errors.As(err, &v) {
			return err
		}
		for f, msg := range v.Fields {
			merged.Check(false, f, msg)
		}
	}
	return merged.Err()
}
