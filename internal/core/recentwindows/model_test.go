package recentwindows

import (
	"errors"
	"testing"
	"time"
)

func TestRecordMaintainsMRUNewestFirstAndDedupe(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 18, 1, 2, 3, 0, time.UTC)
	state := NewState(nil)
	var err error
	state, err = state.Record(snap("/tmp/tmux", "alpha", "@1", "main", now), 0)
	if err != nil {
		t.Fatalf("record alpha: %v", err)
	}
	state, err = state.Record(snap("/tmp/tmux", "beta", "@2", "tests", now.Add(time.Second)), 0)
	if err != nil {
		t.Fatalf("record beta: %v", err)
	}
	state, err = state.Record(snap("/tmp/tmux", "alpha", "@1", "main-new", now.Add(2*time.Second)), 0)
	if err != nil {
		t.Fatalf("record alpha again: %v", err)
	}

	if got, want := len(state.Entries), 2; got != want {
		t.Fatalf("len = %d, want %d", got, want)
	}
	if got := state.Entries[0]; got.Session != "alpha" || got.WindowName != "main-new" {
		t.Fatalf("head = %+v, want updated alpha", got)
	}
	if got := state.Entries[1]; got.Session != "beta" {
		t.Fatalf("second = %+v, want beta", got)
	}
}

func TestRecordAppliesQueueLimit(t *testing.T) {
	t.Parallel()

	state := NewState(nil)
	for i, session := range []string{"one", "two", "three"} {
		var err error
		state, err = state.Record(snap("", session, "@"+session, session, time.Unix(int64(i), 0)), 2)
		if err != nil {
			t.Fatalf("record %s: %v", session, err)
		}
	}

	if got, want := len(state.Entries), 2; got != want {
		t.Fatalf("len = %d, want %d", got, want)
	}
	if state.Entries[0].Session != "three" || state.Entries[1].Session != "two" {
		t.Fatalf("entries = %+v, want three then two", state.Entries)
	}
}

func TestRecordRequiresWindowKey(t *testing.T) {
	t.Parallel()

	_, err := NewState(nil).Record(Snapshot{Session: "alpha"}, 0)
	if !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("Record error = %v, want ErrInvalidSnapshot", err)
	}
}

func TestCandidatesExcludeCurrentAndRetainCrossSession(t *testing.T) {
	t.Parallel()

	state := NewState([]Snapshot{
		snap("/tmp/tmux", "current", "@1", "current-window", time.Unix(3, 0)),
		snap("/tmp/tmux", "other-project", "@2", "agent", time.Unix(2, 0)),
		snap("/tmp/tmux", "third-project", "@3", "shell", time.Unix(1, 0)),
	})
	live := []LiveWindow{
		{Socket: "/tmp/tmux", Session: "current", WindowID: "@1"},
		{Socket: "/tmp/tmux", Session: "other-project", WindowID: "@2"},
		{Socket: "/tmp/tmux", Session: "third-project", WindowID: "@3"},
	}

	candidates, pruned := state.Candidates(WindowKey{Socket: "/tmp/tmux", Session: "current", WindowID: "@1"}, live, 0)
	if got, want := len(candidates), 2; got != want {
		t.Fatalf("candidates len = %d, want %d", got, want)
	}
	if candidates[0].Session != "other-project" || candidates[1].Session != "third-project" {
		t.Fatalf("candidates = %+v, want cross-session entries retained", candidates)
	}
	if got, want := len(pruned.Entries), 3; got != want {
		t.Fatalf("pruned len = %d, want %d", got, want)
	}
}

func TestCandidatesPruneGoneWindowsAgainstSameSocketInventory(t *testing.T) {
	t.Parallel()

	state := NewState([]Snapshot{
		snap("/tmp/tmux-a", "alpha", "@1", "alive", time.Unix(3, 0)),
		snap("/tmp/tmux-a", "alpha", "@2", "gone", time.Unix(2, 0)),
		snap("/tmp/tmux-b", "alpha", "@1", "other-socket", time.Unix(1, 0)),
	})
	live := []LiveWindow{
		{Socket: "/tmp/tmux-a", Session: "alpha", WindowID: "@1"},
	}

	candidates, pruned := state.Candidates(WindowKey{}, live, 0)
	if got, want := len(candidates), 1; got != want {
		t.Fatalf("candidates len = %d, want %d", got, want)
	}
	if candidates[0].WindowName != "alive" {
		t.Fatalf("candidate = %+v, want alive", candidates[0])
	}
	if got, want := len(pruned.Entries), 1; got != want {
		t.Fatalf("pruned len = %d, want %d", got, want)
	}
	if pruned.Entries[0].WindowName != "alive" {
		t.Fatalf("pruned entries = %+v, want only alive", pruned.Entries)
	}
}

func TestBuildLabelPrefersNamesAndCommandOverDebugIDs(t *testing.T) {
	t.Parallel()

	label := BuildLabel(Snapshot{
		Session:       "repos-projmux",
		WindowID:      "@6",
		WindowName:    "projmux",
		Project:       "Projmux",
		LastPaneID:    "%54",
		LastPaneTitle: "codex-review",
		LastCommand:   "codex",
	})

	if got, want := label.Primary, "projmux"; got != want {
		t.Fatalf("Primary = %q, want %q", got, want)
	}
	if label.Primary == "@6" || label.Primary == "%54" {
		t.Fatalf("Primary = %q, should not be debug id", label.Primary)
	}
	if label.Secondary == "" {
		t.Fatal("Secondary should include descriptive pane/project context")
	}
	if got, want := label.Debug, "repos-projmux win @6 pane %54"; got != want {
		t.Fatalf("Debug = %q, want %q", got, want)
	}
}

func TestBuildLabelUsesDebugOnlyAsFallback(t *testing.T) {
	t.Parallel()

	label := BuildLabel(Snapshot{Session: "repos-projmux", WindowID: "@6", LastPaneID: "%54"})
	if got, want := label.Primary, "repos-projmux"; got != want {
		t.Fatalf("Primary = %q, want session before debug id", got)
	}

	label = BuildLabel(Snapshot{WindowID: "@6", LastPaneID: "%54"})
	if got, want := label.Primary, "win @6 pane %54"; got != want {
		t.Fatalf("Primary = %q, want debug fallback %q", got, want)
	}
}

func snap(socket, session, windowID, name string, at time.Time) Snapshot {
	return Snapshot{
		Socket:        socket,
		Session:       session,
		WindowID:      windowID,
		WindowName:    name,
		LastPaneTitle: "pane-" + name,
		LastCommand:   "zsh",
		LastFocusedAt: at,
	}
}
