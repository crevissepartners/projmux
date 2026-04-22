package candidates

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestDiscoverOrdersAndDeduplicatesCandidates(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	fixture.mkdir("home/dotfiles")
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
		fixture.path("home/dotfiles"),
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
	fixture.mkdir("home/dotfiles")
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
		fixture.path("home/dotfiles"),
	}

	if !slices.Equal(got, want) {
		t.Fatalf("Discover() = %q, want %q", got, want)
	}
}

func TestDiscoverKeepsCurrentPathWhenOutsideManagedRoots(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	fixture.mkdir("home/dotfiles")
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
		fixture.path("home/dotfiles"),
		fixture.path("outside/project/deeper"),
	}

	if !slices.Equal(got, want) {
		t.Fatalf("Discover() = %q, want %q", got, want)
	}
}

func TestDiscoverSnapsCurrentPathAgainstRepoRootFirst(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t)
	fixture.mkdir("home/dotfiles")
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
		fixture.path("home/dotfiles"),
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
