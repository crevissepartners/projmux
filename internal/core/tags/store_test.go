package tags

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestStoreListMissingFile(t *testing.T) {
	t.Parallel()

	store := NewStore(filepath.Join(t.TempDir(), "tags"))

	got, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List() = %v, want empty", got)
	}
}

func TestStoreToggleAddsInFirstSeenOrder(t *testing.T) {
	t.Parallel()

	store := NewStore(filepath.Join(t.TempDir(), "tags"))

	tagged, err := store.Toggle("session-a")
	if err != nil {
		t.Fatalf("Toggle() add error = %v", err)
	}
	if !tagged {
		t.Fatalf("Toggle() tagged = %v, want true", tagged)
	}

	tagged, err = store.Toggle("session-b")
	if err != nil {
		t.Fatalf("Toggle() second add error = %v", err)
	}
	if !tagged {
		t.Fatalf("Toggle() tagged = %v, want true", tagged)
	}

	got, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	want := []string{"session-a", "session-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %v, want %v", got, want)
	}
}

func TestStoreToggleRemovesAllMatchingEntries(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "tags")
	if err := os.WriteFile(path, []byte("session-a\nsession-b\nsession-a\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := NewStore(path)
	tagged, err := store.Toggle("session-a")
	if err != nil {
		t.Fatalf("Toggle() remove error = %v", err)
	}
	if tagged {
		t.Fatalf("Toggle() tagged = %v, want false", tagged)
	}

	got, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	want := []string{"session-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %v, want %v", got, want)
	}
}

func TestStoreListDeduplicatesKeepingFirstSeenOrder(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "tags")
	if err := os.WriteFile(path, []byte("session-a\nsession-b\nsession-a\nsession-c\nsession-b\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := NewStore(path)
	got, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	want := []string{"session-a", "session-b", "session-c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %v, want %v", got, want)
	}
}

func TestStoreClearLeavesEmptyInspectableFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "tags")
	store := NewStore(path)

	if _, err := store.Toggle("session-a"); err != nil {
		t.Fatalf("Toggle() error = %v", err)
	}
	if err := store.Clear(); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if size := info.Size(); size != 0 {
		t.Fatalf("file size = %d, want 0", size)
	}
}

func TestStoreRejectsInvalidTags(t *testing.T) {
	t.Parallel()

	store := NewStore(filepath.Join(t.TempDir(), "tags"))

	if _, err := store.Toggle("   "); !errors.Is(err, ErrInvalidTag) {
		t.Fatalf("Toggle() blank error = %v, want %v", err, ErrInvalidTag)
	}
	if _, err := store.Toggle("bad\ntag"); !errors.Is(err, ErrInvalidTag) {
		t.Fatalf("Toggle() newline error = %v, want %v", err, ErrInvalidTag)
	}
}
