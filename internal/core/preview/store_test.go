package preview

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/es5h/projmux/internal/config"
)

func TestNewDefaultStoreUsesStatePreviewFile(t *testing.T) {
	t.Parallel()

	paths := config.DefaultPaths("/tmp/config-home", "/tmp/state-home")
	store := NewDefaultStore(paths)

	if got, want := store.Path(), paths.PreviewStateFile(); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

func TestStoreReadMissingFile(t *testing.T) {
	t.Parallel()

	store := NewStore(filepath.Join(t.TempDir(), "preview-state"))

	windowIndex, ok, err := store.ReadWindowIndex("app")
	if err != nil {
		t.Fatalf("ReadWindowIndex() error = %v", err)
	}
	if ok {
		t.Fatalf("ReadWindowIndex() ok = %v, want false", ok)
	}
	if windowIndex != "" {
		t.Fatalf("ReadWindowIndex() = %q, want empty", windowIndex)
	}

	paneIndex, ok, err := store.ReadPaneIndex("app")
	if err != nil {
		t.Fatalf("ReadPaneIndex() error = %v", err)
	}
	if ok {
		t.Fatalf("ReadPaneIndex() ok = %v, want false", ok)
	}
	if paneIndex != "" {
		t.Fatalf("ReadPaneIndex() = %q, want empty", paneIndex)
	}
}

func TestStoreWriteSelectionAddsAndUpdatesSessionRow(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "preview-state")
	store := NewStore(path)

	if err := store.WriteSelection("app", "1", "2"); err != nil {
		t.Fatalf("WriteSelection() first error = %v", err)
	}
	if err := store.WriteSelection("lib", "3", "4"); err != nil {
		t.Fatalf("WriteSelection() second error = %v", err)
	}
	if err := store.WriteSelection("app", "5", "6"); err != nil {
		t.Fatalf("WriteSelection() update error = %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	want := "lib\t3\t4\napp\t5\t6\n"
	if string(raw) != want {
		t.Fatalf("file contents = %q, want %q", string(raw), want)
	}
}

func TestStoreReadWindowIndexReturnsStoredValue(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "preview-state")
	content := "app\t7\t8\nlib\t2\t3\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	windowIndex, ok, err := NewStore(path).ReadWindowIndex("app")
	if err != nil {
		t.Fatalf("ReadWindowIndex() error = %v", err)
	}
	if !ok {
		t.Fatalf("ReadWindowIndex() ok = %v, want true", ok)
	}
	if windowIndex != "7" {
		t.Fatalf("ReadWindowIndex() = %q, want %q", windowIndex, "7")
	}
}

func TestStoreReadPaneIndexFallsBackToWindowIndex(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "preview-state")
	content := "app\t7\nlib\t2\t3\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	paneIndex, ok, err := NewStore(path).ReadPaneIndex("app")
	if err != nil {
		t.Fatalf("ReadPaneIndex() error = %v", err)
	}
	if !ok {
		t.Fatalf("ReadPaneIndex() ok = %v, want true", ok)
	}
	if paneIndex != "7" {
		t.Fatalf("ReadPaneIndex() = %q, want %q", paneIndex, "7")
	}
}

func TestStoreSkipsMalformedRowsAndPreservesOthersOnWrite(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "preview-state")
	content := "bad-row\n\t1\t2\nlib\t3\t4\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := NewStore(path)
	if err := store.WriteSelection("app", "5", "6"); err != nil {
		t.Fatalf("WriteSelection() error = %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	want := "lib\t3\t4\napp\t5\t6\n"
	if string(raw) != want {
		t.Fatalf("file contents = %q, want %q", string(raw), want)
	}
}

func TestStoreRejectsInvalidSessionNames(t *testing.T) {
	t.Parallel()

	store := NewStore(filepath.Join(t.TempDir(), "preview-state"))

	if _, _, err := store.ReadWindowIndex(""); !errors.Is(err, ErrInvalidSessionName) {
		t.Fatalf("ReadWindowIndex() error = %v, want %v", err, ErrInvalidSessionName)
	}
	if _, _, err := store.ReadPaneIndex("bad\tname"); !errors.Is(err, ErrInvalidSessionName) {
		t.Fatalf("ReadPaneIndex() error = %v, want %v", err, ErrInvalidSessionName)
	}
	if err := store.WriteSelection(" ", "1", "2"); !errors.Is(err, ErrInvalidSessionName) {
		t.Fatalf("WriteSelection() session error = %v, want %v", err, ErrInvalidSessionName)
	}
}

func TestStoreRejectsInvalidCellValues(t *testing.T) {
	t.Parallel()

	store := NewStore(filepath.Join(t.TempDir(), "preview-state"))

	if err := store.WriteSelection("app", "1\t2", "3"); !errors.Is(err, ErrInvalidSessionName) {
		t.Fatalf("WriteSelection() window error = %v, want %v", err, ErrInvalidSessionName)
	}
	if err := store.WriteSelection("app", "1", "3\n4"); !errors.Is(err, ErrInvalidSessionName) {
		t.Fatalf("WriteSelection() pane error = %v, want %v", err, ErrInvalidSessionName)
	}
}

func TestStoreReadSelectionReturnsStoredRow(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "preview-state")
	content := "app\t7\t8\nlib\t2\t3\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	selection, ok, err := NewStore(path).ReadSelection("app")
	if err != nil {
		t.Fatalf("ReadSelection() error = %v", err)
	}
	if !ok {
		t.Fatal("ReadSelection() ok = false, want true")
	}
	if selection != (Selection{SessionName: "app", WindowIndex: "7", PaneIndex: "8"}) {
		t.Fatalf("ReadSelection() = %+v, want app/7/8", selection)
	}
}

func TestStoreCyclePaneSelectionReadsFromStoreAndWritesBack(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "preview-state")
	if err := os.WriteFile(path, []byte("app\t1\t0\nlib\t3\t4\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := NewStore(path)
	result, err := store.CyclePaneSelection("app", nil, []Pane{
		{WindowIndex: "1", Index: "0"},
		{WindowIndex: "1", Index: "1"},
	}, DirectionNext)
	if err != nil {
		t.Fatalf("CyclePaneSelection() error = %v", err)
	}
	if !result.Selected {
		t.Fatal("CyclePaneSelection() Selected = false, want true")
	}
	if !result.Changed {
		t.Fatal("CyclePaneSelection() Changed = false, want true")
	}
	if result.Cursor != (Cursor{WindowIndex: "1", PaneIndex: "1"}) {
		t.Fatalf("CyclePaneSelection() cursor = %+v, want window 1 pane 1", result.Cursor)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got, want := string(raw), "lib\t3\t4\napp\t1\t1\n"; got != want {
		t.Fatalf("file contents = %q, want %q", got, want)
	}
}

func TestStoreCycleWindowSelectionWritesInitialSelectionWhenRowIsMissing(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "preview-state")
	if err := os.WriteFile(path, []byte("lib\t3\t4\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := NewStore(path)
	result, err := store.CycleWindowSelection("app", []Window{
		{Index: "1", Active: true},
		{Index: "2"},
	}, []Pane{
		{WindowIndex: "1", Index: "0", Active: true},
		{WindowIndex: "2", Index: "5"},
	}, DirectionNext)
	if err != nil {
		t.Fatalf("CycleWindowSelection() error = %v", err)
	}
	if !result.Selected {
		t.Fatal("CycleWindowSelection() Selected = false, want true")
	}
	if !result.Changed {
		t.Fatal("CycleWindowSelection() Changed = false, want true")
	}
	if result.Cursor != (Cursor{WindowIndex: "2", PaneIndex: "5"}) {
		t.Fatalf("CycleWindowSelection() cursor = %+v, want window 2 pane 5", result.Cursor)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got, want := string(raw), "lib\t3\t4\napp\t2\t5\n"; got != want {
		t.Fatalf("file contents = %q, want %q", got, want)
	}
}

func TestStoreCycleWindowSelectionDoesNotRewriteWhenSelectionIsUnchanged(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "preview-state")
	content := "app\t2\t5\nlib\t3\t4\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := NewStore(path)
	result, err := store.CycleWindowSelection("app", []Window{
		{Index: "2"},
	}, []Pane{
		{WindowIndex: "2", Index: "5", Active: true},
	}, DirectionNext)
	if err != nil {
		t.Fatalf("CycleWindowSelection() error = %v", err)
	}
	if !result.Selected {
		t.Fatal("CycleWindowSelection() Selected = false, want true")
	}
	if result.Changed {
		t.Fatal("CycleWindowSelection() Changed = true, want false")
	}
	if result.Cursor != (Cursor{WindowIndex: "2", PaneIndex: "5"}) {
		t.Fatalf("CycleWindowSelection() cursor = %+v, want window 2 pane 5", result.Cursor)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got := string(raw); got != content {
		t.Fatalf("file contents = %q, want unchanged %q", got, content)
	}
}

func TestStoreCyclePaneSelectionNoOpWhenNoTargetExists(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "preview-state")
	content := "app\t1\t0\nlib\t3\t4\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := NewStore(path)
	result, err := store.CyclePaneSelection("app", nil, nil, DirectionNext)
	if err != nil {
		t.Fatalf("CyclePaneSelection() error = %v", err)
	}
	if result.Selected {
		t.Fatal("CyclePaneSelection() Selected = true, want false")
	}
	if result.Changed {
		t.Fatal("CyclePaneSelection() Changed = true, want false")
	}
	if result.Cursor != (Cursor{}) {
		t.Fatalf("CyclePaneSelection() cursor = %+v, want empty", result.Cursor)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got := string(raw); got != content {
		t.Fatalf("file contents = %q, want unchanged %q", got, content)
	}
}
