package aisessions

import "sync/atomic"

// claudeScanObserver is a test-only observation seam that names every file the
// Claude candidate scan hands to the parser.
//
// It is deliberately the smallest seam that makes the Claude lane's cost
// measurable: the cost is "files opened", and that is the only property a test
// can pin without measuring a clock, which is exactly what would make the
// assertion load-dependent and flaky.
//
// Nothing outside a _test.go installs an observer. It is not reachable from a
// CLI flag, a config key, an env var, a Registry field, or any rendered value,
// and an installed observer changes no discovery behaviour whatsoever: no file
// is opened, skipped, ordered, parsed, or filtered differently, and no result
// depends on whether one is present.
var claudeScanObserver atomic.Pointer[func(path string)]

// observeClaudeScan reports one scanned file to the installed observer, if any.
func observeClaudeScan(path string) {
	if observer := claudeScanObserver.Load(); observer != nil {
		(*observer)(path)
	}
}
