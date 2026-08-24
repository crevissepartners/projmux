package app

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/core/sessionstate"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

const (
	correctiveAControlUID     = "ctl-fberkuk3qs7vemdgmflduipcua"
	correctiveAProjectUID     = "proj-ej7scn4oskbt445d5hfna66omi"
	correctiveAProjectRoot    = "/home/es5h"
	correctiveAProjectSession = "home--" + correctiveAProjectUID
)

func addCorrectiveAIncidentRegistry(t *testing.T, registry *coremetadata.Registry) {
	t.Helper()
	owner := func(kind coremetadata.Kind, uid string) *coremetadata.OwnerRef {
		return &coremetadata.OwnerRef{Kind: kind, UID: uid}
	}
	meta := func(uid, name string, ownerRef *coremetadata.OwnerRef) coremetadata.ObjectMeta {
		return coremetadata.ObjectMeta{UID: uid, Name: name, OwnerRef: ownerRef, CreatedAt: resourceFixtureClock}
	}
	reserve := func(scope string, kind coremetadata.Kind, name, uid string) {
		registry.NameReservations = append(registry.NameReservations, coremetadata.NameReservation{Scope: scope, Kind: kind, Name: name, UID: uid})
	}

	registry.ControlSessions = append(registry.ControlSessions, coremetadata.ControlSession{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindControlSession,
		Metadata: meta(correctiveAControlUID, "home", nil),
		Spec:     coremetadata.ControlSessionSpec{Session: "home"},
	})
	reserve("", coremetadata.KindControlSession, "home", correctiveAControlUID)
	registry.Windows = append(registry.Windows, coremetadata.Window{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
		Metadata: meta("win-home-control", "control", owner(coremetadata.KindControlSession, correctiveAControlUID)),
		Spec:     coremetadata.WindowSpec{AnchorPaneRef: "pane-home-control"},
	})
	reserve(correctiveAControlUID, coremetadata.KindWindow, "control", "win-home-control")
	registry.Panes = append(registry.Panes, coremetadata.Pane{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
		Metadata: meta("pane-home-control", "zsh", owner(coremetadata.KindWindow, "win-home-control")),
		Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: correctiveAProjectRoot},
	})
	reserve("win-home-control", coremetadata.KindPane, "zsh", "pane-home-control")

	registry.Projects = append(registry.Projects, coremetadata.Project{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
		Metadata: meta(correctiveAProjectUID, "home-project", nil),
		Spec:     coremetadata.ProjectSpec{Root: correctiveAProjectRoot, PrimaryWindowRef: "win-home-project"},
		Status:   coremetadata.ProjectStatus{Session: &coremetadata.SessionProjection{Name: "home", Live: false}},
	})
	reserve("", coremetadata.KindProject, "home-project", correctiveAProjectUID)
	registry.Windows = append(registry.Windows, coremetadata.Window{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
		Metadata: meta("win-home-project", "project", owner(coremetadata.KindProject, correctiveAProjectUID)),
		Spec:     coremetadata.WindowSpec{AnchorPaneRef: "pane-home-project"},
	})
	reserve(correctiveAProjectUID, coremetadata.KindWindow, "project", "win-home-project")
	registry.Panes = append(registry.Panes, coremetadata.Pane{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
		Metadata: meta("pane-home-project", "zsh", owner(coremetadata.KindWindow, "win-home-project")),
		Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: correctiveAProjectRoot},
	})
	reserve("win-home-project", coremetadata.KindPane, "zsh", "pane-home-project")
	*registry = registry.Normalize()
	if err := registry.Validate(); err != nil {
		t.Fatalf("Corrective A incident fixture is invalid: %v", err)
	}
}

func seedCorrectiveAHomeRuntime(t *testing.T, tmux *fakeTmux) *fakeTmuxSession {
	t.Helper()
	home := tmux.addSession("home")
	home.windows[0].opts[tmuxopts.WindowUID] = "win-home-control"
	home.windows[0].panes[0].opts[tmuxopts.PaneUID] = "pane-home-control"
	return home
}

func correctiveAReconciler(runner tmuxCommandRunner, sessions sessionLister) *registryReconciler {
	mirror := intmetadata.NewMirror(runner)
	return &registryReconciler{
		liveSessions:  sessions.ExistingSessions,
		observeLegacy: mirror.ObserveLegacySessionTargets,
		mirror:        mirror,
		mirrorProject: func(context.Context, string, coremetadata.Project) error { return nil },
		mirrorWindow:  mirror.MirrorWindow,
		mirrorPane: func(ctx context.Context, target, _ string, pane coremetadata.Pane) error {
			return mirror.MirrorPane(ctx, target, pane)
		},
		shell: "/bin/zsh",
		sessionNameFor: func(root string) string {
			if root == correctiveAProjectRoot {
				return "home"
			}
			return strings.TrimPrefix(root, "/srv/")
		},
	}
}

func TestRegistryReconcilerConstructorAlwaysInitializesRefusalBookkeeping(t *testing.T) {
	tmux := newFakeTmux()
	r := newRegistryReconciler(tmux, inttmux.NewClient(tmux))
	if r.refusedSessions == nil || r.refusedSessionDivergence == nil || r.refusedSessionReasons == nil || r.exactProjects == nil {
		t.Fatalf("constructor left refusal bookkeeping nil: %+v", r)
	}
	// An injected planner factory is allowed to return a literal. The planner
	// boundary must restore the same invariant before any per-session write.
	injected := &registryReconciler{}
	injected.initializeRefusalBookkeeping()
	injected.refuseSession("foreign", resourcegraph.DivergenceContaminated, "foreign fixture")
	if !injected.refusedSessions["foreign"] || injected.refusedSessionDivergence["foreign"] != resourcegraph.DivergenceContaminated {
		t.Fatalf("injected reconciler refusal = %#v / %#v", injected.refusedSessions, injected.refusedSessionDivergence)
	}
}

func TestHomeDualRootClaimantPrecedenceAndPhysicalAllocation(t *testing.T) {
	registry := coremetadata.NewRegistry()
	addCorrectiveAIncidentRegistry(t, &registry)
	r := &registryReconciler{sessionNameFor: func(string) string { return "home" }}

	claim := r.resolveRegistrySessionClaim(registry, "home")
	if claim.Kind != registrySessionClaimControl || claim.UID != correctiveAControlUID {
		t.Fatalf("home claim = %+v, want exact ControlSession", claim)
	}
	if uid := r.projectsBySessionName(registry)["home"]; uid != "" {
		t.Fatalf("Project session-name fallback crossed exact ControlSession claim: %q", uid)
	}
	project, _ := registry.Project(correctiveAProjectUID)
	if got := r.projectPhysicalSessionName(registry, *project); got != correctiveAProjectSession {
		t.Fatalf("collision-safe Project session = %q, want %q", got, correctiveAProjectSession)
	}
	if err := sessionstate.ValidateSessionName(correctiveAProjectSession); err != nil {
		t.Fatalf("full-UID collision suffix violates existing tmux session-name rules: %v", err)
	}
	project.Status.Session = &coremetadata.SessionProjection{Name: correctiveAProjectSession, Live: false}
	if got := r.projectPhysicalSessionName(registry, *project); got != correctiveAProjectSession {
		t.Fatalf("repeat allocation churned %q to %q", correctiveAProjectSession, got)
	}

	contaminated := []observedResourceProjectSession{{id: "$1", name: "home", uid: correctiveAProjectUID, root: correctiveAProjectRoot}}
	refused := refusedResourceProjectSessions(registry, contaminated, r)
	if !refused["home"] || r.refusedSessionDivergence["home"] != resourcegraph.DivergenceContaminated || !strings.Contains(r.refusedSessionReasons["home"], "ControlSession") {
		t.Fatalf("contaminated control observation was not a typed D4 refusal: refused=%v divergence=%q reason=%q", refused, r.refusedSessionDivergence["home"], r.refusedSessionReasons["home"])
	}
}

func TestHomeDualRootClaimResolutionIsRegistryOrderIndependent(t *testing.T) {
	registry := coremetadata.NewRegistry()
	addCorrectiveAIncidentRegistry(t, &registry)
	project, _ := registry.Project(correctiveAProjectUID)

	orders := []coremetadata.Registry{registry.Clone(), registry.Clone()}
	for left, right := 0, len(orders[1].Projects)-1; left < right; left, right = left+1, right-1 {
		orders[1].Projects[left], orders[1].Projects[right] = orders[1].Projects[right], orders[1].Projects[left]
	}
	for left, right := 0, len(orders[1].ControlSessions)-1; left < right; left, right = left+1, right-1 {
		orders[1].ControlSessions[left], orders[1].ControlSessions[right] = orders[1].ControlSessions[right], orders[1].ControlSessions[left]
	}

	for index, ordered := range orders {
		r := &registryReconciler{sessionNameFor: func(string) string { return "home" }}
		claim := r.resolveRegistrySessionClaim(ordered, "home")
		if claim.Kind != registrySessionClaimControl || claim.UID != correctiveAControlUID {
			t.Fatalf("order %d claim = %+v, want exact ControlSession", index, claim)
		}
		if got := r.projectPhysicalSessionName(ordered, *project); got != correctiveAProjectSession {
			t.Fatalf("order %d physical Project session = %q, want %q", index, got, correctiveAProjectSession)
		}
	}
}

func TestHomeDualRootIncidentDoesNotBlockUnrelatedCreateAgent(t *testing.T) {
	store := newFakeResourceStore(t)
	addCorrectiveAIncidentRegistry(t, &store.registry)
	tmux := newFakeTmux()
	home := seedCorrectiveAHomeRuntime(t, tmux)
	homeWindow, homePane := home.windows[0], home.windows[0].panes[0]
	controlBefore, projectBefore := store.registry.ControlSessions[len(store.registry.ControlSessions)-1].Clone(), func() coremetadata.Project {
		project, _ := store.registry.Project(correctiveAProjectUID)
		return project.Clone()
	}()

	create, launcher := newTestAgentCreateCommand(t, store, tmux)
	create.reconciler = correctiveAReconciler(tmux, inttmux.NewClient(tmux))
	create.runtime.sessions = &fakeSessionMaterializer{tmux: tmux}
	stdout, _, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha", "--window", "main")
	if err != nil || !strings.Contains(stdout, "agent/") {
		t.Fatalf("unrelated create agent failed: stdout=%q err=%v", stdout, err)
	}
	if len(launcher.bound) != 1 {
		t.Fatalf("unrelated create bound %d Agent panes, want 1", len(launcher.bound))
	}
	controlAfter, ok := store.registry.ControlSession(correctiveAControlUID)
	if !ok || !reflect.DeepEqual(controlBefore, *controlAfter) {
		t.Fatalf("Home ControlSession changed or disappeared: before=%+v after=%+v", controlBefore, controlAfter)
	}
	projectAfter, ok := store.registry.Project(correctiveAProjectUID)
	if !ok || projectAfter.Spec.Root != projectBefore.Spec.Root || projectAfter.Metadata.UID != projectBefore.Metadata.UID || len(store.registry.WindowsOf(correctiveAProjectUID)) != 1 {
		t.Fatalf("Home Project identity/descendants were not preserved: %+v", projectAfter)
	}
	if projectAfter.Status.Session == nil || projectAfter.Status.Session.Name != correctiveAProjectSession || projectAfter.Status.Session.Live {
		t.Fatalf("Home Project collision projection = %+v", projectAfter.Status.Session)
	}
	if homeWindow.opts[tmuxopts.WindowUID] != "win-home-control" || homePane.opts[tmuxopts.PaneUID] != "pane-home-control" {
		t.Fatalf("Home runtime was cross-kind rewritten: window=%q pane=%q", homeWindow.opts[tmuxopts.WindowUID], homePane.opts[tmuxopts.PaneUID])
	}
}

func TestHomeProjectExplicitMaterializationUsesCollisionSafeSessionAndRepeatsNoop(t *testing.T) {
	store := newFakeResourceStore(t)
	addCorrectiveAIncidentRegistry(t, &store.registry)
	server := newFakeTmux()
	server.socketPath = "/tmp/fake-tmux/corrective-a"
	home := seedCorrectiveAHomeRuntime(t, server)
	homeBefore := server.state()
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00corrective-a": server}}
	sessions := &fakeSessionMaterializer{tmux: server}
	command := &resourceReconcileCommand{
		runner: runner, resources: store.store(), lookupEnv: func(string) string { return "" },
		newReconciler:  correctiveAReconciler,
		newOperationID: func() (string, error) { return "op-corrective-a", nil },
		newMaterializer: func(exact tmuxCommandRunner, warn io.Writer) *materializer {
			return &materializer{runner: exact, mirror: intmetadata.NewMirror(exact), sessions: sessions, warn: warn}
		},
	}

	out, _, err := runReconcile(t, command, "resources", "--socket", "corrective-a", "--materialize-project", "uid:"+correctiveAProjectUID, "-o", "json")
	if err != nil {
		t.Fatalf("explicit Home Project materialization failed: %v\n%s", err, out)
	}
	materialized := server.session(correctiveAProjectSession)
	if materialized == nil || materialized.opts[tmuxopts.ProjectUIDSession] != correctiveAProjectUID {
		t.Fatalf("collision-safe Project runtime missing: %s", server.state())
	}
	if server.session("home") != home || home.windows[0].opts[tmuxopts.WindowUID] != "win-home-control" {
		t.Fatalf("canonical Home runtime was replaced or cross-written: before=%s after=%s", homeBefore, server.state())
	}
	writes := store.writes
	repeat, _, err := runReconcile(t, command, "resources", "--socket", "corrective-a", "--materialize-project", "uid:"+correctiveAProjectUID, "-o", "json")
	if err != nil || !strings.Contains(repeat, `"outcome": "no-op"`) || store.writes != writes {
		t.Fatalf("repeat materialization was not a Registry-write-free no-op: err=%v writes=%d->%d\n%s", err, writes, store.writes, repeat)
	}
}

func TestUnrelatedObservationFailureBecomesTypedD6AndDoesNotStopReconcile(t *testing.T) {
	registry := coremetadata.NewRegistry()
	addCorrectiveAIncidentRegistry(t, &registry)
	r := &registryReconciler{
		liveSessions: func(context.Context) (map[string]bool, error) {
			return map[string]bool{"broken": true, "home": true}, nil
		},
		observeLegacy: func(_ context.Context, session string) (coremetadata.LegacySession, intmetadata.LegacyTargets, error) {
			if session == "broken" {
				return coremetadata.LegacySession{}, intmetadata.LegacyTargets{}, errors.New("malformed generic observation")
			}
			return coremetadata.LegacySession{Session: session}, intmetadata.LegacyTargets{}, nil
		},
		mirror:         intmetadata.NewMirror(newFakeTmux()),
		sessionNameFor: func(string) string { return "home" },
	}
	mutator := reconcileFixtureMutator()
	if err := r.reconcile(context.Background(), &registry, mutator, "op-d6"); err != nil {
		t.Fatalf("unrelated observation failure escaped containment: %v", err)
	}
	if !r.refusedSessions["broken"] || r.refusedSessionDivergence["broken"] != resourcegraph.DivergenceUnknown || !strings.Contains(r.refusedSessionReasons["broken"], "malformed generic observation") {
		t.Fatalf("generic observation refusal = %v / %q / %q", r.refusedSessions, r.refusedSessionDivergence["broken"], r.refusedSessionReasons["broken"])
	}
	items := resourceProjectForeignItems(registry, []observedResourceProjectSession{{id: "$broken", name: "broken"}}, r)
	if len(items) != 1 || items[0].Divergence != resourcegraph.DivergenceUnknown || !strings.Contains(items[0].Reason, "malformed generic observation") {
		t.Fatalf("generic observation diagnostic = %+v, want one typed D6 item", items)
	}
}
