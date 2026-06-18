package recentwindows

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreMissingFileLoadsEmptyState(t *testing.T) {
	t.Parallel()

	store := NewStore(filepath.Join(t.TempDir(), "recent.json"))
	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	if state.Version != Version {
		t.Fatalf("Version = %d, want %d", state.Version, Version)
	}
	if len(state.Entries) != 0 {
		t.Fatalf("entries = %+v, want empty", state.Entries)
	}
}

func TestStoreCorruptFileBacksUpAndStartsEmpty(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "recent.json")
	if err := os.WriteFile(path, []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("write corrupt state: %v", err)
	}
	store := NewStore(path)
	store.SetClock(func() time.Time { return time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC) })

	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	if len(state.Entries) != 0 {
		t.Fatalf("entries = %+v, want empty", state.Entries)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("original path stat error = %v, want not exist after backup", err)
	}
	backup := path + ".corrupt.20260618T010203Z"
	if data, err := os.ReadFile(backup); err != nil {
		t.Fatalf("read backup: %v", err)
	} else if string(data) != "{not-json" {
		t.Fatalf("backup data = %q, want corrupt payload", data)
	}
}

func TestStoreRecordRoundTrip(t *testing.T) {
	t.Parallel()

	store := NewStore(filepath.Join(t.TempDir(), "recent.json"))
	if _, err := store.Record(snap("/tmp/tmux", "alpha", "@1", "main", time.Unix(10, 0)), 0); err != nil {
		t.Fatalf("Record error = %v", err)
	}

	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var raw State
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	if raw.Version != Version || len(raw.Entries) != 1 {
		t.Fatalf("raw state = %+v, want version and one entry", raw)
	}

	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	if got := state.Entries[0]; got.Socket != "/tmp/tmux" || got.Session != "alpha" || got.WindowID != "@1" {
		t.Fatalf("entry = %+v, want recorded key", got)
	}
}

func TestStoreCandidatesPruneGoneWindowsOnDisk(t *testing.T) {
	t.Parallel()

	store := NewStore(filepath.Join(t.TempDir(), "recent.json"))
	for _, snapshot := range []Snapshot{
		snap("/tmp/tmux", "gone", "@1", "gone", time.Unix(1, 0)),
		snap("/tmp/tmux", "alive", "@2", "alive", time.Unix(2, 0)),
	} {
		if _, err := store.Record(snapshot, 0); err != nil {
			t.Fatalf("Record %s: %v", snapshot.Session, err)
		}
	}

	candidates, err := store.Candidates(WindowKey{}, []LiveWindow{{Socket: "/tmp/tmux", Session: "alive", WindowID: "@2"}}, 0)
	if err != nil {
		t.Fatalf("Candidates error = %v", err)
	}
	if got, want := len(candidates), 1; got != want {
		t.Fatalf("candidates len = %d, want %d", got, want)
	}
	if candidates[0].Session != "alive" {
		t.Fatalf("candidate = %+v, want alive", candidates[0])
	}

	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load after prune error = %v", err)
	}
	if got, want := len(state.Entries), 1; got != want {
		t.Fatalf("stored len = %d, want %d", got, want)
	}
	if state.Entries[0].Session != "alive" {
		t.Fatalf("stored entries = %+v, want alive only", state.Entries)
	}
}

func TestPathForSocketScopesByServerSocket(t *testing.T) {
	t.Parallel()

	path := PathForSocket("/state/projmux", "/tmp/tmux-1000/projmux")
	want := filepath.Join("/state/projmux", "recent-windows", "%2Ftmp%2Ftmux-1000%2Fprojmux.json")
	if path != want {
		t.Fatalf("PathForSocket = %q, want %q", path, want)
	}
	if got, want := PathForSocket("/state/projmux", ""), filepath.Join("/state/projmux", "recent-windows", "default.json"); got != want {
		t.Fatalf("default PathForSocket = %q, want %q", got, want)
	}
}
