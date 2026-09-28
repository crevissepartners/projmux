// Package operatorclient owns the one name rule for operator clients: the
// in-process surfaces (the TUI, or a client layered on top of projmux) that act
// for the operator and say so by name.
//
// A client name is an opaque value. This package knows no particular client and
// keeps no list of them; it only decides whether a name is well formed, so that
// every record carrying one -- a creator annotation, a message origin, an audit
// line -- reads and compares the same way. A well-formed name proves nothing
// about which client really produced a record: operator records are guarded by
// being buildable only in process, not by the name.
package operatorclient

import (
	"errors"
	"fmt"
)

// MaxBytes is the longest client name the rule admits.
const MaxBytes = 32

// ReasonInvalid is the closed refusal token for a name outside the rule.
const ReasonInvalid = "operator-client-invalid"

// ErrInvalid wraps every refusal Validate returns.
var ErrInvalid = errors.New(ReasonInvalid)

// Valid reports whether name is an operator client name: 1 to MaxBytes bytes of
// lowercase ASCII letters, digits, and '-', starting with a letter.
func Valid(name string) bool {
	if len(name) == 0 || len(name) > MaxBytes {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z':
		case i > 0 && (c >= '0' && c <= '9' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// Validate returns nil for a valid name and an error wrapping ErrInvalid that
// quotes the name otherwise.
func Validate(name string) error {
	if Valid(name) {
		return nil
	}
	return fmt.Errorf("%w: %q must be 1-%d bytes of lowercase ASCII letters, digits, and '-', starting with a letter",
		ErrInvalid, name, MaxBytes)
}
