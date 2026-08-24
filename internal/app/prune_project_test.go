package app

import (
	"fmt"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

func newTestPruneProjectCommand(store *fakeResourceStore) *pruneProjectCommand {
	return &pruneProjectCommand{
		store: store.store(),
		now:   func() time.Time { return store.now },
	}
}

// TestPruneProjectRequiresAnExplicitBoundedScope is acceptance criterion 4's
// "explicit" half: the destructive scope must be spelled out in full, and a
// listing never deletes anything.
func TestPruneProjectRequiresAnExplicitBoundedScope(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "no selector at all", args: nil, want: "requires --missing"},
		{name: "missing without an age bound", args: []string{"--missing"}, want: "requires --older-than"},
		{name: "age bound without --missing", args: []string{"--older-than", "720h"}, want: "requires --missing"},
		{name: "unparseable duration", args: []string{"--missing", "--older-than", "forever"}, want: "is not a duration"},
		{name: "negative duration", args: []string{"--missing", "--older-than", "-1h"}, want: "must not be negative"},
		{name: "positional argument", args: []string{"--missing", "--older-than", "720h", "alpha"}, want: "does not accept positional arguments"},
		{name: "unknown flag", args: []string{"--all"}, want: "flag provided but not defined"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			before := store.snapshot()
			stdout, _, err := runRoute(t, newTestPruneProjectCommand(store), test.args...)
			if err == nil {
				t.Fatalf("prune project %v succeeded", test.args)
			}
			if !IsUsageError(err) {
				t.Fatalf("prune project %v error is not a usage error: %v", test.args, err)
			}
			if stdout != "" {
				t.Fatalf("prune project %v wrote %q to stdout, want 0 bytes", test.args, stdout)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("prune project %v error = %q, want it to mention %q", test.args, err, test.want)
			}
			if store.transactions != 0 || store.snapshot() != before {
				t.Fatalf("prune project %v touched the registry", test.args)
			}
		})
	}
}

// TestPruneProjectListsCandidatesWithoutDeletingThem covers the default,
// non-destructive shape of the route.
func TestPruneProjectListsCandidatesWithoutDeletingThem(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	before := store.snapshot()
	stdout, stderr, err := runRoute(t, newTestPruneProjectCommand(store), "--missing", "--older-than", "720h")
	if err != nil {
		t.Fatalf("prune project error = %v (stderr=%s)", err, stderr)
	}
	for _, want := range []string{
		"prune project: would delete 1 project",
		"project/gone uid=prj-gone root=/srv/gone missingSince=2026-07-04T17:00:00Z",
		"dry-run: nothing was deleted; re-run with --yes to delete",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("listing is missing %q:\n%s", want, stdout)
		}
	}
	if store.transactions != 0 {
		t.Fatalf("the listing opened %d write transactions, want 0", store.transactions)
	}
	if store.snapshot() != before {
		t.Fatalf("the listing mutated the registry")
	}
}

// TestPruneProjectExcludesRecoveredRootsAndLiveRuntimes is acceptance criterion
// 4's exclusion half.
func TestPruneProjectExcludesRecoveredRootsAndLiveRuntimes(t *testing.T) {
	t.Parallel()

	t.Run("a recovered root drops out of the candidate set", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		// The root is back on disk, so the observation clears and the Project is
		// no longer a candidate at any age.
		store.dirs["/srv/gone"] = true
		stdout, _, err := runRoute(t, newTestPruneProjectCommand(store), "--missing", "--older-than", "0s")
		if err != nil {
			t.Fatalf("prune project error = %v", err)
		}
		if !strings.Contains(stdout, "no Project matches") {
			t.Fatalf("a recovered root was still listed:\n%s", stdout)
		}
	})

	t.Run("a live runtime keeps the project", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		store.registry.Projects[2].Status.Session = &coremetadata.SessionProjection{Name: "gone", Live: true}
		stdout, _, err := runRoute(t, newTestPruneProjectCommand(store), "--missing", "--older-than", "720h")
		if err != nil {
			t.Fatalf("prune project error = %v", err)
		}
		if !strings.Contains(stdout, "no Project matches") {
			t.Fatalf("a project with a live runtime was listed:\n%s", stdout)
		}
	})

	t.Run("a freshly observed missing root is not old enough", func(t *testing.T) {
		t.Parallel()
		store := newFakeResourceStore(t)
		// `beta` loses its root during this invocation. The observation is
		// recorded with the current time, so an age bound excludes it: nothing is
		// deleted on evidence this command just created.
		delete(store.dirs, "/srv/beta")
		stdout, _, err := runRoute(t, newTestPruneProjectCommand(store), "--missing", "--older-than", "720h")
		if err != nil {
			t.Fatalf("prune project error = %v", err)
		}
		if strings.Contains(stdout, "project/beta") {
			t.Fatalf("a just-observed missing root was listed as prunable:\n%s", stdout)
		}
		if !strings.Contains(stdout, "project/gone") {
			t.Fatalf("the long-missing project dropped out:\n%s", stdout)
		}
	})
}

// TestPruneProjectDeletesOnlyWithYes is the destructive half.
func TestPruneProjectDeletesOnlyWithYes(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	stdout, _, err := runRoute(t, newTestPruneProjectCommand(store), "--missing", "--older-than", "720h", "--yes")
	if err != nil {
		t.Fatalf("prune project --yes error = %v", err)
	}
	if store.writes != 1 {
		t.Fatalf("prune project --yes committed %d writes, want 1", store.writes)
	}
	if _, ok := store.registry.Project("prj-gone"); ok {
		t.Fatal("the missing-root project survived --yes")
	}
	for _, uid := range []string{"prj-alpha", "prj-beta"} {
		if _, ok := store.registry.Project(uid); !ok {
			t.Fatalf("prune removed %q, which is not a candidate", uid)
		}
	}
	if !strings.Contains(stdout, "deleted 1 project") {
		t.Fatalf("stdout = %q", stdout)
	}
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("prune left an invalid registry: %v", err)
	}

	// A registry with no candidates never opens a transaction, so `--yes` on a
	// clean registry cannot create or rewrite state.
	store = newFakeResourceStore(t)
	store.dirs["/srv/gone"] = true
	if _, _, err := runRoute(t, newTestPruneProjectCommand(store), "--missing", "--older-than", "720h", "--yes"); err != nil {
		t.Fatalf("prune project --yes with no candidates error = %v", err)
	}
	if store.transactions != 0 {
		t.Fatalf("a no-candidate --yes opened %d write transactions, want 0", store.transactions)
	}
}

// TestPruneProjectShortCircuitsAnEmptyRegistry keeps the route from writing
// anything for an operator who has never registered a Project.
func TestPruneProjectShortCircuitsAnEmptyRegistry(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	store.registry = coremetadata.NewRegistry()
	stdout, _, err := runRoute(t, newTestPruneProjectCommand(store), "--missing", "--older-than", "1h", "--yes")
	if err != nil {
		t.Fatalf("prune project on an empty registry error = %v", err)
	}
	if store.transactions != 0 {
		t.Fatalf("an empty registry opened %d write transactions, want 0", store.transactions)
	}
	if !strings.Contains(stdout, "no Projects are registered") {
		t.Fatalf("stdout = %q", stdout)
	}
}

// TestPruneProjectCandidateListingIsBounded is acceptance criterion 4's
// "bounded" half: a wide match reports a count instead of every row.
func TestPruneProjectCandidateListingIsBounded(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	missing := coremetadata.Condition{
		Type:             coremetadata.ConditionMissingRoot,
		Status:           coremetadata.ConditionTrue,
		Reason:           "RootDisappeared",
		FirstObservedAt:  resourceFixtureClock.Add(-1000 * time.Hour),
		LastTransitionAt: resourceFixtureClock.Add(-1000 * time.Hour),
	}
	// Seven candidates is two more than the bound the selector contract fixes.
	for i := range 6 {
		name := fmt.Sprintf("stale-%d", i)
		uid := "prj-" + name
		store.registry.Projects = append(store.registry.Projects, coremetadata.Project{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
			Metadata: coremetadata.ObjectMeta{UID: uid, Name: name, CreatedAt: resourceFixtureClock},
			Spec:     coremetadata.ProjectSpec{Root: "/srv/" + name, PrimaryWindowRef: "win-" + name},
			Status:   coremetadata.ProjectStatus{Conditions: []coremetadata.Condition{missing}},
		})
		store.registry.NameReservations = append(store.registry.NameReservations, coremetadata.NameReservation{
			Kind: coremetadata.KindProject, Name: name, UID: uid,
		})
		windowUID, paneUID := "win-"+name, "pan-"+name
		store.registry.Windows = append(store.registry.Windows, coremetadata.Window{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
			Metadata: coremetadata.ObjectMeta{UID: windowUID, Name: "main", OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindProject, UID: uid}, CreatedAt: resourceFixtureClock},
			Spec:     coremetadata.WindowSpec{AnchorPaneRef: paneUID},
		})
		store.registry.Panes = append(store.registry.Panes, coremetadata.Pane{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: coremetadata.ObjectMeta{UID: paneUID, Name: "shell", OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: windowUID}, CreatedAt: resourceFixtureClock},
			Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: "/srv/" + name},
		})
		store.registry.NameReservations = append(store.registry.NameReservations,
			coremetadata.NameReservation{Scope: uid, Kind: coremetadata.KindWindow, Name: "main", UID: windowUID},
			coremetadata.NameReservation{Scope: windowUID, Kind: coremetadata.KindPane, Name: "shell", UID: paneUID},
		)
	}
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("bounded fixture is invalid: %v", err)
	}

	stdout, _, err := runRoute(t, newTestPruneProjectCommand(store), "--missing", "--older-than", "720h")
	if err != nil {
		t.Fatalf("prune project error = %v", err)
	}
	rows := strings.Count(stdout, "  project/")
	if rows != selector.MaxCandidates {
		t.Fatalf("listed %d candidate rows, want the bound of %d:\n%s", rows, selector.MaxCandidates, stdout)
	}
	if !strings.Contains(stdout, "... 2 more omitted") {
		t.Fatalf("the bounded listing does not report the omitted rows:\n%s", stdout)
	}
	if !strings.Contains(stdout, "would delete 7 projects") {
		t.Fatalf("the bounded listing hides the true count:\n%s", stdout)
	}
}
