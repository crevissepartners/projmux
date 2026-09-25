package state

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func plantTemp(t *testing.T, path string, age time.Duration) {
	t.Helper()
	if err := os.WriteFile(path, []byte("orphan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-age)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

func assertExists(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Lstat(path)
	if got := err == nil; got != want {
		t.Fatalf("%s exists = %v (err %v), want %v", filepath.Base(path), got, err, want)
	}
}

func TestLinesFileWriteReclaimsStaleOrphanTemps(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "preview-state")
	stale := filepath.Join(dir, ".preview-state.tmp-111")
	fresh := filepath.Join(dir, ".preview-state.tmp-222")
	otherPrefix := filepath.Join(dir, ".other.tmp-123")
	bare := filepath.Join(dir, ".preview-state.tmp-")
	unrelated := filepath.Join(dir, "notes")
	plantTemp(t, stale, 2*time.Minute)
	plantTemp(t, fresh, 10*time.Second)
	plantTemp(t, otherPrefix, 2*time.Minute)
	plantTemp(t, bare, 2*time.Minute)
	plantTemp(t, unrelated, 2*time.Minute)
	plantTemp(t, target, 2*time.Minute)

	file := NewLinesFile(target)
	if err := file.Write([]string{"alpha"}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	assertExists(t, stale, false)
	assertExists(t, fresh, true)
	assertExists(t, otherPrefix, true)
	assertExists(t, bare, true)
	assertExists(t, unrelated, true)
	got, err := file.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if want := []string{"alpha"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Read() = %v, want %v", got, want)
	}
}

func TestLinesFileWriteSkipsUnremovableStaleTempNames(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "preview-state")
	stamp := time.Now().Add(-2 * time.Minute)

	// A non-empty directory named like an orphan: Remove would fail on it, and
	// the reclaimer must not descend into or delete directories anyway.
	orphanDir := filepath.Join(dir, ".preview-state.tmp-dir")
	kept := filepath.Join(orphanDir, "kept")
	if err := os.MkdirAll(orphanDir, 0o700); err != nil {
		t.Fatal(err)
	}
	plantTemp(t, kept, 2*time.Minute)
	if err := os.Chtimes(orphanDir, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	// A symlink to a directory named like an orphan is not a regular file.
	linkTarget := t.TempDir()
	orphanLink := filepath.Join(dir, ".preview-state.tmp-link")
	if err := os.Symlink(linkTarget, orphanLink); err != nil {
		t.Fatal(err)
	}

	file := NewLinesFile(target)
	if err := file.Write([]string{"alpha", "beta"}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	assertExists(t, orphanDir, true)
	assertExists(t, kept, true)
	assertExists(t, orphanLink, true)
	assertExists(t, linkTarget, true)
	got, err := file.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if want := []string{"alpha", "beta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Read() = %v, want %v", got, want)
	}
}

func TestReclaimStaleTempsIgnoresMissingDirAndPatternWithoutWildcard(t *testing.T) {
	t.Parallel()

	ReclaimStaleTemps(filepath.Join(t.TempDir(), "missing"), ".preview-state.tmp-*")

	dir := t.TempDir()
	orphan := filepath.Join(dir, ".preview-state.tmp-111")
	plantTemp(t, orphan, 2*time.Minute)
	ReclaimStaleTemps(dir, ".preview-state.tmp-111")
	assertExists(t, orphan, true)
}
