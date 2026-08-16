package app

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// The fixture in this file is the machine state the phase exists to repair,
// copied from a real measurement rather than invented:
//
//   - A live tmux session that carries no `@projmux_project_path`. That option
//     is written once, by session creation, so every session older than it has
//     none -- which on the measured machine was every session there was.
//   - A Project the registry already holds for that session, reachable only
//     through status.session.name.
//   - Registry Windows and Panes whose tmux options have been wiped, so nothing
//     on the machine points back at them.
//
// The import path cannot reach any of it: with no project path the session is
// not a Project source at all. Before this phase the binding was therefore
// unrecoverable, and `projmux delete pane` with no selector failed in every one
// of those panes with "carries no @projmux_pane_uid".

// driftedSessionName is deliberately unequal to the Project root's basename, so
// the fixture exercises the stored status.session.name edge rather than the
// sessionNameFor fallback that happens to agree with it.
const driftedSessionName = "repos-alpha"

// driftedRegistry builds a Project whose two Windows and two Panes exist in the
// registry and are bound to nothing on the machine.
func driftedRegistry(t *testing.T, root string) coremetadata.Registry {
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
		Spec:     coremetadata.ProjectSpec{Root: root},
		// Live is stored false on purpose. The session is plainly live -- it is
		// in the inventory reconcile just read -- and nothing in the adoption
		// path may consult this bool to decide otherwise.
		Status: coremetadata.ProjectStatus{
			Session: &coremetadata.SessionProjection{Name: driftedSessionName, Live: false},
		},
	}}
	reserve("", coremetadata.KindProject, filepath.Base(root), "prj-alpha")

	registry.Windows = []coremetadata.Window{
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
			Metadata: meta("win-first", "lead-roadmap", owner(coremetadata.KindProject, "prj-alpha")),
			Spec:     coremetadata.WindowSpec{PrimaryPaneRef: "pan-first"},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
			Metadata: meta("win-second", "zsh", owner(coremetadata.KindProject, "prj-alpha")),
			Spec:     coremetadata.WindowSpec{PrimaryPaneRef: "pan-second"},
		},
	}
	reserve("prj-alpha", coremetadata.KindWindow, "lead-roadmap", "win-first")
	reserve("prj-alpha", coremetadata.KindWindow, "zsh", "win-second")

	registry.Panes = []coremetadata.Pane{
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: meta("pan-first", "zsh", owner(coremetadata.KindWindow, "win-first")),
			Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: root},
		},
		{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
			Metadata: meta("pan-second", "zsh", owner(coremetadata.KindWindow, "win-second")),
			Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: root},
		},
	}
	reserve("win-first", coremetadata.KindPane, "zsh", "pan-first")
	reserve("win-second", coremetadata.KindPane, "zsh", "pan-second")

	if err := registry.Validate(); err != nil {
		t.Fatalf("drifted fixture is not a valid registry: %v", err)
	}
	return registry
}

// unanchoredObservation returns an observeLegacy stub for a session that has no
// `@projmux_project_path`, reporting whatever uid each live object currently
// carries -- exactly what ObserveLegacySessionTargets does now.
func unanchoredObservation(name string, session *fakeTmuxSession) func(context.Context, string) (coremetadata.LegacySession, intmetadata.LegacyTargets, error) {
	return func(_ context.Context, want string) (coremetadata.LegacySession, intmetadata.LegacyTargets, error) {
		if want != name {
			return coremetadata.LegacySession{Session: want}, intmetadata.LegacyTargets{}, nil
		}
		legacy := coremetadata.LegacySession{Session: name}
		var targets intmetadata.LegacyTargets
		for _, window := range session.windows {
			observed := coremetadata.LegacyWindow{
				Name: window.name,
				UID:  window.opts[tmuxopts.WindowUID],
			}
			var row []string
			for _, pane := range window.panes {
				// The projmux-owned AI options ride along exactly as the real
				// mirror reads them, so a fake pane is an agent pane only when
				// projmux itself marked it as one.
				observed.Panes = append(observed.Panes, coremetadata.LegacyPane{
					Command:   pane.command,
					UID:       pane.opts[tmuxopts.PaneUID],
					Provider:  pane.opts[tmuxopts.AgentProviderPane],
					Topic:     pane.opts[tmuxopts.AgentTopicPane],
					SessionID: pane.opts[tmuxopts.AgentSessionIDPane],
					ThreadID:  pane.opts[tmuxopts.AgentThreadIDPane],
				})
				row = append(row, pane.id)
			}
			legacy.Windows = append(legacy.Windows, observed)
			targets.Windows = append(targets.Windows, window.id)
			targets.Panes = append(targets.Panes, row)
		}
		return legacy, targets, nil
	}
}

func fixtureMutator() coremetadata.Mutator {
	return coremetadata.Mutator{
		Now:       func() time.Time { return resourceFixtureClock },
		NewUID:    coremetadata.NewUID,
		DirExists: intmetadata.DirExists,
	}
}

// TestASingleReconcileReattachesADriftedRegistryToItsLiveSession is acceptance
// criteria 1 and 2 together: after one pass the live objects carry their uids,
// and the pass that repairs the drift is the first one, not the second.
func TestASingleReconcileReattachesADriftedRegistryToItsLiveSession(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	tmux := newFakeTmux()
	session := tmux.addSession(driftedSessionName)
	// One live window, blank of every projmux option -- what a tmux server
	// restart leaves behind.
	window := session.windows[0]
	window.name = "zsh"

	reconciler := newTestReconciler(tmux, []string{root})
	reconciler.observeLegacy = unanchoredObservation(driftedSessionName, session)
	registry := driftedRegistry(t, root)
	windowIdentityBefore := make(map[string]coremetadata.ObjectMeta, len(registry.Windows))
	for _, stored := range registry.Windows {
		windowIdentityBefore[stored.Metadata.UID] = stored.Metadata.Clone()
	}
	reservationsBefore := slices.Clone(registry.NameReservations)

	if err := reconciler.reconcile(context.Background(), &registry, fixtureMutator(), "op-1"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// Ordinal alignment inside the resolved Project: live window 0 pairs with
	// the Project's first Window in creation order.
	if got := window.opts[tmuxopts.WindowUID]; got != "win-first" {
		t.Fatalf("live window uid = %q, want win-first", got)
	}
	if got := window.panes[0].opts[tmuxopts.PaneUID]; got != "pan-first" {
		t.Fatalf("live pane uid = %q, want pan-first", got)
	}
	// The binding write is the existing one, whole: automatic-rename off and the
	// name mirror come with it rather than a uid-only variant.
	if got := window.opts[tmuxopts.AutomaticRenameWindow]; got != "off" {
		t.Fatalf("automatic-rename = %q on a reattached Window, want off", got)
	}
	if got := window.opts[tmuxopts.WindowName]; got != "lead-roadmap" {
		t.Fatalf("mirrored window name = %q, want lead-roadmap", got)
	}
	if got := window.name; got != "zsh" {
		t.Fatalf("runtime window_name = %q, want the observed display name zsh", got)
	}
	projected, _ := registry.Window("win-first")
	if got := projected.Metadata.DisplayName; got != "zsh" {
		t.Fatalf("Window displayName = %q, want observed window_name zsh", got)
	}

	// Nothing was created and nothing was re-identified.
	if len(registry.Windows) != 2 || len(registry.Panes) != 2 {
		t.Fatalf("reattachment changed the topology: %d windows, %d panes", len(registry.Windows), len(registry.Panes))
	}
	for _, uid := range []string{"win-first", "win-second"} {
		after, ok := registry.Window(uid)
		if !ok {
			t.Fatalf("Window %s is gone", uid)
		}
		before := windowIdentityBefore[uid]
		if after.Metadata.UID != before.UID || after.Metadata.Name != before.Name || !reflect.DeepEqual(after.Metadata.OwnerRef, before.OwnerRef) {
			t.Fatalf("Window %s identity changed: before=%+v after=%+v", uid, before, after.Metadata)
		}
	}
	if !reflect.DeepEqual(registry.NameReservations, reservationsBefore) {
		t.Fatalf("display projection changed name reservations:\nbefore=%+v\nafter=%+v", reservationsBefore, registry.NameReservations)
	}

	// And the reattached objects are not conditioned by the observation step
	// that runs after them, while the genuinely unbound ones still are.
	assertRuntimeConditions(t, registry, []string{"win-second", "pan-second"})
}

// TestReattachingADriftedRegistryIsIdempotent is the regression half: a second
// pass over the repaired machine changes neither the registry nor tmux.
func TestReattachingADriftedRegistryIsIdempotent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	tmux := newFakeTmux()
	session := tmux.addSession(driftedSessionName)
	session.windows[0].name = "zsh"

	reconciler := newTestReconciler(tmux, []string{root})
	reconciler.observeLegacy = unanchoredObservation(driftedSessionName, session)
	registry := driftedRegistry(t, root)
	mutator := fixtureMutator()

	if err := reconciler.reconcile(context.Background(), &registry, mutator, "op-1"); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	afterFirst := registry.Clone()
	tmuxAfterFirst := tmux.state()
	tmux.calls = nil

	if err := reconciler.reconcile(context.Background(), &registry, mutator, "op-2"); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if !reflect.DeepEqual(registry, afterFirst) {
		t.Fatalf("a second reconcile changed the registry:\nbefore %+v\nafter  %+v", afterFirst, registry)
	}
	if got := tmux.state(); got != tmuxAfterFirst {
		t.Fatalf("a second reconcile changed tmux:\nbefore\n%s\nafter\n%s", tmuxAfterFirst, got)
	}
	if got := bindingWriteCalls(tmux.calls); got != 0 {
		t.Fatalf("a second reconcile issued %d set-option/rename writes: %v", got, tmux.calls)
	}
}

// TestBindingReapplyRefusesEverySessionItCannotResolveToExactlyOneProject
// covers the scope refusals at the reconciler seam, where the resolution key
// lives.
func TestBindingReapplyRefusesEverySessionItCannotResolveToExactlyOneProject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// projects returns the extra Projects to append to the fixture, and the
		// session name the live session should carry.
		sessionName string
		mutate      func(registry *coremetadata.Registry, root string)
	}{
		{
			// The measured `home` session: live, real, and belonging to no
			// Project, because candidate discovery deliberately excludes the
			// home directory.
			name:        "a session no Project claims",
			sessionName: "home",
		},
		{
			// Two Projects claiming one session name. Guessing which one owns
			// the live windows is exactly the cross-project mistake adoption
			// must never make, so it adopts nothing at all.
			name:        "a session two Projects both claim",
			sessionName: driftedSessionName,
			mutate: func(registry *coremetadata.Registry, root string) {
				registry.Projects = append(registry.Projects, coremetadata.Project{
					APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
					Metadata: coremetadata.ObjectMeta{UID: "prj-twin", Name: "twin", CreatedAt: resourceFixtureClock},
					Spec:     coremetadata.ProjectSpec{Root: filepath.Join(root, "twin")},
					Status: coremetadata.ProjectStatus{
						Session: &coremetadata.SessionProjection{Name: driftedSessionName},
					},
				})
				registry.NameReservations = append(registry.NameReservations, coremetadata.NameReservation{
					Kind: coremetadata.KindProject, Name: "twin", UID: "prj-twin",
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			tmux := newFakeTmux()
			session := tmux.addSession(tt.sessionName)
			window := session.windows[0]

			reconciler := newTestReconciler(tmux, []string{root})
			reconciler.observeLegacy = unanchoredObservation(tt.sessionName, session)
			registry := driftedRegistry(t, root)
			if tt.mutate != nil {
				tt.mutate(&registry, root)
			}

			if err := reconciler.reconcile(context.Background(), &registry, fixtureMutator(), "op-1"); err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if got := window.opts[tmuxopts.WindowUID]; got != "" {
				t.Fatalf("an unresolvable session was bound to %q", got)
			}
			if got := window.panes[0].opts[tmuxopts.PaneUID]; got != "" {
				t.Fatalf("an unresolvable session's pane was bound to %q", got)
			}
		})
	}
}

// TestBindingReapplyNeverMovesAnObjectBetweenProjects is acceptance criterion 4
// at the reconciler seam.
//
// Two Projects, two live sessions, and Window names that collide across them --
// which is the real registry's shape, where `zsh` names a Window in nine
// different Projects. Each session may only ever reach its own Project's
// objects.
func TestBindingReapplyNeverMovesAnObjectBetweenProjects(t *testing.T) {
	t.Parallel()

	alphaRoot := t.TempDir()
	betaRoot := t.TempDir()
	tmux := newFakeTmux()
	alphaSession := tmux.addSession(driftedSessionName)
	betaSession := tmux.addSession("repos-beta")

	reconciler := newTestReconciler(tmux, []string{alphaRoot, betaRoot})
	alphaObserve := unanchoredObservation(driftedSessionName, alphaSession)
	betaObserve := unanchoredObservation("repos-beta", betaSession)
	reconciler.observeLegacy = func(ctx context.Context, name string) (coremetadata.LegacySession, intmetadata.LegacyTargets, error) {
		if name == "repos-beta" {
			return betaObserve(ctx, name)
		}
		return alphaObserve(ctx, name)
	}

	registry := driftedRegistry(t, alphaRoot)
	registry.Projects = append(registry.Projects, coremetadata.Project{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
		Metadata: coremetadata.ObjectMeta{UID: "prj-beta", Name: "beta", CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.ProjectSpec{Root: betaRoot},
		Status: coremetadata.ProjectStatus{
			Session: &coremetadata.SessionProjection{Name: "repos-beta"},
		},
	})
	registry.Windows = append(registry.Windows, coremetadata.Window{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
		Metadata: coremetadata.ObjectMeta{
			UID: "win-beta", Name: "zsh", CreatedAt: resourceFixtureClock,
			OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindProject, UID: "prj-beta"},
		},
		Spec: coremetadata.WindowSpec{PrimaryPaneRef: "pan-beta"},
	})
	registry.Panes = append(registry.Panes, coremetadata.Pane{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
		Metadata: coremetadata.ObjectMeta{
			UID: "pan-beta", Name: "zsh", CreatedAt: resourceFixtureClock,
			OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: "win-beta"},
		},
		Spec: coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: betaRoot},
	})
	registry.NameReservations = append(registry.NameReservations,
		coremetadata.NameReservation{Kind: coremetadata.KindProject, Name: "beta", UID: "prj-beta"},
		coremetadata.NameReservation{Scope: "prj-beta", Kind: coremetadata.KindWindow, Name: "zsh", UID: "win-beta"},
		coremetadata.NameReservation{Scope: "win-beta", Kind: coremetadata.KindPane, Name: "zsh", UID: "pan-beta"},
	)
	if err := registry.Validate(); err != nil {
		t.Fatalf("two-Project fixture is not a valid registry: %v", err)
	}

	if err := reconciler.reconcile(context.Background(), &registry, fixtureMutator(), "op-1"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if got := alphaSession.windows[0].opts[tmuxopts.WindowUID]; got != "win-first" {
		t.Fatalf("alpha's live window = %q, want win-first", got)
	}
	if got := betaSession.windows[0].opts[tmuxopts.WindowUID]; got != "win-beta" {
		t.Fatalf("beta's live window = %q, want win-beta", got)
	}
	if got := alphaSession.windows[0].panes[0].opts[tmuxopts.PaneUID]; got != "pan-first" {
		t.Fatalf("alpha's live pane = %q, want pan-first", got)
	}
	if got := betaSession.windows[0].panes[0].opts[tmuxopts.PaneUID]; got != "pan-beta" {
		t.Fatalf("beta's live pane = %q, want pan-beta", got)
	}
}

// TestBindingReapplyLeavesAForeignUIDAlone pins the refusal that keeps the
// repair step from re-identifying anything it finds.
//
// A uid the registry does not know is not a blank, so no existing registry
// Window is pointed at it -- and the repair step creates nothing either, so the
// live window keeps exactly the uid it had and none of its panes are
// considered. (The anchored import path does mint for an unknown uid; it has
// the project anchor that makes minting safe. This step does not.)
func TestBindingReapplyLeavesAForeignUIDAlone(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	tmux := newFakeTmux()
	session := tmux.addSession(driftedSessionName)
	window := session.windows[0]
	window.opts[tmuxopts.WindowUID] = "win-from-another-machine"

	reconciler := newTestReconciler(tmux, []string{root})
	reconciler.observeLegacy = unanchoredObservation(driftedSessionName, session)
	registry := driftedRegistry(t, root)

	if err := reconciler.reconcile(context.Background(), &registry, fixtureMutator(), "op-1"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := window.opts[tmuxopts.WindowUID]; got != "win-from-another-machine" {
		t.Fatalf("a foreign uid was rewritten to %q", got)
	}
	if got := window.panes[0].opts[tmuxopts.PaneUID]; got != "" {
		t.Fatalf("a refused window still contributed a pane binding: %q", got)
	}
	if len(registry.Windows) != 2 {
		t.Fatalf("windows = %d, want 2; a foreign uid must not mint a Window", len(registry.Windows))
	}
}

// TestBindingReapplyNeverStealsABindingFromAnotherLiveWindow pins the "already
// bound elsewhere" refusal.
//
// Project alpha's first Window is the runtime of a live tmux window in a
// session this pass has not reached yet. A blank live window must not take it;
// the ordinal walk moves on to the next unbound candidate instead.
func TestBindingReapplyNeverStealsABindingFromAnotherLiveWindow(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	tmux := newFakeTmux()
	session := tmux.addSession(driftedSessionName)
	blank := session.windows[0]
	// A second live window in the same session, already bound to win-first.
	// The pre-pass inventory sees it, so win-first is off the table.
	bound := &fakeTmuxWindow{id: tmux.mint("@"), name: "held", opts: map[string]string{tmuxopts.WindowUID: "win-first"}}
	bound.panes = append(bound.panes, &fakeTmuxPane{id: tmux.mint("%"), opts: map[string]string{tmuxopts.PaneUID: "pan-first"}})
	session.windows = append(session.windows, bound)

	reconciler := newTestReconciler(tmux, []string{root})
	reconciler.observeLegacy = unanchoredObservation(driftedSessionName, session)
	registry := driftedRegistry(t, root)

	if err := reconciler.reconcile(context.Background(), &registry, fixtureMutator(), "op-1"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := bound.opts[tmuxopts.WindowUID]; got != "win-first" {
		t.Fatalf("the already-bound window lost its binding: %q", got)
	}
	if got := blank.opts[tmuxopts.WindowUID]; got != "win-second" {
		t.Fatalf("the blank window took %q, want win-second rather than a stolen win-first", got)
	}
}

// TestReconcileToleratesABindingWriteThatFails keeps the repair step from
// failing the operation that happened to trigger it.
//
// It is maintenance riding along inside somebody else's transaction. A tmux
// window that disappeared between the observation and the write must not turn a
// `create pane` into an error; the next pass observes the same drift and tries
// again.
func TestReconcileToleratesABindingWriteThatFails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	tmux := newFakeTmux()
	session := tmux.addSession(driftedSessionName)
	tmux.fail = []string{"set-option", tmuxopts.WindowUID}
	tmux.failAlways = true

	reconciler := newTestReconciler(tmux, []string{root})
	reconciler.observeLegacy = unanchoredObservation(driftedSessionName, session)
	registry := driftedRegistry(t, root)

	if err := reconciler.reconcile(context.Background(), &registry, fixtureMutator(), "op-1"); err != nil {
		t.Fatalf("a failed binding write failed the whole reconcile: %v", err)
	}
	if got := session.windows[0].opts[tmuxopts.WindowUID]; got != "" {
		t.Fatalf("the failed write landed anyway: %q", got)
	}
}
