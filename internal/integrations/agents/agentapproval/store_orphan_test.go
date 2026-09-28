package agentapproval

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
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
		if err := os.WriteFile(path, []byte(`{"version":1,"records":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	kept := map[string]string{
		store.AuditPath() + auditRotatedSuffix:  "{}\n",
		filepath.Join(dir, "notes.txt"):         "unrelated\n",
		filepath.Join(dir, "requests.json.bak"): "unrelated\n",
		filepath.Join(dir, ".questions.tmp-1"):  "another store's temp name\n",
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
// between the answer line and the rename relies on: the next write of every
// kind removes the temp files left in the store directory and nothing else,
// and still commits its own record with its audit line.
func TestStoreWriteRemovesTempFilesADeadWriteLeft(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		write func(*Store, *testClock) (string, error)
		state State
		event string
	}{
		{"create", func(store *Store, clock *testClock) (string, error) {
			record, err := store.Create(Record{
				ID: testID(2), AgentUID: "agt-a", SessionID: "sess-2", ToolName: "Bash",
				ToolInput: json.RawMessage(testBashInput), Deadline: clock.Now().Add(5 * time.Minute),
			})
			return record.ID, err
		}, StateWaiting, AuditRequested},
		{"answer", func(store *Store, _ *testClock) (string, error) {
			record, err := store.Answer(testID(1), "agt-a", true, ViaCLI)
			return record.ID, err
		}, StateAllowed, AuditAllowed},
		{"close", func(store *Store, _ *testClock) (string, error) {
			record, err := store.Close(testID(1), CloseReasonCanceled)
			return record.ID, err
		}, StateClosed, AuditClosed},
		{"close past deadline", func(store *Store, clock *testClock) (string, error) {
			clock.Advance(10 * time.Minute)
			record, err := store.Close(testID(1), CloseReasonCanceled)
			return record.ID, err
		}, StateExpired, AuditExpired},
		{"terminal close", func(store *Store, _ *testClock) (string, error) {
			closed, err := store.CloseAnsweredInTerminal("sess-1", "Bash", json.RawMessage(testBashInput))
			if !closed {
				return "", err
			}
			return testID(1), err
		}, StateClosed, AuditClosed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store, clock := newTestStore(t)
			createTestRecord(t, store, clock, 1, "agt-a", "sess-1", "Bash", testBashInput)
			orphans, kept := plantOrphans(t, store)
			var removed []string
			recording := *store
			recording.removeTemp = func(path string) error {
				removed = append(removed, path)
				return os.Remove(path)
			}

			id, err := tc.write(&recording, clock)
			if err != nil || id == "" {
				t.Fatalf("write = %q, %v; want it to commit", id, err)
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
			record, ok, err := store.Get(id)
			if err != nil || !ok || record.State != tc.state {
				t.Fatalf("record = %+v, %v, %v; want %s", record, ok, err, tc.state)
			}
			if got := auditLinesFor(t, store, tc.event, id); got != 1 {
				t.Fatalf("%s lines for %s = %d, want 1", tc.event, id, got)
			}
			if _, err := os.Stat(store.Path() + ".flock"); err != nil {
				t.Fatalf("lock file: %v", err)
			}
		})
	}
}

// TestStoreTempCleanupFailureDoesNotFailTheWrite holds that the cleanup is best
// effort: a temp file that cannot be removed stays, the write commits with its
// audit line, and a later write whose removal works clears it.
func TestStoreTempCleanupFailureDoesNotFailTheWrite(t *testing.T) {
	t.Parallel()

	store, clock := newTestStore(t)
	createTestRecord(t, store, clock, 1, "agt-a", "sess-1", "Bash", testBashInput)
	orphans, _ := plantOrphans(t, store)
	refusing := *store
	refusing.removeTemp = func(path string) error {
		return &fs.PathError{Op: "remove", Path: path, Err: syscall.EPERM}
	}

	record, err := refusing.Answer(testID(1), "agt-a", false, ViaCLI)
	if err != nil || record.State != StateDenied {
		t.Fatalf("answer = %+v, %v; want it denied despite the cleanup failure", record, err)
	}
	if got := strings.Join(auditEvents(readAudit(t, store.AuditPath())), ","); got != AuditRequested+","+AuditDenied {
		t.Fatalf("audit events = %s, want the denied line and nothing after it", got)
	}
	for _, path := range orphans {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("orphan %s: %v; want it left for a later write", path, err)
		}
	}

	createTestRecord(t, store, clock, 2, "agt-a", "sess-2", "Bash", testBashInput)
	if got := tempEntries(t, store); !slices.Equal(got, []string{tempPrefix + "dir"}) {
		t.Fatalf("temp entries after a later write = %v, want only the directory", got)
	}
}
