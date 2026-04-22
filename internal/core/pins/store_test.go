package pins

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/es5h/projmux/internal/config"
)

func TestNewDefaultStoreUsesConfigPinFile(t *testing.T) {
	t.Parallel()

	paths := config.DefaultPaths("/tmp/config-home", "/tmp/state-home")
	store := NewDefaultStore(paths)

	if got, want := store.Path(), paths.PinFile(); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

func TestStoreListMissingFile(t *testing.T) {
	t.Parallel()

	store := NewStore(filepath.Join(t.TempDir(), "pins"))

	got, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List() = %v, want empty", got)
	}
}

func TestStoreAddAndList(t *testing.T) {
	t.Parallel()

	store := NewStore(filepath.Join(t.TempDir(), "pins"))

	if err := store.Add("/tmp/app"); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if err := store.Add("/tmp/lib"); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if err := store.Add("/tmp/app"); err != nil {
		t.Fatalf("Add() duplicate error = %v", err)
	}

	got, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	want := []string{"/tmp/app", "/tmp/lib"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %v, want %v", got, want)
	}
}

func TestStoreRemoveRemovesAllMatchingEntries(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "pins")
	if err := os.WriteFile(path, []byte("/tmp/app\n/tmp/lib\n/tmp/app\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := NewStore(path)
	if err := store.Remove("/tmp/app"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	got, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	want := []string{"/tmp/lib"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %v, want %v", got, want)
	}
}

func TestStoreToggleAddsAndRemoves(t *testing.T) {
	t.Parallel()

	store := NewStore(filepath.Join(t.TempDir(), "pins"))

	pinned, err := store.Toggle("/tmp/app")
	if err != nil {
		t.Fatalf("Toggle() add error = %v", err)
	}
	if !pinned {
		t.Fatalf("Toggle() pinned = %v, want true", pinned)
	}

	pinned, err = store.Toggle("/tmp/app")
	if err != nil {
		t.Fatalf("Toggle() remove error = %v", err)
	}
	if pinned {
		t.Fatalf("Toggle() pinned = %v, want false", pinned)
	}

	got, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List() = %v, want empty", got)
	}
}

func TestStoreClearLeavesEmptyInspectableFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "pins")
	store := NewStore(path)

	if err := store.Add("/tmp/app"); err != nil {
		t.Fatalf("Add() error = %v", err)
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

func TestStoreRejectsInvalidPins(t *testing.T) {
	t.Parallel()

	store := NewStore(filepath.Join(t.TempDir(), "pins"))

	if err := store.Add("   "); !errors.Is(err, ErrInvalidPin) {
		t.Fatalf("Add() error = %v, want %v", err, ErrInvalidPin)
	}
	if err := store.Remove(""); !errors.Is(err, ErrInvalidPin) {
		t.Fatalf("Remove() error = %v, want %v", err, ErrInvalidPin)
	}
	if _, err := store.Toggle("bad\npin"); !errors.Is(err, ErrInvalidPin) {
		t.Fatalf("Toggle() error = %v, want %v", err, ErrInvalidPin)
	}
}
