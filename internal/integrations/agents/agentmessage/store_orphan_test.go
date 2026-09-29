package agentmessage

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
)

// plantOrphans leaves in the store directory what a write killed between its
// temp file and its rename leaves, beside entries the cleanup must not touch,
// and returns the orphan paths and the untouched entries with their contents.
func plantOrphans(t *testing.T, store *Store) ([]string, map[string]string) {
	t.Helper()
	dir := filepath.Dir(store.Path())
	orphans := []string{
		filepath.Join(dir, tempPrefix+"1111111111"),
		filepath.Join(dir, tempPrefix+"2222222222"),
	}
	for _, path := range orphans {
		if err := os.WriteFile(path, []byte(`{"version":2,"records":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	kept := map[string]string{
		store.historyPath(): "{}\n",
		filepath.Join(dir, "release-0123456789ab.flock"): "",
		filepath.Join(dir, "notes.txt"):                  "unrelated\n",
		filepath.Join(dir, "messages.json.bak"):          "unrelated\n",
		filepath.Join(dir, ".questions.tmp-1"):           "another store's temp name\n",
		filepath.Join(dir, ".requests.tmp-1"):            "another store's temp name\n",
	}
	for path, body := range kept {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A directory with the temp prefix is not a file a write made.
	if err := os.Mkdir(filepath.Join(dir, tempPrefix+"dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	return orphans, kept
}

func tempEntries(t *testing.T, store *Store) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(store.Path()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), tempPrefix) {
			names = append(names, entry.Name())
		}
	}
	return names
}

// TestStoreWriteRemovesTempFilesADeadWriteLeft holds the cleanup a crash
// between the temp file and the rename relies on: the next write of every kind
// removes the temp files left in the store directory and nothing else, and
// still commits its own change.
func TestStoreWriteRemovesTempFilesADeadWriteLeft(t *testing.T) {
	t.Parallel()

	first := storeEnvelope(1)
	first.Target.Provider = "claude"
	codex := storeEnvelope(3)
	cases := []struct {
		name  string
		write func(*Store) (string, error)
		check func(Record) bool
	}{
		{"put accepted", func(store *Store) (string, error) {
			record, _, err := store.PutAccepted(storeEnvelope(2), "codex-inbox")
			return record.Envelope.MessageRef, err
		}, func(record Record) bool { return record.Delivery.State == coremessage.StateAccepted }},
		{"apply", func(store *Store) (string, error) {
			record, _, err := store.Apply(first.MessageRef, coremessage.Event{Kind: coremessage.EventHold, MessageRef: first.MessageRef,
				ConversationRef: first.ConversationRef, Target: first.Target, Reason: "target-awaiting-operator",
				ObservedAt: first.AcceptedAt})
			return record.Envelope.MessageRef, err
		}, func(record Record) bool { return record.Delivery.State == coremessage.StateHeld }},
		{"mark handoff", func(store *Store) (string, error) {
			record, _, err := store.MarkHandoff(first.MessageRef)
			return record.Envelope.MessageRef, err
		}, func(record Record) bool { return record.HandoffObserved }},
		{"claim", func(store *Store) (string, error) {
			record, _, err := store.Claim(codex.Target, storeTestNow.Add(time.Minute))
			return record.Envelope.MessageRef, err
		}, func(record Record) bool { return record.Delivery.State == coremessage.StateDelivered }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := frozenStore(NewStoreAt(filepath.Join(t.TempDir(), "messages.json")))
			if _, _, err := store.PutAccepted(first, "claude-coordination"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.PutAccepted(codex, "codex-inbox"); err != nil {
				t.Fatal(err)
			}
			orphans, kept := plantOrphans(t, store)
			var removed []string
			recording := *store
			recording.hooks.removeTemp = func(path string) error {
				removed = append(removed, path)
				return os.Remove(path)
			}

			ref, err := tc.write(&recording)
			if err != nil || ref == "" {
				t.Fatalf("write = %q, %v; want it to commit", ref, err)
			}
			if got := tempEntries(t, store); !slices.Equal(got, []string{tempPrefix + "dir"}) {
				t.Fatalf("temp entries after the write = %v, want only the directory", got)
			}
			slices.Sort(removed)
			if !slices.Equal(removed, orphans) {
				t.Fatalf("removed = %v, want exactly the orphan files %v", removed, orphans)
			}
			for path, body := range kept {
				if data, err := os.ReadFile(path); err != nil || string(data) != body {
					t.Fatalf("%s = %q, %v; want it untouched", path, data, err)
				}
			}
			if _, err := os.Stat(store.Path() + ".flock"); err != nil {
				t.Fatalf("lock file: %v", err)
			}
			record, ok, err := store.Get(ref)
			if err != nil || !ok || !tc.check(record) {
				t.Fatalf("record = %+v, %v, %v; want the write committed", record, ok, err)
			}
		})
	}
}

// TestStoreTempCleanupFailureDoesNotFailTheWrite holds that the cleanup is best
// effort: a temp file that cannot be removed stays, the write commits, and a
// later write whose removal works clears it.
func TestStoreTempCleanupFailureDoesNotFailTheWrite(t *testing.T) {
	t.Parallel()

	store := frozenStore(NewStoreAt(filepath.Join(t.TempDir(), "messages.json")))
	if _, _, err := store.PutAccepted(storeEnvelope(1), "codex-inbox"); err != nil {
		t.Fatal(err)
	}
	orphans, _ := plantOrphans(t, store)
	refusing := *store
	refusing.hooks.removeTemp = func(path string) error {
		return &fs.PathError{Op: "remove", Path: path, Err: syscall.EPERM}
	}

	envelope := storeEnvelope(2)
	record, created, err := refusing.PutAccepted(envelope, "codex-inbox")
	if err != nil || !created || record.Delivery.State != coremessage.StateAccepted {
		t.Fatalf("put = %+v, %t, %v; want it accepted despite the cleanup failure", record, created, err)
	}
	if stored, ok, err := store.Get(envelope.MessageRef); err != nil || !ok || stored != record {
		t.Fatalf("stored = %+v, %v, %v; want %+v", stored, ok, err, record)
	}
	for _, path := range orphans {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("orphan %s: %v; want it left for a later write", path, err)
		}
	}

	if _, _, err := store.PutAccepted(storeEnvelope(3), "codex-inbox"); err != nil {
		t.Fatal(err)
	}
	if got := tempEntries(t, store); !slices.Equal(got, []string{tempPrefix + "dir"}) {
		t.Fatalf("temp entries after a later write = %v, want only the directory", got)
	}
}
