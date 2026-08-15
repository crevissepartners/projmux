package usage

import (
	"errors"
	"fmt"
	"strings"
)

// AdapterError attributes one collect failure to the adapter that produced
// it. The rendered message is unchanged from the previous `<model>: <err>`
// wrapping, so existing CLI warning output stays byte-identical; the typed
// form exists so callers can name the provider for the operations journal
// without parsing the string.
type AdapterError struct {
	Model string
	Err   error
}

func (e *AdapterError) Error() string { return e.Model + ": " + e.Err.Error() }

func (e *AdapterError) Unwrap() error { return e.Err }

// Partial reports whether the adapter still produced usable rows and only
// dropped the ones that failed validation.
func (e *AdapterError) Partial() bool { return errors.Is(e.Err, ErrRowsSkipped) }

// AdapterErrors flattens a Manager collect result into the per-adapter
// failures it carries, in the order the adapters ran. Collect joins every
// adapter's failure into one error, so a caller that needs to attribute a
// failure to a provider (the operations journal does) cannot use errors.As —
// that would stop at the first match and lose the rest.
func AdapterErrors(err error) []*AdapterError {
	var out []*AdapterError
	var walk func(error)
	walk = func(e error) {
		switch typed := e.(type) {
		case nil:
			return
		case *AdapterError:
			out = append(out, typed)
		case interface{ Unwrap() []error }:
			for _, sub := range typed.Unwrap() {
				walk(sub)
			}
		case interface{ Unwrap() error }:
			walk(typed.Unwrap())
		}
	}
	walk(err)
	return out
}

// ErrRowsSkipped marks a partial-collection warning: the adapter produced a
// usable result, but one or more upstream rows were dropped because they
// failed field validation. Callers use errors.Is to distinguish "some rows
// were lost" from "the whole collect failed" without parsing the message.
var ErrRowsSkipped = errors.New("usage rows skipped")

// maxSkipReasons bounds how many per-row reasons a warning renders. Upstream
// controls the row count, so an unbounded join would let a hostile or broken
// payload dictate the length of a line printed to the user's terminal and of
// the string a caller may hand to the bounded operations journal.
const maxSkipReasons = 5

// rowSkipError carries the rendered warning while still matching
// ErrRowsSkipped through errors.Is. Unwrap keeps the sentinel out of the
// message text so the user-facing line stays the bounded reason list.
type rowSkipError struct{ message string }

func (e *rowSkipError) Error() string { return e.message }

func (e *rowSkipError) Unwrap() error { return ErrRowsSkipped }

// RowSkipWarning renders the bounded, privacy-safe warning an adapter returns
// alongside the rows that DID parse. It returns nil when nothing was skipped,
// so callers can assign the result straight to their error return.
//
// Privacy contract: reasons must be built from row-index + field-name context
// only (`row 2: missing resets_at`). Raw upstream usage values, reset
// timestamps, bucket identities, and wrapped decoder error text must never
// reach this helper — decoder messages routinely quote the offending input.
func RowSkipWarning(reasons []string) error {
	if len(reasons) == 0 {
		return nil
	}
	noun := "row"
	if len(reasons) > 1 {
		noun = "rows"
	}
	shown := reasons
	suffix := ""
	if len(shown) > maxSkipReasons {
		shown = shown[:maxSkipReasons]
		suffix = fmt.Sprintf(" (+%d more)", len(reasons)-maxSkipReasons)
	}
	return &rowSkipError{message: fmt.Sprintf("skipped %d usage %s: %s%s",
		len(reasons), noun, strings.Join(shown, "; "), suffix)}
}
