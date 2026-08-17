package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// The fixture in this file extends the drifted machine of the binding-reapply
// suite with the one shape adoption cannot repair: a live tmux pane that has no
// registry Pane to be adopted into at all.
//
// It is the measured state, not an invented one. Panes created outside the
// Registry have no resource counterpart; on the measured socket
// one live pane out of seven carried a binding, and the operator's own active
// pane was not it. `projmux delete pane` with no selector therefore refused with
// "carries no @projmux_pane_uid" in exactly the pane the operator was sitting
// in.

// addLivePane appends one more live pane to a fake tmux window, carrying
// whatever projmux options the case is about.
func addLivePane(tmux *fakeTmux, window *fakeTmuxWindow, command string, opts map[string]string) *fakeTmuxPane {
	if opts == nil {
		opts = map[string]string{}
	}
	pane := &fakeTmuxPane{id: tmux.mint("%"), opts: opts, command: command}
	window.panes = append(window.panes, pane)
	return pane
}

// newPaneUIDs returns the uids of the Panes a pass added, in registry order.
func newPaneUIDs(before, after coremetadata.Registry) []string {
	existing := map[string]bool{}
	for _, pane := range before.Panes {
		existing[pane.Metadata.UID] = true
	}
	var added []string
	for _, pane := range after.Panes {
		if !existing[pane.Metadata.UID] {
			added = append(added, pane.Metadata.UID)
		}
	}
	return added
}

// TestReconcileRegistersAnOrphanLivePaneAndMirrorsItsUID is the phase, end to
// end at the reconciler seam: a live pane with no registry counterpart comes out
// of one pass as a registry Pane whose uid the tmux pane mirrors, which is what
// makes the active pane resolvable at all.
func TestReconcileRegistersAnOrphanLivePaneAndMirrorsItsUID(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	tmux := newFakeTmux()
	session := tmux.addSession(driftedSessionName)
	window := session.windows[0]
	window.name = "zsh"
	// The second live pane of an already-paired window. `win-first` owns exactly
	// one registry Pane, so ordinal alignment runs out here and Phase 1 left this
	// pane unbound forever.
	orphan := addLivePane(tmux, window, "claude", nil)

	reconciler := newTestReconciler(tmux, []string{root})
	reconciler.observeLegacy = unanchoredObservation(driftedSessionName, session)
	registry := driftedRegistry(t, root)
	mutator := fixtureMutator()

	if err := reconciler.reconcile(context.Background(), &registry, mutator, "op-1"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// The pane that did have a counterpart is still adopted, not displaced by
	// the new create path.
	if got := window.panes[0].opts[tmuxopts.PaneUID]; got != "pan-first" {
		t.Fatalf("the paired pane = %q, want pan-first", got)
	}

	uid := orphan.opts[tmuxopts.PaneUID]
	if uid == "" {
		t.Fatalf("the orphan live pane still carries no @projmux_pane_uid")
	}
	pane, ok := registry.Pane(uid)
	if !ok {
		t.Fatalf("the mirrored uid %q names no registry Pane", uid)
	}
	if owner := pane.Metadata.OwnerUID(); owner != "win-first" {
		t.Fatalf("the registered Pane is owned by %q, want the already-paired Window win-first", owner)
	}
	if pane.Spec.Role != coremetadata.PaneRoleShell {
		t.Fatalf("the registered Pane role = %q, want %q", pane.Spec.Role, coremetadata.PaneRoleShell)
	}
	// The name is the fallback base, not the `claude` the pane happens to be
	// running: metadata.name is never derived from a runtime attribute.
	if pane.Metadata.Name != coremetadata.FallbackPaneNameBase {
		t.Fatalf("the registered Pane name = %q, want %q", pane.Metadata.Name, coremetadata.FallbackPaneNameBase)
	}
	// The binding write is the existing whole one, so the name mirror comes with
	// the uid rather than a uid-only variant.
	if got := orphan.opts[tmuxopts.PaneName]; got != coremetadata.FallbackPaneNameBase {
		t.Fatalf("mirrored pane name = %q, want %q", got, coremetadata.FallbackPaneNameBase)
	}
	if len(registry.Agents) != 0 {
		t.Fatalf("registering a pane running an AI agent minted %d Agents, want 0", len(registry.Agents))
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("registry does not validate after registering an orphan pane: %v", err)
	}
	// The pane this pass bound is not then conditioned by the observation step
	// that runs after it; the genuinely unbound objects still are.
	assertRuntimeConditions(t, registry, []string{"win-second", "pan-second"})

	// A second pass finds the pane bound and registers nothing more. Without the
	// Claim inside the create path this is where a duplicate would appear.
	afterFirst := registry.Clone()
	tmuxAfterFirst := tmux.state()
	if err := reconciler.reconcile(context.Background(), &registry, mutator, "op-2"); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if !reflect.DeepEqual(registry, afterFirst) {
		t.Fatalf("a second reconcile changed the registry:\nbefore %+v\nafter  %+v", afterFirst, registry)
	}
	if got := tmux.state(); got != tmuxAfterFirst {
		t.Fatalf("a second reconcile changed tmux:\nbefore\n%s\nafter\n%s", tmuxAfterFirst, got)
	}
}

// TestOrphanPaneRegistrationRulesAndRefusals is the registration rule as a
// table: exactly which live panes become registry Panes, and every way the
// answer is "leave it alone".
//
// Anything ambiguous refuses. A refusal here is not a missed opportunity -- it
// means a real registry object sits on the other side of the ambiguity, so
// registering would leave two registry Panes describing one tmux pane, and no
// later pass could tell which one to drop.
func TestOrphanPaneRegistrationRulesAndRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// sessionName is the live tmux session; empty means the drifted session
		// the Project actually claims.
		sessionName string
		// arrange shapes the live tmux side of the case.
		arrange func(tmux *fakeTmux, session *fakeTmuxSession)
		// mutate shapes the registry side.
		mutate func(registry *coremetadata.Registry, root string)
		// wantRegistered is how many Panes the pass may add.
		wantRegistered int
	}{
		{
			// The measured case. The Window is paired, its one registry Pane is
			// taken by the first live pane, and the second has nothing left to
			// adopt.
			name: "a blank pane inside a paired Window with no candidate left",
			arrange: func(tmux *fakeTmux, session *fakeTmuxSession) {
				addLivePane(tmux, session.windows[0], "claude", nil)
			},
			wantRegistered: 1,
		},
		{
			// A uid nothing in the registry knows. Never adopted -- pointing an
			// existing Pane at it would be re-identification off a failed lookup
			// -- but registered, because projmux itself produces unknown uids
			// when a reconcile is rolled back after its tmux writes landed, and
			// stranding those panes forever is the worse answer.
			name: "a pane carrying a uid the registry has never heard of",
			arrange: func(tmux *fakeTmux, session *fakeTmuxSession) {
				addLivePane(tmux, session.windows[0], "zsh", map[string]string{
					tmuxopts.PaneUID: "pane-from-another-machine",
				})
			},
			wantRegistered: 1,
		},
		{
			// The uid belongs to a sibling Window. Claiming it would take a
			// binding that is genuinely somebody else's, and registering a
			// second Pane beside it would double-describe the tmux pane.
			name: "a pane carrying a uid a sibling Window owns",
			arrange: func(tmux *fakeTmux, session *fakeTmuxSession) {
				addLivePane(tmux, session.windows[0], "zsh", map[string]string{
					tmuxopts.PaneUID: "pan-second",
				})
			},
			wantRegistered: 0,
		},
		{
			// Two live panes carrying one registry Pane's uid. Neither wins twice,
			// and the loser is not handed a fresh Pane either: the ambiguity is
			// about which tmux pane `pan-first` already describes, and minting
			// beside it would answer that question by guessing.
			name: "two panes carrying one registry Pane's uid",
			arrange: func(tmux *fakeTmux, session *fakeTmuxSession) {
				session.windows[0].panes[0].opts[tmuxopts.PaneUID] = "pan-first"
				addLivePane(tmux, session.windows[0], "zsh", map[string]string{
					tmuxopts.PaneUID: "pan-first",
				})
			},
			wantRegistered: 0,
		},
		{
			// The Window layer is unchanged by this phase: a window the pass
			// refused contributes none of its panes, so nothing under it is
			// registered either.
			name: "a pane under a Window carrying a foreign uid",
			arrange: func(tmux *fakeTmux, session *fakeTmuxSession) {
				session.windows[0].opts[tmuxopts.WindowUID] = "win-from-another-machine"
				addLivePane(tmux, session.windows[0], "claude", nil)
			},
			wantRegistered: 0,
		},
		{
			// A third live window has no registry Window left to pair with, and
			// this path never creates a Window. With no paired Window there is
			// no owner to register a Pane under, so its panes are not candidates.
			name: "a pane under a live window with no registry Window left",
			arrange: func(tmux *fakeTmux, session *fakeTmuxSession) {
				for range 2 {
					extra := &fakeTmuxWindow{id: tmux.mint("@"), name: "zsh", opts: map[string]string{}}
					addLivePane(tmux, extra, "claude", nil)
					session.windows = append(session.windows, extra)
				}
			},
			wantRegistered: 0,
		},
		{
			// The measured `home` session: live, real, and claimed by no Project
			// at all, because candidate discovery excludes the home directory.
			name:        "a session no Project claims",
			sessionName: "home",
			arrange: func(tmux *fakeTmux, session *fakeTmuxSession) {
				addLivePane(tmux, session.windows[0], "claude", nil)
			},
			wantRegistered: 0,
		},
		{
			// Two Projects claiming one session name. Guessing which one owns the
			// live panes is the cross-project mistake no later pass can undo, so
			// the scope resolves to nothing and registers nothing.
			name: "a session two Projects both claim",
			arrange: func(tmux *fakeTmux, session *fakeTmuxSession) {
				addLivePane(tmux, session.windows[0], "claude", nil)
			},
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
			wantRegistered: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sessionName := tt.sessionName
			if sessionName == "" {
				sessionName = driftedSessionName
			}
			root := t.TempDir()
			tmux := newFakeTmux()
			session := tmux.addSession(sessionName)
			session.windows[0].name = "zsh"
			tt.arrange(tmux, session)

			reconciler := newTestReconciler(tmux, []string{root})
			reconciler.observeLegacy = unanchoredObservation(sessionName, session)
			registry := driftedRegistry(t, root)
			if tt.mutate != nil {
				tt.mutate(&registry, root)
			}
			before := registry.Clone()

			if err := reconciler.reconcile(context.Background(), &registry, fixtureMutator(), "op-1"); err != nil {
				t.Fatalf("reconcile: %v", err)
			}

			added := newPaneUIDs(before, registry)
			if len(added) != tt.wantRegistered {
				t.Fatalf("registered %d Panes %v, want %d", len(added), added, tt.wantRegistered)
			}
			for _, uid := range added {
				pane, ok := registry.Pane(uid)
				if !ok {
					t.Fatalf("registered uid %q is not in the registry", uid)
				}
				// Every registration lands under a Window that was paired this
				// pass, which is what closes the Project boundary structurally.
				window, ok := registry.Window(pane.Metadata.OwnerUID())
				if !ok {
					t.Fatalf("registered Pane %q is owned by %q, which is not a Window", uid, pane.Metadata.OwnerUID())
				}
				if window.Metadata.OwnerUID() != "prj-alpha" {
					t.Fatalf("registered Pane %q landed in Project %q, want prj-alpha", uid, window.Metadata.OwnerUID())
				}
			}
			if err := registry.Validate(); err != nil {
				t.Fatalf("registry does not validate after the pass: %v", err)
			}
		})
	}
}

// TestOrphanPaneRegistrationNeverCrossesAProjectBoundary is acceptance criterion
// 3: cross-project registration is zero.
//
// Two Projects, two live sessions, and an orphan pane in each. The names collide
// across the Projects on purpose -- the real registry carries the Window name
// `zsh` in nine of them -- so a registration that resolved by name rather than by
// scope would land in the wrong Project and be caught here.
func TestOrphanPaneRegistrationNeverCrossesAProjectBoundary(t *testing.T) {
	t.Parallel()

	alphaRoot := t.TempDir()
	betaRoot := t.TempDir()
	tmux := newFakeTmux()
	alphaSession := tmux.addSession(driftedSessionName)
	betaSession := tmux.addSession("repos-beta")
	alphaOrphan := addLivePane(tmux, alphaSession.windows[0], "claude", nil)
	betaOrphan := addLivePane(tmux, betaSession.windows[0], "claude", nil)

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
	before := registry.Clone()

	if err := reconciler.reconcile(context.Background(), &registry, fixtureMutator(), "op-1"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// One registration per Project, each under that Project's own Window.
	wantOwner := map[string]string{
		alphaOrphan.opts[tmuxopts.PaneUID]: "win-first",
		betaOrphan.opts[tmuxopts.PaneUID]:  "win-beta",
	}
	added := newPaneUIDs(before, registry)
	if len(added) != 2 {
		t.Fatalf("registered %d Panes %v, want one per Project", len(added), added)
	}
	for _, uid := range added {
		want, ok := wantOwner[uid]
		if !ok {
			t.Fatalf("registered Pane %q is mirrored onto no live pane of either session", uid)
		}
		pane, _ := registry.Pane(uid)
		if got := pane.Metadata.OwnerUID(); got != want {
			t.Fatalf("registered Pane %q is owned by Window %q, want %q -- it crossed a Project boundary", uid, got, want)
		}
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("registry does not validate after the pass: %v", err)
	}
}

// TestOrphanPaneRegistrationReIdentifiesAndDeletesNothing is acceptance
// criterion 4, as a diff audit rather than a spot check.
//
// The identity of every object that existed before the pass -- its uid, name,
// owner, and spec -- must come out byte-identical, and every name reservation
// must survive. Status is deliberately excluded: it is an observation, and this
// pass is supposed to change it. This phase only ever adds.
func TestOrphanPaneRegistrationReIdentifiesAndDeletesNothing(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	tmux := newFakeTmux()
	session := tmux.addSession(driftedSessionName)
	session.windows[0].name = "zsh"
	addLivePane(tmux, session.windows[0], "claude", nil)

	reconciler := newTestReconciler(tmux, []string{root})
	reconciler.observeLegacy = unanchoredObservation(driftedSessionName, session)
	registry := driftedRegistry(t, root)
	before := identitySnapshot(registry)

	if err := reconciler.reconcile(context.Background(), &registry, fixtureMutator(), "op-1"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	after := identitySnapshot(registry)

	for uid, want := range before {
		got, ok := after[uid]
		if !ok {
			t.Fatalf("%s was deleted by a pass that may only add", uid)
		}
		if got != want {
			t.Fatalf("%s was re-identified:\nbefore %s\nafter  %s", uid, want, got)
		}
	}
	// And the additions are additions: strictly more objects, never fewer.
	if len(after) <= len(before) {
		t.Fatalf("the pass registered nothing; the fixture no longer exercises the phase")
	}

	wantReservations := []string{"zsh", "lead-roadmap", "zsh", "zsh"}
	slices.Sort(wantReservations)
	var gotReservations []string
	for _, reservation := range registry.NameReservations {
		if reservation.Kind == coremetadata.KindWindow || reservation.Kind == coremetadata.KindPane {
			gotReservations = append(gotReservations, reservation.Name)
		}
	}
	slices.Sort(gotReservations)
	// Every pre-existing reservation is still held; the only difference is the
	// one the registration allocated.
	if len(gotReservations) != len(wantReservations)+1 {
		t.Fatalf("Window/Pane name reservations = %v, want the original %v plus exactly one", gotReservations, wantReservations)
	}
	for _, name := range wantReservations {
		if !slices.Contains(gotReservations, name) {
			t.Fatalf("reservation %q was released; a vanished or re-identified object is the only way that happens", name)
		}
	}
}

// TestReconcileToleratesAnOrphanPaneItCannotRegister keeps the new create path
// inside the tolerance the rest of this step already has.
//
// This is maintenance riding along inside somebody else's transaction: the
// operator ran `create pane`, and a reconcile that cannot register some
// unrelated orphan must not turn that into an error. Everything the pass could
// do, it still does.
func TestReconcileToleratesAnOrphanPaneItCannotRegister(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	tmux := newFakeTmux()
	session := tmux.addSession(driftedSessionName)
	session.windows[0].name = "zsh"
	orphan := addLivePane(tmux, session.windows[0], "claude", nil)

	reconciler := newTestReconciler(tmux, []string{root})
	reconciler.observeLegacy = unanchoredObservation(driftedSessionName, session)
	registry := driftedRegistry(t, root)

	mutator := fixtureMutator()
	mutator.NewUID = func(kind coremetadata.Kind) (string, error) {
		if kind == coremetadata.KindPane {
			return "", errors.New("uid source is unavailable")
		}
		return coremetadata.NewUID(kind)
	}

	if err := reconciler.reconcile(context.Background(), &registry, mutator, "op-1"); err != nil {
		t.Fatalf("a failed orphan registration failed the whole reconcile: %v", err)
	}
	if len(registry.Panes) != 2 {
		t.Fatalf("panes = %d, want the 2 the fixture started with", len(registry.Panes))
	}
	if got := orphan.opts[tmuxopts.PaneUID]; got != "" {
		t.Fatalf("a failed registration still mirrored %q", got)
	}
	// The work that did succeed is kept, not rolled back with the failure.
	if got := session.windows[0].panes[0].opts[tmuxopts.PaneUID]; got != "pan-first" {
		t.Fatalf("the paired pane = %q, want pan-first", got)
	}
}

// identitySnapshot renders the identity-bearing fields of every resource, keyed
// by uid. Status is excluded on purpose: it is the one thing reconcile is
// supposed to rewrite.
func identitySnapshot(registry coremetadata.Registry) map[string]string {
	out := map[string]string{}
	for _, project := range registry.Projects {
		out["project/"+project.Metadata.UID] = fmt.Sprintf("name=%s root=%s", project.Metadata.Name, project.Spec.Root)
	}
	for _, window := range registry.Windows {
		out["window/"+window.Metadata.UID] = fmt.Sprintf("name=%s owner=%s spec=%+v", window.Metadata.Name, window.Metadata.OwnerUID(), window.Spec)
	}
	for _, pane := range registry.Panes {
		out["pane/"+pane.Metadata.UID] = fmt.Sprintf("name=%s owner=%s spec=%+v", pane.Metadata.Name, pane.Metadata.OwnerUID(), pane.Spec)
	}
	for _, agent := range registry.Agents {
		out["agent/"+agent.Metadata.UID] = fmt.Sprintf("name=%s owner=%s spec=%+v", agent.Metadata.Name, agent.Metadata.OwnerUID(), agent.Spec)
	}
	return out
}
