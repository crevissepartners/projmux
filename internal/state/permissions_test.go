package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsurePrivateDirCreatesPrivateDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "nested")
	if err := EnsurePrivateDir(path); err != nil {
		t.Fatalf("EnsurePrivateDir() error = %v", err)
	}
	assertMode(t, path, PrivateDirMode)
}

func TestRepairPrivateFileIsIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	path := filepath.Join(dir, "sensitive.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	RepairPrivateFile(path)
	RepairPrivateFile(path)

	assertMode(t, dir, PrivateDirMode)
	assertMode(t, path, PrivateFileMode)
}

func TestRepairPrivatePathIgnoresUnsupportedChmod(t *testing.T) {
	calls := 0
	repairPrivatePathWith("/mnt/c/state", PrivateFileMode, func(string, os.FileMode) error {
		calls++
		return errors.New("operation not supported")
	})
	if calls != 1 {
		t.Fatalf("chmod calls = %d, want 1", calls)
	}
}

func TestRepairPrivateFileDoesNotFollowSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	RepairPrivateFile(link)

	assertMode(t, target, 0o644)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %#o, want %#o", path, got, want)
	}
}
