package app

import (
	"os"
	"strings"
	"testing"
)

// requireClaudeLeaseDirRemoved fails t when the Claude activation lease
// directory of t's own activation still exists once every cleanup registered
// after this call has run. claudeActivationLeaseDir lives under a fixed /tmp
// root, so a test that leaves a socket in it leaks one /tmp/pmx-ce-* entry
// per run that no TempDir cleanup ever reaches.
//
// Call it right after registering the removal of the private root that holds
// the Registry path. Cleanups run last-in first-out, so the check runs after
// the helper, listener, and fixture cleanups registered later, but while that
// root still exists. The checked directory is derived only from this test's
// own Registry path, which no other process can hold while the root exists,
// so a live Claude helper or a concurrent go test cannot make it fail. It
// never lists or globs /tmp.
//
// dir returns the exact lease directory, or "" when the fixture failed before
// its activation was known.
func requireClaudeLeaseDirRemoved(t testing.TB, dir func() string) {
	t.Helper()
	t.Cleanup(func() {
		path := dir()
		if path == "" {
			return
		}
		entries, err := os.ReadDir(path)
		if os.IsNotExist(err) {
			return
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("%s left its Claude activation lease directory %s behind (entries: %s, err: %v); remove what the test created in a cleanup",
			t.Name(), path, strings.Join(names, ", "), err)
		// The path is this test's own, so removing it keeps a failing run from
		// leaking the entry the guard reports.
		_ = os.RemoveAll(path)
	})
}
