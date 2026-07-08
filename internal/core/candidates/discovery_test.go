package candidates

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDiscoverOrdersAndDeduplicatesCandidates(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	fixture.mkdir("home")
	fixture.mkdir("pins/alpha")
	fixture.mkdir("pins/beta")
	fixture.mkdir("rp/repo-a")
	fixture.mkdir("rp/repo-b")
	fixture.mkdir("managed/work-a")
	fixture.mkdir("managed/work-b")
	fixture.mkdir("managed/work-a/nested")

	got, err := Discover(Inputs{
		HomeDir:      fixture.path("home"),
		RepoRoot:     fixture.path("rp"),
		ManagedRoots: []string{fixture.path("managed")},
		Pins: []string{
			fixture.path("pins/alpha"),
			fixture.path("pins/beta"),
			fixture.path("rp/repo-a"),
		},
		CurrentPath: fixture.path("managed/work-a/nested"),
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	want := []string{
		fixture.path("home"),
		fixture.path("pins/alpha"),
		fixture.path("pins/beta"),
		fixture.path("rp/repo-a"),
		fixture.path("managed/work-a"),
		fixture.path("rp/repo-b"),
		fixture.path("managed/work-b"),
	}

	if !slices.Equal(got, want) {
		t.Fatalf("Discover() = %q, want %q", got, want)
	}
}

func TestDiscoverSkipsMissingInputs(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	fixture.mkdir("home")
	fixture.mkdir("rp")

	got, err := Discover(Inputs{
		HomeDir:      fixture.path("home"),
		RepoRoot:     fixture.path("rp"),
		ManagedRoots: []string{fixture.path("missing-root")},
		Pins:         []string{fixture.path("missing-pin")},
		CurrentPath:  fixture.path("missing-current"),
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	want := []string{
		fixture.path("home"),
	}

	if !slices.Equal(got, want) {
		t.Fatalf("Discover() = %q, want %q", got, want)
	}
}

func TestDiscoverKeepsCurrentPathWhenOutsideManagedRoots(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	fixture.mkdir("home")
	fixture.mkdir("outside/project/deeper")

	got, err := Discover(Inputs{
		HomeDir:     fixture.path("home"),
		CurrentPath: fixture.path("outside/project/deeper"),
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	want := []string{
		fixture.path("home"),
		fixture.path("outside/project/deeper"),
	}

	if !slices.Equal(got, want) {
		t.Fatalf("Discover() = %q, want %q", got, want)
	}
}

func TestDiscoverSnapsCurrentPathAgainstRepoRootFirst(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	fixture.mkdir("home")
	fixture.mkdir("rp/repo-a/deeper")

	got, err := Discover(Inputs{
		HomeDir:      fixture.path("home"),
		RepoRoot:     fixture.path("rp"),
		ManagedRoots: []string{fixture.path("rp")},
		CurrentPath:  fixture.path("rp/repo-a/deeper"),
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	want := []string{
		fixture.path("home"),
		fixture.path("rp/repo-a"),
	}

	if !slices.Equal(got, want) {
		t.Fatalf("Discover() = %q, want %q", got, want)
	}
}

type fixtureFS struct {
	root string
	t    *testing.T
}

func newFixture(t *testing.T) fixtureFS {
	t.Helper()

	return fixtureFS{
		root: t.TempDir(),
		t:    t,
	}
}

func (f fixtureFS) mkdir(rel string) {
	f.t.Helper()

	if err := os.MkdirAll(f.path(rel), 0o755); err != nil {
		f.t.Fatalf("MkdirAll(%q): %v", rel, err)
	}
}

func (f fixtureFS) path(rel string) string {
	f.t.Helper()

	return filepath.Join(f.root, filepath.FromSlash(rel))
}

func TestDiscoverDedupesSymlinkAndRealPathKeepingDisplayForm(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	fixture.mkdir("real/proj")

	linkRoot := fixture.path("link")
	if err := os.Symlink(fixture.path("real"), linkRoot); err != nil {
		t.Fatalf("Symlink(): %v", err)
	}

	symlinkProj := filepath.Join(linkRoot, "proj")
	realProj := fixture.path("real/proj")

	got, err := Discover(Inputs{
		// Symlink spelling is encountered first, so it wins as the display form.
		Pins: []string{symlinkProj, realProj},
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	want := []string{symlinkProj}
	if !slices.Equal(got, want) {
		t.Fatalf("Discover() = %q, want %q (symlink and real path must collapse to one, keeping symlink spelling)", got, want)
	}
}

func TestDiscoverDedupePrefersFirstEncounteredDisplayForm(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	fixture.mkdir("real/proj")

	linkRoot := fixture.path("link")
	if err := os.Symlink(fixture.path("real"), linkRoot); err != nil {
		t.Fatalf("Symlink(): %v", err)
	}

	symlinkProj := filepath.Join(linkRoot, "proj")
	realProj := fixture.path("real/proj")

	got, err := Discover(Inputs{
		// Real path is encountered first here, so it wins.
		Pins: []string{realProj, symlinkProj},
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	want := []string{realProj}
	if !slices.Equal(got, want) {
		t.Fatalf("Discover() = %q, want %q", got, want)
	}
}

func TestCanonicalPathResolvesSymlinkAndFallsBack(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	fixture.mkdir("real/proj")

	linkRoot := fixture.path("link")
	if err := os.Symlink(fixture.path("real"), linkRoot); err != nil {
		t.Fatalf("Symlink(): %v", err)
	}

	symlinkProj := filepath.Join(linkRoot, "proj")
	realProj := fixture.path("real/proj")

	wantResolved, err := filepath.EvalSymlinks(realProj)
	if err != nil {
		t.Fatalf("EvalSymlinks(): %v", err)
	}
	if got := CanonicalPath(symlinkProj); got != wantResolved {
		t.Fatalf("CanonicalPath(symlink) = %q, want %q", got, wantResolved)
	}
	if got := CanonicalPath(realProj); got != wantResolved {
		t.Fatalf("CanonicalPath(real) = %q, want %q", got, wantResolved)
	}

	// Broken/dangling symlink and missing paths must fall back to Clean without
	// erroring or panicking.
	brokenLink := fixture.path("broken")
	if err := os.Symlink(fixture.path("does-not-exist"), brokenLink); err != nil {
		t.Fatalf("Symlink(broken): %v", err)
	}
	if got := CanonicalPath(brokenLink); got != filepath.Clean(brokenLink) {
		t.Fatalf("CanonicalPath(broken) = %q, want %q", got, filepath.Clean(brokenLink))
	}

	missing := fixture.path("missing/./child")
	if got := CanonicalPath(missing); got != filepath.Clean(missing) {
		t.Fatalf("CanonicalPath(missing) = %q, want %q", got, filepath.Clean(missing))
	}

	if got := CanonicalPath("   "); got != "" {
		t.Fatalf("CanonicalPath(blank) = %q, want empty", got)
	}
}

func TestDiscoverSnapsRealPathCurrentBackToSymlinkRoot(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	fixture.mkdir("home")
	fixture.mkdir("real/proj")

	// linkRoot is the managed root spelled via a symlink (~/obsidian style);
	// its real location is fixture/real.
	linkRoot := fixture.path("link")
	if err := os.Symlink(fixture.path("real"), linkRoot); err != nil {
		t.Fatalf("Symlink(): %v", err)
	}

	// tmux reports the kernel-resolved real path as the active session cwd.
	realCurrent := fixture.path("real/proj")

	got, err := Discover(Inputs{
		HomeDir:      fixture.path("home"),
		ManagedRoots: []string{linkRoot},
		CurrentPath:  realCurrent,
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	symlinkProj := filepath.Join(linkRoot, "proj")
	want := []string{
		fixture.path("home"),
		symlinkProj,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Discover() = %q, want %q (real-path current must snap back to the symlink-form root and dedup with root children)", got, want)
	}

	realPrefix := fixture.path("real") + string(filepath.Separator)
	for _, c := range got {
		if c == realCurrent || strings.HasPrefix(c, realPrefix) {
			t.Fatalf("Discover() leaked real path %q; candidates = %q", c, got)
		}
	}
}

func TestSnappedCurrentPathRebuildsSymlinkFormAndFallsBack(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	fixture.mkdir("real/proj/deeper")

	linkRoot := fixture.path("link")
	if err := os.Symlink(fixture.path("real"), linkRoot); err != nil {
		t.Fatalf("Symlink(): %v", err)
	}

	// A real-path cwd nested under a symlink-form managed root snaps back to the
	// symlink spelling, one segment below the root.
	realCurrent := fixture.path("real/proj/deeper")
	if got, want := snappedCurrentPath(realCurrent, []string{linkRoot}), filepath.Join(linkRoot, "proj"); got != want {
		t.Fatalf("snappedCurrentPath(real current) = %q, want %q", got, want)
	}

	// Broken root symlink: no reconstruction, current returned as-is (Clean),
	// no panic.
	brokenRoot := fixture.path("broken")
	if err := os.Symlink(fixture.path("does-not-exist"), brokenRoot); err != nil {
		t.Fatalf("Symlink(broken): %v", err)
	}
	if got, want := snappedCurrentPath(realCurrent, []string{brokenRoot}), filepath.Clean(realCurrent); got != want {
		t.Fatalf("snappedCurrentPath(broken root) = %q, want %q", got, want)
	}

	// Current outside every managed root is returned unchanged.
	outside := fixture.path("real/proj")
	if got, want := snappedCurrentPath(outside, []string{fixture.path("nope")}), filepath.Clean(outside); got != want {
		t.Fatalf("snappedCurrentPath(outside root) = %q, want %q", got, want)
	}
}
