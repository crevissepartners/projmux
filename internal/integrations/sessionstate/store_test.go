package sessionstate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	want := sampleSnapshot()

	if err := store.Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := store.Load(want.Session)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}

	data, err := os.ReadFile(mustPath(t, store, want.Session))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal(raw) error = %v", err)
	}
	if raw["default_cwd"] != want.DefaultCWD {
		t.Fatalf("default_cwd = %#v, want %q", raw["default_cwd"], want.DefaultCWD)
	}
	windows, ok := raw["windows"].([]any)
	if !ok || len(windows) != 1 {
		t.Fatalf("windows = %#v, want one window", raw["windows"])
	}
	window, ok := windows[0].(map[string]any)
	if !ok {
		t.Fatalf("window = %#v, want object", windows[0])
	}
	if window["layout"] != want.Windows[0].Layout {
		t.Fatalf("layout = %#v, want %q", window["layout"], want.Windows[0].Layout)
	}
	if window["active_pane_index"] != float64(want.Windows[0].ActivePaneIndex) {
		t.Fatalf("active_pane_index = %#v, want %d", window["active_pane_index"], want.Windows[0].ActivePaneIndex)
	}
}

func TestStoreDefaultPathFromEnvUsesXDGStateHome(t *testing.T) {
	xdgState := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", xdgState)

	store, err := NewDefaultStoreFromEnv()
	if err != nil {
		t.Fatalf("NewDefaultStoreFromEnv() error = %v", err)
	}

	path, err := store.Path("home")
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}
	want := filepath.Join(xdgState, "projmux", "sessions", "home.json")
	if path != want {
		t.Fatalf("Path() = %q, want %q", path, want)
	}
}

func TestStoreDefaultPathFromEnvFallsBackToHomeState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")

	store, err := NewDefaultStoreFromEnv()
	if err != nil {
		t.Fatalf("NewDefaultStoreFromEnv() error = %v", err)
	}

	path, err := store.Path("home")
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}
	want := filepath.Join(home, ".local", "state", "projmux", "sessions", "home.json")
	if path != want {
		t.Fatalf("Path() = %q, want %q", path, want)
	}
}

func TestLoadMissingFile(t *testing.T) {
	t.Parallel()

	_, err := NewStore(t.TempDir()).Load("missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load() error = %v, want %v", err, ErrNotFound)
	}
}

func TestLoadLegacySnapshotWithoutSourceDefaultsToAutosaveLabel(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	snap := sampleSnapshot()
	writeSnapshotJSON(t, store, snap.Session, map[string]any{
		"version":     snap.Version,
		"session":     snap.Session,
		"default_cwd": snap.DefaultCWD,
		"saved_at":    snap.SavedAt,
		"windows":     snap.Windows,
	})

	got, err := store.Load(snap.Session)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Source != "" || got.SourceLabel() != SourceAutosave {
		t.Fatalf("source = %q label %q, want legacy empty source with autosave label", got.Source, got.SourceLabel())
	}
}

func TestLoadLegacySnapshotWithoutPaneTitle(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	snap := sampleSnapshot()
	writeSnapshotJSON(t, store, snap.Session, map[string]any{
		"version":     snap.Version,
		"session":     snap.Session,
		"default_cwd": snap.DefaultCWD,
		"saved_at":    snap.SavedAt,
		"windows": []any{
			map[string]any{
				"index":             0,
				"name":              "projmux",
				"active_pane_index": 0,
				"panes": []any{
					map[string]any{
						"index":  0,
						"cwd":    "/home/tester/source/repos/projmux",
						"recipe": map[string]any{"kind": "shell"},
					},
				},
			},
		},
	})

	got, err := store.Load(snap.Session)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(got.Windows) != 1 || len(got.Windows[0].Panes) != 1 {
		t.Fatalf("Load() = %#v, want one pane", got)
	}
	if got.Windows[0].Panes[0].Title != "" {
		t.Fatalf("legacy pane title = %q, want empty", got.Windows[0].Panes[0].Title)
	}
}

func TestStoreSummaryCountsWindowsAndPanes(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	snap := sampleSnapshot()
	snap.Windows = append(snap.Windows, Window{
		Index:           1,
		Name:            "logs",
		ActivePaneIndex: 0,
		Panes: []Pane{
			{Index: 0, CWD: "/tmp", Recipe: ShellRecipe()},
			{Index: 1, CWD: "/tmp", Recipe: ShellRecipe()},
		},
	})
	if err := store.Save(snap); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := store.Summary(snap.Session)
	if err != nil {
		t.Fatalf("Summary() error = %v", err)
	}
	if got.Session != snap.Session || got.WindowCount != 2 || got.PaneCount != 4 || !got.SavedAt.Equal(snap.SavedAt) {
		t.Fatalf("Summary() = %#v, want session/window/pane/saved_at summary", got)
	}
}

func TestStoreDeleteRemovesSnapshotAndIgnoresMissing(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	snap := sampleSnapshot()
	if err := store.Save(snap); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if err := store.Delete(snap.Session); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Load(snap.Session); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load() after Delete error = %v, want %v", err, ErrNotFound)
	}
	if err := store.Delete(snap.Session); err != nil {
		t.Fatalf("Delete() missing error = %v, want nil", err)
	}
}

func TestLoadMalformedJSON(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	path := mustPath(t, store, "home")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version":`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := store.Load("home")
	if !errors.Is(err, ErrMalformedJSON) {
		t.Fatalf("Load() error = %v, want %v", err, ErrMalformedJSON)
	}
}

func TestLoadMissingVersion(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	writeSnapshotJSON(t, store, "home", map[string]any{
		"session":  "home",
		"saved_at": "2026-05-11T12:34:56Z",
		"windows":  []any{},
	})

	_, err := store.Load("home")
	if !errors.Is(err, ErrMissingVersion) {
		t.Fatalf("Load() error = %v, want %v", err, ErrMissingVersion)
	}
}

func TestLoadUnsupportedVersion(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	writeSnapshotJSON(t, store, "home", map[string]any{
		"version":  2,
		"session":  "home",
		"saved_at": "2026-05-11T12:34:56Z",
		"windows":  []any{},
	})

	_, err := store.Load("home")
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("Load() error = %v, want %v", err, ErrUnsupportedVersion)
	}
}

func TestLoadRejectsMissingPaneCWD(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	writeSnapshotJSON(t, store, "home", map[string]any{
		"version":  Version,
		"session":  "home",
		"saved_at": "2026-05-11T12:34:56Z",
		"windows": []any{
			map[string]any{
				"index": 0,
				"name":  "main",
				"panes": []any{
					map[string]any{
						"index":  0,
						"recipe": map[string]any{"kind": "shell"},
					},
				},
			},
		},
	})

	_, err := store.Load("home")
	if !errors.Is(err, ErrInvalidSnapshot) || !strings.Contains(err.Error(), "cwd is required") {
		t.Fatalf("Load() error = %v, want invalid snapshot cwd error", err)
	}
}

func TestLoadRejectsInvalidDefaultCWD(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	snap := sampleSnapshot()
	snap.DefaultCWD = "relative"
	writeSnapshotJSON(t, store, "home", snap)

	_, err := store.Load("home")
	if !errors.Is(err, ErrInvalidSnapshot) || !strings.Contains(err.Error(), "default_cwd") {
		t.Fatalf("Load() error = %v, want invalid default_cwd error", err)
	}
}

func TestLoadRejectsInvalidActivePaneIndex(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	snap := sampleSnapshot()
	snap.Windows[0].ActivePaneIndex = 9
	writeSnapshotJSON(t, store, "home", snap)

	_, err := store.Load("home")
	if !errors.Is(err, ErrInvalidSnapshot) || !strings.Contains(err.Error(), "active_pane_index") {
		t.Fatalf("Load() error = %v, want invalid active_pane_index error", err)
	}
}

func TestSaveRejectsUnsafeSessionName(t *testing.T) {
	t.Parallel()

	snap := sampleSnapshot()
	snap.Session = "../home"
	if err := NewStore(t.TempDir()).Save(snap); !errors.Is(err, ErrInvalidSessionName) {
		t.Fatalf("Save() error = %v, want %v", err, ErrInvalidSessionName)
	}
}

func TestSaveUsesFinalPathAndCleansTempFile(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	snap := sampleSnapshot()
	if err := store.Save(snap); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	path := mustPath(t, store, snap.Session)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Stat(final) error = %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(store.Dir, ".*.tmp-*"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temp files left behind = %v, want none", matches)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != fileMode {
		t.Fatalf("mode = %v, want %v", got, os.FileMode(fileMode))
	}
}

func TestRecipeJSONForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   Recipe
		want string
	}{
		{
			name: "shell",
			in:   ShellRecipe(),
			want: `{"kind":"shell"}`,
		},
		{
			name: "agent",
			in:   AgentRecipe("claude", "abcdef-1234", "keybinding in-app"),
			want: `{"kind":"agent","agent":"claude","resume_id":"abcdef-1234","topic":"keybinding in-app"}`,
		},
		{
			name: "startup",
			in:   StartupRecipe("npm run dev"),
			want: `{"kind":"startup","command":"npm run dev"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := json.Marshal(tt.in)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("Marshal() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestLoadRejectsShellRecipeWithReplayCommand(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	snap := sampleSnapshot()
	snap.Windows[0].Panes = []Pane{
		{
			Index: 0,
			CWD:   "/home/tester/source/repos/projmux",
			Recipe: Recipe{
				Kind:    RecipeKindShell,
				Command: "rm -rf important",
			},
		},
	}
	snap.Windows[0].ActivePaneIndex = 0
	writeSnapshotJSON(t, store, "home", snap)

	_, err := store.Load("home")
	if !errors.Is(err, ErrInvalidSnapshot) || !strings.Contains(err.Error(), "shell recipe cannot include replay metadata") {
		t.Fatalf("Load() error = %v, want invalid shell replay metadata error", err)
	}
}

func TestLoadRejectsStartupRecipeWithoutCommand(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	snap := sampleSnapshot()
	snap.Windows[0].Panes = []Pane{
		{
			Index:  0,
			CWD:    "/home/tester/source/repos/projmux",
			Recipe: Recipe{Kind: RecipeKindStartup},
		},
	}
	snap.Windows[0].ActivePaneIndex = 0
	writeSnapshotJSON(t, store, "home", snap)

	_, err := store.Load("home")
	if !errors.Is(err, ErrInvalidSnapshot) || !strings.Contains(err.Error(), "startup recipe requires command") {
		t.Fatalf("Load() error = %v, want invalid startup command error", err)
	}
}

func sampleSnapshot() Snapshot {
	return Snapshot{
		Version:    Version,
		Session:    "home",
		DefaultCWD: "/home/tester/source/repos/projmux",
		SavedAt:    time.Date(2026, 5, 11, 12, 34, 56, 0, time.UTC),
		Windows: []Window{
			{
				Index:           0,
				Name:            "projmux",
				Layout:          "d3a9,120x36,0,0{60x36,0,0,1,59x36,61,0,2}",
				ActivePaneIndex: 1,
				Panes: []Pane{
					{
						Index:  0,
						CWD:    "/home/tester/source/repos/projmux",
						Recipe: ShellRecipe(),
					},
					{
						Index:  1,
						Title:  "agent task",
						CWD:    "/home/tester/source/repos/projmux",
						Recipe: AgentRecipe("claude", "abcdef-1234", "keybinding in-app"),
					},
				},
			},
		},
	}
}

func mustPath(t *testing.T, store Store, session string) string {
	t.Helper()

	path, err := store.Path(session)
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}
	return path
}

func writeSnapshotJSON(t *testing.T, store Store, session string, body any) {
	t.Helper()

	path := mustPath(t, store, session)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
