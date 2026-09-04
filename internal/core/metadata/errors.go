package metadata

import (
	"errors"
	"fmt"
)

// Sentinel causes. Every metadata failure wraps exactly one of these so
// callers classify with errors.Is instead of string matching.
var (
	// ErrNameConflict is an explicit --name / rename collision. It never gets
	// an implicit suffix; the operation fails with zero mutations.
	ErrNameConflict = errors.New("name is already in use")
	// ErrNameExhausted means 100 unpublished exact UID/name candidates collided.
	ErrNameExhausted = errors.New("automatic uid/name candidate space is exhausted")
	// ErrExplicitNameCardinality rejects one explicit address applied to more
	// than one create target before UID, Registry, tmux, or provider mutation.
	ErrExplicitNameCardinality = errors.New("explicit-name-cardinality")
	// ErrInvalidName marks a name that cannot be a stable query key.
	ErrInvalidName = errors.New("invalid resource name")
	// ErrInvalidRoot marks a root that is not an existing absolute directory.
	ErrInvalidRoot = errors.New("invalid project root")
	// ErrRootConflict marks a rebind onto a root already bound to another
	// Project uid.
	ErrRootConflict = errors.New("project root is already bound to another project")
	// ErrNotFound marks an unresolvable uid.
	ErrNotFound = errors.New("resource not found")
	// ErrInvalidPhase marks an Agent phase value or transition that is not in
	// the closed lifecycle model.
	ErrInvalidPhase = errors.New("invalid agent phase")
	// ErrInvalidRegistry marks a registry that violates a structural invariant.
	ErrInvalidRegistry = errors.New("invalid resource registry")
	// ErrSchemaTooNew marks a registry envelope newer than this build. It is
	// handled fail-closed: the caller refuses to read it as valid and performs
	// no write at all.
	ErrSchemaTooNew = errors.New("resource registry schema version is newer than supported")
	// ErrSchemaUnsupported marks an envelope version with no migration path.
	ErrSchemaUnsupported = errors.New("unsupported resource registry schema version")
)

// ExplicitNameCardinalityError returns the typed multi-target create refusal.
func ExplicitNameCardinalityError(op string, targets int) error {
	return inputErr(op, ErrExplicitNameCardinality,
		"explicit-name-cardinality: --name requires exactly one target; resolved %d", targets)
}

// usageMarker is implemented by metadata errors that are caused by invalid
// user input rather than by a runtime fault. internal/app maps these onto the
// existing UsageError -> exit code 2 path in cmd/projmux/main.go.
type usageMarker interface {
	MetadataUsageError() bool
}

// InputError is a typed metadata error caused by invalid user input. It
// carries the operation, a human-readable detail, and the sentinel cause.
type InputError struct {
	Op     string
	Detail string
	Cause  error
}

// Error renders "<op>: <detail>" with the sentinel cause appended when the
// detail does not already state it.
func (e *InputError) Error() string {
	switch {
	case e.Op == "" && e.Detail == "":
		return e.Cause.Error()
	case e.Detail == "":
		return fmt.Sprintf("%s: %v", e.Op, e.Cause)
	case e.Op == "":
		return e.Detail
	default:
		return fmt.Sprintf("%s: %s", e.Op, e.Detail)
	}
}

// Unwrap exposes the sentinel cause to errors.Is.
func (e *InputError) Unwrap() error { return e.Cause }

// MetadataUsageError marks this error as a usage error for IsUsageError.
func (e *InputError) MetadataUsageError() bool { return true }

func inputErr(op string, cause error, format string, args ...any) error {
	return &InputError{Op: op, Detail: fmt.Sprintf(format, args...), Cause: cause}
}

// StateError is a typed metadata error caused by inconsistent persisted state
// or an unavailable resource. It is not a usage error: it maps to exit 1.
type StateError struct {
	Op     string
	Detail string
	Cause  error
}

// Error renders "<op>: <detail>".
func (e *StateError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("%s: %v", e.Op, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Op, e.Detail)
}

// Unwrap exposes the sentinel cause to errors.Is.
func (e *StateError) Unwrap() error { return e.Cause }

func stateErr(op string, cause error, format string, args ...any) error {
	return &StateError{Op: op, Detail: fmt.Sprintf(format, args...), Cause: cause}
}

// IsUsageError reports whether err was caused by invalid user input. The app
// layer converts these into the CLI usage-error exit code 2.
func IsUsageError(err error) bool {
	var marker usageMarker
	return errors.As(err, &marker) && marker.MetadataUsageError()
}
