package usagecmd

import (
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/internal/diagnostics"
)

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
