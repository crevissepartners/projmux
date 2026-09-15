package usagecmd

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/internal/diagnostics"
)

// TestMain keeps this package's tests away from the user's real operations
// journal and turns any test that still reaches the default journal into a
// package failure.
//
// A Command without journalFn resolves diagnostics.DefaultPath, which reads
// XDG_STATE_HOME and otherwise falls back to $HOME/.local/state. Both are
// pointed at a private directory before any test runs, so a forgotten
// journalFn writes there instead of the real journal, and the scan after
// m.Run reports what it wrote and fails the run.
func TestMain(m *testing.M) {
	os.Exit(runWithDefaultJournalGuard(m))
}

func runWithDefaultJournalGuard(m *testing.M) int {
	root, err := os.MkdirTemp("", "projmux-usagecmd-journal-guard-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "usagecmd journal guard: create private state root: %v\n", err)
		return 1
	}
	defer os.RemoveAll(root)

	for name, dir := range map[string]string{
		"HOME":           filepath.Join(root, "home"),
		"XDG_STATE_HOME": filepath.Join(root, "state"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fmt.Fprintf(os.Stderr, "usagecmd journal guard: create %s: %v\n", name, err)
			return 1
		}
		if err := os.Setenv(name, dir); err != nil {
			fmt.Fprintf(os.Stderr, "usagecmd journal guard: set %s: %v\n", name, err)
			return 1
		}
	}

	code := m.Run()
	if leaked := defaultJournalGuardLeaks(root); len(leaked) > 0 {
		fmt.Fprintln(os.Stderr, "FAIL: a test reached the default operations journal; inject journalFn")
		for _, line := range leaked {
			fmt.Fprintln(os.Stderr, "  "+line)
		}
		if code == 0 {
			code = 1
		}
	}
	return code
}

// defaultJournalGuardLeaks describes every record written to an operations
// journal under root by its closed fields only.
func defaultJournalGuardLeaks(root string) []string {
	var leaked []string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			leaked = append(leaked, fmt.Sprintf("unreadable guard path: %v", err))
			return nil
		}
		if entry.IsDir() || entry.Name() != diagnostics.LogFileName {
			return nil
		}
		events, readErr := diagnostics.NewStore(path).Read()
		if readErr != nil {
			leaked = append(leaked, fmt.Sprintf("unreadable journal %s: %v", path, readErr))
			return nil
		}
		for _, event := range events {
			leaked = append(leaked, fmt.Sprintf("event=%s provider=%s failure=%s", event.Event, event.Provider, event.Failure))
		}
		return nil
	})
	return leaked
}

// newTestUsageJournal returns a journalFn bound to a private journal and the
// store that reads it back.
func newTestUsageJournal(t *testing.T) (func() *diagnostics.UsageRecorder, *diagnostics.Store) {
	t.Helper()
	store := diagnostics.NewStore(filepath.Join(t.TempDir(), "logs", diagnostics.LogFileName))
	return func() *diagnostics.UsageRecorder {
		return diagnostics.NewUsageRecorder(store, "usagetestrun", "0.0.0-test", diagnostics.MuxBackend())
	}, store
}

// requireSingleUsageJournalRow asserts the private journal holds exactly one
// usage row with the given provider and failure.
func requireSingleUsageJournalRow(t *testing.T, store *diagnostics.Store, provider diagnostics.Provider, failure diagnostics.UsageFailure) {
	t.Helper()
	events, err := store.Read()
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("journal rows = %#v, want exactly one", events)
	}
	if events[0].Component != "usage" || events[0].Provider != string(provider) || events[0].Failure != string(failure) {
		t.Fatalf("journal row = %#v, want usage %s/%s", events[0], provider, failure)
	}
}
