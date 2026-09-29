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

func TestRemoveLockedTempsRemovesOnlyRegularFilesWithThePrefix(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	orphans := []string{filepath.Join(dir, ".store.tmp-111"), filepath.Join(dir, ".store.tmp-222")}
	for _, path := range orphans {
		// Fresh on purpose: the caller's lock, not an age, proves no live write
		// owns them.
		plantTemp(t, path, 0)
	}
	kept := []string{
		filepath.Join(dir, "store.json"),
		filepath.Join(dir, "store.json.flock"),
		filepath.Join(dir, ".other.tmp-111"),
		filepath.Join(dir, "x.store.tmp-111"),
	}
	for _, path := range kept {
		plantTemp(t, path, 0)
	}
	prefixDir := filepath.Join(dir, ".store.tmp-dir")
	if err := os.Mkdir(prefixDir, 0o700); err != nil {
		t.Fatal(err)
	}
	prefixLink := filepath.Join(dir, ".store.tmp-link")
	if err := os.Symlink(kept[0], prefixLink); err != nil {
		t.Fatal(err)
	}

	var removed []string
	RemoveLockedTemps(dir, ".store.tmp-", func(path string) error {
		removed = append(removed, path)
		return os.Remove(path)
	})

	if !reflect.DeepEqual(removed, orphans) {
		t.Fatalf("removed = %v, want %v", removed, orphans)
	}
	for _, path := range orphans {
		assertExists(t, path, false)
	}
	for _, path := range append(kept, prefixDir, prefixLink) {
		assertExists(t, path, true)
	}
}

func TestRemoveLockedTempsIsBestEffort(t *testing.T) {
	t.Parallel()

	RemoveLockedTemps(filepath.Join(t.TempDir(), "missing"), ".store.tmp-", nil)

	dir := t.TempDir()
	first := filepath.Join(dir, ".store.tmp-111")
	second := filepath.Join(dir, ".store.tmp-222")
	plantTemp(t, first, 0)
	plantTemp(t, second, 0)

	RemoveLockedTemps(dir, "", nil)
	assertExists(t, first, true)
	assertExists(t, second, true)

	// A failed removal leaves that file and does not stop the others.
	RemoveLockedTemps(dir, ".store.tmp-", func(path string) error {
		if path == first {
			return os.ErrPermission
		}
		return os.Remove(path)
	})
	assertExists(t, first, true)
	assertExists(t, second, false)

	RemoveLockedTemps(dir, ".store.tmp-", nil)
	assertExists(t, first, false)
}

func TestReclaimStaleTempsMatchingRemovesOnlyStaleRegularMatches(t *testing.T) {
	t.Parallel()

	ReclaimStaleTempsMatching(filepath.Join(t.TempDir(), "missing"), func(string) bool { return true }, nil)

	dir := t.TempDir()
	first := filepath.Join(dir, ".a.tmp-111")
	second := filepath.Join(dir, ".b.tmp-222")
	fresh := filepath.Join(dir, ".a.tmp-333")
	unmatched := filepath.Join(dir, ".c.tmp-444")
	plantTemp(t, first, 2*time.Minute)
	plantTemp(t, second, 2*time.Minute)
	plantTemp(t, fresh, 10*time.Second)
	plantTemp(t, unmatched, 2*time.Minute)
	matchDir := filepath.Join(dir, ".a.tmp-dir")
	if err := os.Mkdir(matchDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(matchDir, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	match := func(name string) bool { return name != filepath.Base(unmatched) }

	ReclaimStaleTempsMatching(dir, nil, nil)
	assertExists(t, first, true)

	// A failed removal leaves that file and does not stop the others.
	var tried []string
	ReclaimStaleTempsMatching(dir, match, func(path string) error {
		tried = append(tried, path)
		if path == first {
			return os.ErrPermission
		}
		return os.Remove(path)
	})
	if want := []string{first, second}; !reflect.DeepEqual(tried, want) {
		t.Fatalf("tried = %v, want %v", tried, want)
	}
	assertExists(t, first, true)
	assertExists(t, second, false)
	assertExists(t, fresh, true)
	assertExists(t, unmatched, true)
	assertExists(t, matchDir, true)

	ReclaimStaleTempsMatching(dir, match, nil)
	assertExists(t, first, false)
}
