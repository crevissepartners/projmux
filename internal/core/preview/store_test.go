package preview

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

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
