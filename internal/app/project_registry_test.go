package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/candidates"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// newTestReconciler builds a reconciler over an explicit root list and an
// in-memory tmux server.
func newTestReconciler(tmux *fakeTmux, roots []string) *registryReconciler {
	return &registryReconciler{
		discoverRoots: func() ([]string, error) { return roots, nil },
		liveSessions: func(context.Context) (map[string]bool, error) {
			return tmux.sessionNames(), nil
		},
		observeLegacy: func(context.Context, string) (coremetadata.LegacySession, intmetadata.LegacyTargets, error) {
			return coremetadata.LegacySession{}, intmetadata.LegacyTargets{}, nil
		},
		mirror:         intmetadata.NewMirror(tmux),
		shell:          "/bin/zsh",
		sessionNameFor: filepath.Base,
	}
}

// TestDiscoveryRegistrationIsSortedByResolvedRootAndNeverDuplicatesAProject
// pins the deterministic registration order and the canonical-path dedupe.
func TestDiscoveryRegistrationIsSortedByResolvedRootAndNeverDuplicatesAProject(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	var roots []string
	for _, name := range []string{"zebra", "alpha", "mango"} {
		root := filepath.Join(base, name)
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		roots = append(roots, root)
	}
	// A symlinked spelling of one root must not become a second Project.
	link := filepath.Join(base, "alpha-link")
	if err := os.Symlink(filepath.Join(base, "alpha"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// Deliberately unsorted. The link follows the root it points at, which is
	// the order candidate discovery itself would produce, so first-spelling-wins
	// keeps the real path.
	reconciler := newTestReconciler(newFakeTmux(), []string{roots[0], roots[1], link, roots[2]})

	registry := coremetadata.NewRegistry()
	mutator := coremetadata.Mutator{
		Now:       func() time.Time { return resourceFixtureClock },
		NewUID:    coremetadata.NewUID,
		DirExists: intmetadata.DirExists,
	}
	if err := reconciler.registerDiscoveredRoots(&registry, mutator, "op-test"); err != nil {
		t.Fatalf("register: %v", err)
	}

	var got []string
	for _, project := range registry.Projects {
		got = append(got, project.Metadata.Name)
	}
	// Registration order follows the resolved absolute path, not argv order,
	// which is what makes automatic suffix allocation reproducible.
	if want := []string{"alpha", "mango", "zebra"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("registration order = %v, want %v", got, want)
	}
	// The symlink spelling resolved to an already-registered root, so it is not
	// a second Project. Nothing was merged or re-identified: a second Project
	// simply was not created.
	if len(registry.Projects) != 3 {
		t.Fatalf("projects = %d, want 3", len(registry.Projects))
	}
	if candidates.CanonicalPath(link) != candidates.CanonicalPath(filepath.Join(base, "alpha")) {
		t.Fatal("the fixture symlink does not resolve onto the alpha root")
	}

	// Re-running registers nothing new: first registration wins.
	before := registry.Clone()
	if err := reconciler.registerDiscoveredRoots(&registry, mutator, "op-test-2"); err != nil {
		t.Fatalf("second register: %v", err)
	}
	if len(registry.Projects) != len(before.Projects) {
		t.Fatalf("a second reconcile registered %d extra Projects", len(registry.Projects)-len(before.Projects))
	}
	for i := range registry.Projects {
		if registry.Projects[i].Metadata.UID != before.Projects[i].Metadata.UID ||
			registry.Projects[i].Metadata.Name != before.Projects[i].Metadata.Name {
			t.Fatalf("a second reconcile renumbered %+v", registry.Projects[i].Metadata)
		}
	}
}

// TestReconcileRefreshesStatusSessionAndMissingRoot pins the two status inputs
// the create preflight reads.
func TestReconcileRefreshesStatusSessionAndMissingRoot(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	live := filepath.Join(base, "live")
	gone := filepath.Join(base, "gone")
	for _, root := range []string{live, gone} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	tmux := newFakeTmux()
	reconciler := newTestReconciler(tmux, []string{live, gone})
	mutator := coremetadata.Mutator{
		Now:       func() time.Time { return resourceFixtureClock },
		NewUID:    coremetadata.NewUID,
		DirExists: intmetadata.DirExists,
	}

	registry := coremetadata.NewRegistry()
	if err := reconciler.reconcile(context.Background(), &registry, mutator, "op-1"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	for _, project := range registry.Projects {
		if project.Status.Session == nil {
			t.Fatalf("project %s has no session projection", project.Metadata.Name)
		}
		if project.Status.Session.Live {
			t.Fatalf("project %s is live with no tmux server", project.Metadata.Name)
		}
	}

	// The runtime appears: status.session flips to live without touching uids.
	tmux.addSession("live")
	uids := map[string]string{}
	for _, project := range registry.Projects {
		uids[project.Metadata.Name] = project.Metadata.UID
	}
	// And one root disappears: the Project is tombstoned, never deleted.
	if err := os.RemoveAll(gone); err != nil {
		t.Fatalf("remove root: %v", err)
	}
	if err := reconciler.reconcile(context.Background(), &registry, mutator, "op-2"); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	for _, project := range registry.Projects {
		if uids[project.Metadata.Name] != project.Metadata.UID {
			t.Fatalf("project %s changed uid across reconciles", project.Metadata.Name)
		}
		switch project.Metadata.Name {
		case "live":
			if !project.Status.Session.Live {
				t.Fatal("a live tmux session did not refresh status.session")
			}
		case "gone":
			if _, ok := project.HasCondition(coremetadata.ConditionMissingRoot); !ok {
				t.Fatal("a disappeared root did not record MissingRoot")
			}
		}
	}
	if len(registry.Projects) != 2 {
		t.Fatalf("projects = %d, want 2; a missing root must never delete a Project", len(registry.Projects))
	}
}

// TestAnUnobservableOrRootlessSessionIsSkippedRatherThanFailingTheCreate keeps
// one strange tmux session from breaking an unrelated create.
func TestAnUnobservableOrRootlessSessionIsSkippedRatherThanFailingTheCreate(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	root := filepath.Join(base, "alpha")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tmux := newFakeTmux()
	tmux.addSession("scratch")  // no project path
	tmux.addSession("vanished") // records a root that no longer exists
	reconciler := newTestReconciler(tmux, []string{root})
	reconciler.observeLegacy = func(_ context.Context, name string) (coremetadata.LegacySession, intmetadata.LegacyTargets, error) {
		switch name {
		case "vanished":
			return coremetadata.LegacySession{Session: name, Root: filepath.Join(base, "not-here")}, intmetadata.LegacyTargets{}, nil
		default:
			return coremetadata.LegacySession{Session: name}, intmetadata.LegacyTargets{}, nil
		}
	}

	registry := coremetadata.NewRegistry()
	mutator := coremetadata.Mutator{
		Now:       func() time.Time { return resourceFixtureClock },
		NewUID:    coremetadata.NewUID,
		DirExists: intmetadata.DirExists,
	}
	if err := reconciler.reconcile(context.Background(), &registry, mutator, "op-1"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(registry.Projects) != 1 || registry.Projects[0].Metadata.Name != "alpha" {
		var names []string
		for _, project := range registry.Projects {
			names = append(names, project.Metadata.Name)
		}
		t.Fatalf("projects = %v, want only the discovered workdir", names)
	}
}

// TestDiscoverProjectRootsExcludesTheHomeAndCurrentPathConveniences proves the
// registration input is the configured project set, not the picker's
// convenience rows.
func TestDiscoverProjectRootsExcludesTheHomeAndCurrentPathConveniences(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	work := filepath.Join(home, "work")
	for _, name := range []string{"one", "two"} {
		if err := os.MkdirAll(filepath.Join(work, name), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	env := map[string]string{"PROJMUX_MANAGED_ROOTS": work}
	roots, err := discoverProjectRoots(home, func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	for _, root := range roots {
		if root == filepath.Clean(home) {
			t.Fatalf("the home directory was registered as a Project: %v", roots)
		}
	}
	if len(roots) != 2 {
		t.Fatalf("roots = %v, want the two managed children", roots)
	}
	if !slices.Contains(roots, filepath.Join(work, "one")) || !slices.Contains(roots, filepath.Join(work, "two")) {
		t.Fatalf("roots = %v, want both managed children", roots)
	}
}
