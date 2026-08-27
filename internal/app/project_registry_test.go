package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/candidates"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// newTestReconciler builds a reconciler over an explicit root list and an
// in-memory tmux server.
func newTestReconciler(tmux *fakeTmux, roots []string) *registryReconciler {
	typed := runtimeMutationMetadataMirror{runner: explicitTmuxRunner{
		runner: tmux, target: tmuxTransport{Kind: tmuxSocketName, Value: defaultAppSocket, Source: tmuxSocketNameSource},
	}}
	return &registryReconciler{
		discoverRoots: func() ([]string, error) { return roots, nil },
		liveSessions: func(context.Context) (map[string]bool, error) {
			return tmux.sessionNames(), nil
		},
		observeLegacy: func(context.Context, string) (coremetadata.LegacySession, intmetadata.LegacyTargets, error) {
			return coremetadata.LegacySession{}, intmetadata.LegacyTargets{}, nil
		},
		mirror:         intmetadata.NewMirror(tmux),
		mirrorProject:  typed.MirrorProject,
		mirrorWindow:   typed.MirrorWindow,
		mirrorPane:     typed.MirrorPane,
		shell:          "/bin/zsh",
		sessionNameFor: filepath.Base,
	}
}

// reconcileFixtureMutator is the deterministic mutator every reconciler test uses.
func reconcileFixtureMutator() coremetadata.Mutator {
	return coremetadata.Mutator{
		Now:       func() time.Time { return resourceFixtureClock },
		NewUID:    coremetadata.NewUID,
		DirExists: intmetadata.DirExists,
	}
}

// registerFixtureProject performs the explicit bootstrap a test needs, so a test
// about reconciliation states plainly that the Project got there on purpose.
func registerFixtureProject(t *testing.T, registry *coremetadata.Registry, mutator coremetadata.Mutator, root string) coremetadata.Project {
	t.Helper()
	result, err := mutator.RegisterProject(registry, coremetadata.RegisterProjectOptions{
		Root:         root,
		DefaultShell: "/bin/zsh",
		SessionName:  filepath.Base(root),
		OperationID:  "op-register-" + filepath.Base(root),
	})
	if err != nil {
		t.Fatalf("register project %q: %v", root, err)
	}
	return result.Project
}

// TestReconcileNeverRegistersADiscoveredWorkdir is acceptance (1).
//
// Discovery used to be a registration: every reconciliation pass walked the
// configured workdirs and registered the ones it did not recognize, so any
// unrelated mutation -- a `create pane` in one repository -- could add a Project
// for every sibling directory under a scan root. Scanning finds candidates; it
// decides nothing.
//
// The fixture is deliberately the worst case for the old behaviour: several
// children under one root, plus a symlinked spelling, plus a pinned path. The
// reconciler is handed all of them and must still add zero Projects, while still
// doing its actual job of importing the live session it can attribute.
func TestReconcileNeverRegistersADiscoveredWorkdir(t *testing.T) {
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
	link := filepath.Join(base, "alpha-link")
	if err := os.Symlink(filepath.Join(base, "alpha"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	reconciler := newTestReconciler(newFakeTmux(), append(slices.Clone(roots), link))

	registry := coremetadata.NewRegistry()
	mutator := reconcileFixtureMutator()
	if err := reconciler.reconcile(context.Background(), &registry, mutator, "op-1"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(registry.Projects) != 0 {
		var names []string
		for _, project := range registry.Projects {
			names = append(names, project.Metadata.Name+"="+project.Spec.Root)
		}
		t.Fatalf("reconciliation registered %d Projects from discovery alone: %v", len(registry.Projects), names)
	}

	// Registering one candidate explicitly registers that candidate and nothing
	// adjacent to it, and a later pass still adds none of its siblings.
	explicit := registerFixtureProject(t, &registry, mutator, roots[1])
	if err := reconciler.reconcile(context.Background(), &registry, mutator, "op-2"); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if len(registry.Projects) != 1 {
		t.Fatalf("projects = %d, want only the explicitly registered one", len(registry.Projects))
	}
	if got := registry.Projects[0].Metadata.UID; got != explicit.Metadata.UID {
		t.Fatalf("project uid = %q, want the explicitly registered %q", got, explicit.Metadata.UID)
	}
	if got, want := registry.Projects[0].Spec.Root, roots[1]; got != want {
		t.Fatalf("project root = %q, want the exact registered path %q", got, want)
	}
	// The symlinked spelling of the registered root is still not a second
	// Project, and it is not one because nothing registered it -- not because a
	// uid was merged.
	if candidates.CanonicalPath(link) != candidates.CanonicalPath(roots[1]) {
		t.Fatal("the fixture symlink does not resolve onto the alpha root")
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
	mutator := reconcileFixtureMutator()

	// Both Projects are registered explicitly. Reconciliation refreshes status;
	// it is not what puts a Project in the Registry.
	registry := coremetadata.NewRegistry()
	registerFixtureProject(t, &registry, mutator, live)
	registerFixtureProject(t, &registry, mutator, gone)
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
	mutator := reconcileFixtureMutator()
	if err := reconciler.reconcile(context.Background(), &registry, mutator, "op-1"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	// Neither strange session is importable, and the discovered workdir is not a
	// Project either: nothing registered it.
	if len(registry.Projects) != 0 {
		var names []string
		for _, project := range registry.Projects {
			names = append(names, project.Metadata.Name)
		}
		t.Fatalf("projects = %v, want none", names)
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

// TestReconcileConvergesRuntimeBindingsWithNoHookFired is the integration half
// of the runtime-observation contract.
//
// It fires no hook at all. `after-kill-pane` reports an empty #{hook_pane}, so
// no event can name the object that died; convergence therefore has to come
// from an inventory diff, and this test proves it does. The pane and the window
// simply stop existing on the fake server, and the very next reconciliation
// pass -- with nothing told to it -- records why.
//
// It also pins the preservation half: neither the Window nor the Pane is
// deleted, re-identified, or stripped of its name reservation, and the reason
// clears again when the runtime comes back.
func TestReconcileConvergesRuntimeBindingsWithNoHookFired(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	tmux := newFakeTmux()
	session := tmux.addSession(filepath.Base(root))
	seedLiveWindow(t, tmux, session, "win-alpha", "pan-alpha")
	window := seedLiveWindow(t, tmux, session, "win-beta", "pan-beta")
	window.panes = append(window.panes, &fakeTmuxPane{
		id:   tmux.mint("%"),
		opts: map[string]string{tmuxopts.PaneUID: "pan-beta-second"},
	})

	reconciler := newTestReconciler(tmux, []string{root})
	mutator := reconcileFixtureMutator()
	registry := runtimeBindingRegistry(t, root)

	reconcileOnce := func(operationID string) {
		t.Helper()
		if err := reconciler.reconcile(context.Background(), &registry, mutator, operationID); err != nil {
			t.Fatalf("reconcile %s: %v", operationID, err)
		}
	}

	// Everything the registry holds is mirrored on the fake server, so nothing
	// is conditioned.
	reconcileOnce("op-1")
	assertRuntimeConditions(t, registry, nil)

	// One pane of a live window goes away. No hook fires; the pane is simply
	// gone from the inventory.
	if _, err := tmux.Run(context.Background(), "tmux", "kill-pane", "-t", window.panes[1].id); err != nil {
		t.Fatalf("kill-pane: %v", err)
	}
	reconcileOnce("op-2")
	assertRuntimeConditions(t, registry, []string{"pan-beta-second"})

	// Now the whole window goes away, taking its remaining pane with it.
	if _, err := tmux.Run(context.Background(), "tmux", "kill-window", "-t", window.id); err != nil {
		t.Fatalf("kill-window: %v", err)
	}
	reconcileOnce("op-3")
	assertRuntimeConditions(t, registry, []string{"pan-beta", "pan-beta-second", "win-beta"})

	// Nothing was deleted or re-identified, and the reason is queryable.
	if len(registry.Windows) != 2 || len(registry.Panes) != 3 {
		t.Fatalf("a vanished runtime removed resources: %d windows, %d panes", len(registry.Windows), len(registry.Panes))
	}
	gone, ok := registry.Window("win-beta")
	if !ok {
		t.Fatal("the vanished Window is no longer queryable")
	}
	condition, ok := gone.HasCondition(coremetadata.ConditionMissingRuntime)
	if !ok {
		t.Fatal("the vanished Window carries no MissingRuntime condition")
	}
	if condition.Reason != coremetadata.ReasonRuntimeUnbound || !strings.Contains(condition.Message, "win-beta") {
		t.Fatalf("condition does not record the reason: %+v", condition)
	}
	// The first observation is the one that is preserved: a later pass must not
	// rewrite the timestamp and make an old problem look new.
	firstObserved := condition.FirstObservedAt
	reconcileOnce("op-4")
	again, _ := registry.Window("win-beta")
	repeat, _ := again.HasCondition(coremetadata.ConditionMissingRuntime)
	if !repeat.FirstObservedAt.Equal(firstObserved) {
		t.Fatalf("a repeat observation rewrote firstObservedAt: %v -> %v", firstObserved, repeat.FirstObservedAt)
	}

	// The runtime comes back on the same uids. The condition clears and the
	// resources keep the identity they have had all along.
	restored := seedLiveWindow(t, tmux, session, "win-beta", "pan-beta")
	restored.panes = append(restored.panes, &fakeTmuxPane{
		id:   tmux.mint("%"),
		opts: map[string]string{tmuxopts.PaneUID: "pan-beta-second"},
	})
	reconcileOnce("op-5")
	assertRuntimeConditions(t, registry, nil)
}

// TestReconcileFailsClosedWhenTheInventoryCannotBeRead proves an unreadable
// tmux server never rewrites the registry.
//
// Conditioning every Window and Pane because a query errored would be the same
// class of mistake as the stored bool: an answer invented from something other
// than an observation.
func TestReconcileFailsClosedWhenTheInventoryCannotBeRead(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	tmux := newFakeTmux()
	session := tmux.addSession(filepath.Base(root))
	seedLiveWindow(t, tmux, session, "win-alpha", "pan-alpha")
	seedLiveWindow(t, tmux, session, "win-beta", "pan-beta")
	// Armed for the whole pass. Reconcile reads the pane inventory twice now --
	// once before it writes any binding, once after -- and a one-shot trigger
	// would leave the second read succeeding, which is not the machine state
	// this test is about.
	tmux.fail = []string{"list-panes", "-a"}
	tmux.failAlways = true

	reconciler := newTestReconciler(tmux, []string{root})
	mutator := coremetadata.Mutator{
		Now:       func() time.Time { return resourceFixtureClock },
		NewUID:    coremetadata.NewUID,
		DirExists: intmetadata.DirExists,
	}
	// The registry holds a Pane the fake server does not mirror at all, so a
	// pass that trusted the failed query would condition at least that one.
	registry := runtimeBindingRegistry(t, root)

	if err := reconciler.reconcile(context.Background(), &registry, mutator, "op-1"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	assertRuntimeConditions(t, registry, nil)
}

// runtimeBindingRegistry builds a one-Project registry whose two Windows and
// three Panes carry the uids the fixture mirrors onto the fake tmux server.
func runtimeBindingRegistry(t *testing.T, root string) coremetadata.Registry {
	t.Helper()

	registry := coremetadata.NewRegistry()
	owner := func(kind coremetadata.Kind, uid string) *coremetadata.OwnerRef {
		return &coremetadata.OwnerRef{Kind: kind, UID: uid}
	}
	meta := func(uid, name string, ownerRef *coremetadata.OwnerRef) coremetadata.ObjectMeta {
		return coremetadata.ObjectMeta{UID: uid, Name: name, OwnerRef: ownerRef, CreatedAt: resourceFixtureClock}
	}
	reserve := func(scope string, kind coremetadata.Kind, name, uid string) {
		registry.NameReservations = append(registry.NameReservations, coremetadata.NameReservation{
			Scope: scope, Kind: kind, Name: name, UID: uid,
		})
	}

	registry.Projects = []coremetadata.Project{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
		Metadata: meta("prj-alpha", filepath.Base(root), nil),
		Spec:     coremetadata.ProjectSpec{Root: root, PrimaryWindowRef: "win-alpha"},
		Status: coremetadata.ProjectStatus{
			Session: &coremetadata.SessionProjection{Name: filepath.Base(root), Live: true},
		},
	}}
	reserve("", coremetadata.KindProject, filepath.Base(root), "prj-alpha")

	registry.Windows = []coremetadata.Window{
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
			Metadata: meta("win-alpha", "alpha", owner(coremetadata.KindProject, "prj-alpha")),
			Spec:     coremetadata.WindowSpec{AnchorPaneRef: "pan-alpha"},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
			Metadata: meta("win-beta", "beta", owner(coremetadata.KindProject, "prj-alpha")),
			Spec:     coremetadata.WindowSpec{AnchorPaneRef: "pan-beta"},
		},
	}
	reserve("prj-alpha", coremetadata.KindWindow, "alpha", "win-alpha")
	reserve("prj-alpha", coremetadata.KindWindow, "beta", "win-beta")

	registry.Panes = []coremetadata.Pane{
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: meta("pan-alpha", "zsh", owner(coremetadata.KindWindow, "win-alpha")),
			Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: root},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: meta("pan-beta", "zsh", owner(coremetadata.KindWindow, "win-beta")),
			Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: root},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: meta("pan-beta-second", "log", owner(coremetadata.KindWindow, "win-beta")),
			Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: root},
		},
	}
	reserve("win-alpha", coremetadata.KindPane, "zsh", "pan-alpha")
	reserve("win-beta", coremetadata.KindPane, "zsh", "pan-beta")
	reserve("win-beta", coremetadata.KindPane, "log", "pan-beta-second")

	if err := registry.Validate(); err != nil {
		t.Fatalf("runtime binding fixture is not a valid registry: %v", err)
	}
	return registry
}

// assertRuntimeConditions checks exactly which uids carry MissingRuntime.
func assertRuntimeConditions(t *testing.T, registry coremetadata.Registry, want []string) {
	t.Helper()
	var got []string
	for _, window := range registry.Windows {
		if _, ok := window.HasCondition(coremetadata.ConditionMissingRuntime); ok {
			got = append(got, window.Metadata.UID)
		}
	}
	for _, pane := range registry.Panes {
		if _, ok := pane.HasCondition(coremetadata.ConditionMissingRuntime); ok {
			got = append(got, pane.Metadata.UID)
		}
	}
	slices.Sort(got)
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MissingRuntime carried by %v, want %v", got, want)
	}
}
