//go:build windows

package app

import "errors"

// errControllerLeaseUnsupported lives beside the only implementation that returns
// it. Declaring it next to the trigger would leave it unused on every platform
// projmux actually ships.
var errControllerLeaseUnsupported = errors.New("controller worker lease is not supported on this platform")

// acquireControllerLease has no Windows implementation.
//
// The file name carries the constraint rather than a `!unix` build tag alone,
// which is the pattern the sibling platform files here already use: a tool that
// globs a package directory without evaluating build tags would otherwise compile
// both halves of the pair and see one symbol declared twice.
//
// Reporting "not acquired" without an error would be the worst of the two
// available answers: every trigger would silently decline to converge and the
// registry would drift with no diagnostic. Refusing loudly keeps the missing
// port visible on a platform projmux does not ship a tmux host for anyway.
func acquireControllerLease(string) (func(), bool, error) {
	return nil, false, errControllerLeaseUnsupported
}
