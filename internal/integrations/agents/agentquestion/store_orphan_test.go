package agentquestion

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
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
		filepath.Join(dir, "notes.txt"):          "unrelated\n",
		filepath.Join(dir, "questions.json.bak"): "unrelated\n",
		filepath.Join(dir, ".messages.tmp-1"):    "another store's temp name\n",
		filepath.Join(dir, ".requests.tmp-1"):    "another store's temp name\n",
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
// still commits its own record.
func TestStoreWriteRemovesTempFilesADeadWriteLeft(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		write func(*Store, *testClock) (string, error)
		state State
	}{
		{"create", func(store *Store, clock *testClock) (string, error) {
			record := createTestRecord(t, store, clock, 2, "agt-a")
			return record.ID, nil
		}, StateWaiting},
		{"answer", func(store *Store, _ *testClock) (string, error) {
			record, err := store.Answer(testID(1), "agt-a", testAnswers)
			return record.ID, err
		}, StateAnswered},
		{"close agent", func(store *Store, _ *testClock) (string, error) {
			closed, err := store.CloseAgent("agt-a")
			if closed != 1 {
				return "", err
			}
			return testID(1), err
		}, StateClosed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store, clock := newTestStore(t)
			createTestRecord(t, store, clock, 1, "agt-a")
			lockBefore, err := os.ReadFile(store.Path() + ".flock")
			if err != nil {
				t.Fatalf("lock file: %v", err)
			}
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
			if data, err := os.ReadFile(store.Path() + ".flock"); err != nil || !bytes.Equal(data, lockBefore) {
				t.Fatalf("lock file = %q, %v; want it untouched", data, err)
			}
			record, ok, err := store.Get(id)
			if err != nil || !ok || record.State != tc.state {
				t.Fatalf("record = %+v, %v, %v; want %s", record, ok, err, tc.state)
			}
		})
	}
}

// TestStoreTempCleanupFailureDoesNotFailTheWrite holds that the cleanup is best
// effort: a temp file that cannot be removed stays, the write commits, and a
// later write whose removal works clears it.
func TestStoreTempCleanupFailureDoesNotFailTheWrite(t *testing.T) {
	t.Parallel()

	store, clock := newTestStore(t)
	createTestRecord(t, store, clock, 1, "agt-a")
	orphans, _ := plantOrphans(t, store)
	refusing := *store
	refusing.removeTemp = func(path string) error {
		return &fs.PathError{Op: "remove", Path: path, Err: syscall.EPERM}
	}

	record, err := refusing.Answer(testID(1), "agt-a", testAnswers)
	if err != nil || record.State != StateAnswered {
		t.Fatalf("answer = %+v, %v; want it answered despite the cleanup failure", record, err)
	}
	stored, ok, err := store.Get(testID(1))
	if err != nil || !ok || stored.State != StateAnswered || len(stored.Answers) != len(testAnswers) {
		t.Fatalf("stored = %+v, %v, %v; want the answered record", stored, ok, err)
	}
	for _, path := range orphans {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("orphan %s: %v; want it left for a later write", path, err)
		}
	}

	createTestRecord(t, store, clock, 2, "agt-a")
	if got := tempEntries(t, store); !slices.Equal(got, []string{tempPrefix + "dir"}) {
		t.Fatalf("temp entries after a later write = %v, want only the directory", got)
	}
}
