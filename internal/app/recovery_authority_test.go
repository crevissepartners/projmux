package app

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/controller"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

func TestAutomaticRecoveryCallsitesCannotBypassTheProductionAuthorityGate(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	targets := map[string]map[string]bool{
		"resourceControllerKernel.plan":                                  {"requireAutomaticRecoveryPaths": false, "authorizeApprovedOrphanImport": false},
		"resourceControllerKernel.converge":                              {"authorizeApprovedOrphanImport": false},
		"controllerTriggerRunner.converge":                               {"requireAutomaticRecoveryPaths": false, "runLockedAutomaticMirrorRecovery": false},
		"registryProjectTopologyMaterializer.MaterializeProjectTopology": {"requireAutomaticRecoveryPaths": false, "runLockedAutomaticMirrorRecovery": false},
		"topologyMaterializeRun.execute":                                 {"authorizeRecovery": false},
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, declaration := range file.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			receiver := fn.Recv.List[0].Type
			if pointer, ok := receiver.(*ast.StarExpr); ok {
				receiver = pointer.X
			}
			receiverName, ok := receiver.(*ast.Ident)
			if !ok {
				continue
			}
			key := receiverName.Name + "." + fn.Name.Name
			wanted, tracked := targets[key]
			if !tracked {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if ident, ok := call.Fun.(*ast.Ident); ok {
					if _, expected := wanted[ident.Name]; expected {
						wanted[ident.Name] = true
					}
				}
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
					if _, expected := wanted[selector.Sel.Name]; expected {
						wanted[selector.Sel.Name] = true
					}
				}
				return true
			})
		}
	}
	for site, calls := range targets {
		for call, found := range calls {
			if !found {
				t.Fatalf("automatic recovery site %s bypasses %s", site, call)
			}
		}
	}
}

func TestLockedAutomaticRecoveryReclassifiesConcurrentRegistryClaimBeforeL8(t *testing.T) {
	full := resourceFixtureRegistry(t)
	root := t.TempDir()
	full.Projects[0].Spec.Root = root
	full = full.Normalize()
	if err := full.Validate(); err != nil {
		t.Fatalf("claimed Registry invalid: %v", err)
	}
	snapshot := full.Clone()
	snapshot.Panes = nil
	snapshot.Agents = nil
	snapshot.Windows[0].Spec.PrimaryPaneRef = ""
	known := map[string]bool{snapshot.Projects[0].Metadata.UID: true, snapshot.Windows[0].Metadata.UID: true}
	snapshot.NameReservations = slicesDeleteReservations(snapshot.NameReservations, known)
	snapshot = snapshot.Normalize()

	server := newFakeTmux()
	session := server.addSession("alpha")
	session.opts[tmuxopts.ProjectUIDSession] = full.Projects[0].Metadata.UID
	session.opts[tmuxopts.ProjectPathSession] = root
	session.windows[0].opts[tmuxopts.WindowUID] = full.Windows[0].Metadata.UID
	session.windows[0].panes[0].opts[tmuxopts.PaneUID] = full.Panes[0].Metadata.UID
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00locked-race": server}}
	target := explicitTmuxTarget{flag: "-L", value: "locked-race"}
	if got := len(resourcegraph.Resolve(snapshot, intmetadata.NewInventoryObserver(runner, controllerTransport(target)).Observe(context.Background())).RuntimeOfClass(resourcegraph.ClassRecoverable)); got != 1 {
		t.Fatalf("unlocked preview D3 count = %d, want 1", got)
	}
	store := &resourceStore{updateConvergent: func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
		working := full.Clone() // concurrent writer claimed the uid before lock acquisition
		if err := fn(&working); err != nil {
			return coremetadata.Registry{}, false, err
		}
		return working, false, nil
	}}
	server.calls = nil
	recovered, err := runLockedAutomaticMirrorRecovery(context.Background(), store, runner, target, controller.RecoveryProjectOpen)
	if err != nil || recovered != 0 {
		t.Fatalf("locked recovery recovered=%d err=%v", recovered, err)
	}
	if got := server.sessions[0].windows[0].panes[0].opts[tmuxopts.PaneUID]; got != full.Panes[0].Metadata.UID || tmuxMutationCallCount(server) != 0 {
		t.Fatalf("locked authoritative mirror was discarded: uid=%q mutations=%d", got, tmuxMutationCallCount(server))
	}
}

func TestLockedAutomaticRecoveryIgnoresOuterStoreNormalizationChangeSignal(t *testing.T) {
	registry := coremetadata.NewRegistry()
	server := newFakeTmux()
	server.addSession("unattributed")
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00outer-normalize": server}}
	store := &resourceStore{updateConvergent: func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
		working := registry.Clone()
		if err := fn(&working); err != nil {
			return coremetadata.Registry{}, false, err
		}
		// Production stores may normalize or migrate around the callback and
		// therefore report changed even though L8 left callback state untouched.
		return working, true, nil
	}}
	recovered, err := runLockedAutomaticMirrorRecovery(context.Background(), store, runner,
		explicitTmuxTarget{flag: "-L", value: "outer-normalize"}, controller.RecoveryHookConverge)
	if err != nil || recovered != 0 {
		t.Fatalf("outer changed=true was attributed to L8: recovered=%d err=%v", recovered, err)
	}
}

func TestAutomaticRecoveryRegistryInvariantRejectsCallbackMutation(t *testing.T) {
	before := coremetadata.NewRegistry()
	after := before.Clone()
	after.SchemaVersion++
	if err := verifyAutomaticRecoveryRegistryUnchanged(before, after); err == nil || !strings.Contains(err.Error(), "changed Registry bytes") {
		t.Fatalf("Registry mutation invariant error = %v", err)
	}
}

func incident20260820RecoveryFixture(t *testing.T) (coremetadata.Registry, *fakeTmux, string) {
	t.Helper()
	root := t.TempDir()
	registry := resourceFixtureRegistry(t)
	project := registry.Projects[0]
	project.Spec.Root = root
	window := registry.WindowsOf(project.Metadata.UID)[0]
	agent, _ := registry.Agent("agt-alpha-codex")
	agent.Status.SessionRef = &coremetadata.AgentSessionRef{
		Provider: "codex", ObservedAt: resourceFixtureClock,
		Codex: &coremetadata.CodexSessionRef{ThreadID: "sentinel-thread", SessionID: "sentinel-session"},
	}
	shellPane, _ := registry.Pane("pan-alpha-zsh")
	sentinelPane, _ := registry.Pane("pan-alpha-codex")
	registry.Projects = []coremetadata.Project{project}
	registry.Windows = []coremetadata.Window{window}
	registry.Panes = []coremetadata.Pane{*shellPane, *sentinelPane}
	registry.Agents = []coremetadata.Agent{*agent}
	registry.ControlSessions = nil
	known := map[string]bool{project.Metadata.UID: true, window.Metadata.UID: true, shellPane.Metadata.UID: true, sentinelPane.Metadata.UID: true, agent.Metadata.UID: true}
	registry.NameReservations = append([]coremetadata.NameReservation(nil), registry.NameReservations...)
	registry.NameReservations = slicesDeleteReservations(registry.NameReservations, known)
	registry = registry.Normalize()
	if err := registry.Validate(); err != nil {
		t.Fatalf("incident Registry is invalid: %v", err)
	}

	server := newFakeTmux()
	session := server.addSession("alpha")
	session.opts[tmuxopts.ProjectUIDSession] = project.Metadata.UID
	session.opts[tmuxopts.ProjectNameSession] = project.Metadata.Name
	session.opts[tmuxopts.ProjectPathSession] = root
	liveWindow := session.windows[0]
	liveWindow.opts[tmuxopts.WindowUID] = window.Metadata.UID
	liveWindow.opts[tmuxopts.WindowName] = window.Metadata.Name
	liveWindow.panes[0].opts[tmuxopts.PaneUID] = shellPane.Metadata.UID
	liveWindow.panes = append(liveWindow.panes, newFakeTmuxPane(server.mint("%")), newFakeTmuxPane(server.mint("%")), newFakeTmuxPane(server.mint("%")))
	liveWindow.panes[1].opts[tmuxopts.PaneUID] = sentinelPane.Metadata.UID
	liveWindow.panes[1].opts[tmuxopts.AgentSessionIDPane] = "sentinel-session"
	liveWindow.panes[1].opts[tmuxopts.AgentThreadIDPane] = "sentinel-thread"
	for i, pane := range liveWindow.panes[2:] {
		pane.opts[tmuxopts.PaneUID] = []string{"pane-incident-orphan-one", "pane-incident-orphan-two"}[i]
		pane.opts[tmuxopts.AgentProviderPane] = "codex"
		pane.opts[tmuxopts.AgentTopicPane] = "preserved topic"
		pane.opts[tmuxopts.AgentSessionIDPane] = "session-routing-" + pane.id
		pane.opts[tmuxopts.AgentThreadIDPane] = "thread-routing-" + pane.id
	}
	return registry, server, root
}

func slicesDeleteReservations(in []coremetadata.NameReservation, known map[string]bool) []coremetadata.NameReservation {
	out := make([]coremetadata.NameReservation, 0, len(in))
	for _, reservation := range in {
		if known[reservation.UID] {
			out = append(out, reservation)
		}
	}
	return out
}

func TestIncident20260820ExactD3AutomaticallyRunsL8WhileL7RequiresApproval(t *testing.T) {
	registry, server, root := incident20260820RecoveryFixture(t)
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00incident": server}}
	target := explicitTmuxTarget{flag: "-L", value: "incident"}
	inventory := intmetadata.NewInventoryObserver(runner, controllerTransport(target)).Observe(context.Background())
	items := resourcegraph.ClassifyDivergences(registry, inventory)
	d3 := 0
	for _, item := range items {
		if item.Divergence == resourcegraph.DivergenceOrphanMirror {
			d3++
		}
	}
	if d3 != 2 {
		t.Fatalf("incident D3 count = %d, want exact 2: %+v", d3, items)
	}
	if got := controller.AuthorizeRecovery(resourcegraph.DivergenceOrphanMirror, controller.RecoveryHookConverge,
		controller.RecoveryImport, false, 2); got.Decision != controller.RecoveryRequireApproval || got.LossCount != 2 {
		t.Fatalf("D3 L7 authority = %+v", got)
	}

	beforeRegistry := registry.Clone()
	recovered, err := runAutomaticMirrorRecovery(context.Background(), runner, target, registry, controller.RecoveryHookConverge)
	if err != nil || recovered != 2 {
		t.Fatalf("automatic L8 recovered=%d err=%v", recovered, err)
	}
	if !reflect.DeepEqual(registry, beforeRegistry) {
		t.Fatal("automatic L8 changed Registry rows or the sentinel conversation pointer")
	}
	if preserved, ok := registry.Agent("agt-alpha-codex"); !ok || preserved.Status.SessionRef == nil ||
		preserved.Status.SessionRef.Codex == nil || preserved.Status.SessionRef.Codex.ThreadID != "sentinel-thread" {
		t.Fatalf("sentinel conversation pointer changed: %+v", preserved)
	}
	panes := server.sessions[0].windows[0].panes
	if panes[1].opts[tmuxopts.PaneUID] != "pan-alpha-codex" || panes[1].opts[tmuxopts.AgentThreadIDPane] != "sentinel-thread" {
		t.Fatalf("managed sentinel mirror changed: %+v", panes[1].opts)
	}
	for _, pane := range panes[2:] {
		for _, field := range []string{tmuxopts.PaneUID, tmuxopts.AgentSessionIDPane, tmuxopts.AgentThreadIDPane} {
			if got := pane.opts[field]; got != "" {
				t.Fatalf("%s on %s survived L8 as %q", field, pane.id, got)
			}
		}
		if pane.opts[tmuxopts.AgentProviderPane] != "codex" || pane.opts[tmuxopts.AgentTopicPane] != "preserved topic" {
			t.Fatalf("L8 erased provider/topic on %s: %+v", pane.id, pane.opts)
		}
	}

	// The post-L8 objects are D2, and an ordinary explicit reconcile must not
	// import them. This is also the write-path recovery check: the same valid
	// Registry can enter its convergent write transaction after the incident.
	store := &fakeResourceStore{registry: registry.Clone(), dirs: map[string]bool{root: true}, now: resourceFixtureClock}
	command := &resourceReconcileCommand{
		runner: runner, resources: store.store(), lookupEnv: func(string) string { return "" },
		newReconciler: reconcileFixtureReconciler(root, "alpha"),
	}
	stdout, _, err := runReconcile(t, command, "resources", "--socket", "incident", "-o", "json")
	if err != nil {
		t.Fatalf("post-L8 reconcile did not restore writable convergence: %v\n%s", err, stdout)
	}
	if len(store.registry.Panes) != 2 {
		t.Fatalf("post-L8 D2 was imported: panes=%d\n%s", len(store.registry.Panes), stdout)
	}
	repeat, _, err := runReconcile(t, command, "resources", "--socket", "incident", "-o", "json")
	if err != nil || !strings.Contains(repeat, `"outcome": "no-op"`) {
		t.Fatalf("post-L8 write path did not converge to a no-op: err=%v\n%s", err, repeat)
	}
}

func TestPublicReconcileReportsOneAutomaticL8ItemPerIncidentObject(t *testing.T) {
	registry, server, root := incident20260820RecoveryFixture(t)
	store := &fakeResourceStore{registry: registry, dirs: map[string]bool{root: true}, now: resourceFixtureClock}
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00incident-report": server}}
	command := &resourceReconcileCommand{
		runner: runner, resources: store.store(), lookupEnv: func(string) string { return "" },
		newReconciler: reconcileFixtureReconciler(root, "alpha"),
	}
	stdout, _, err := runReconcile(t, command, "resources", "--socket", "incident-report", "-o", "json")
	if err != nil {
		t.Fatalf("incident reconcile: %v\n%s", err, stdout)
	}
	report := parseControllerReport(t, stdout)
	count := 0
	for _, item := range report.Items {
		if item["divergence"] == string(resourcegraph.DivergenceOrphanMirror) &&
			item["action"] == string(controller.RecoveryDiscardMirror) {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("reported automatic L8 items = %d, want 2\n%s", count, stdout)
	}
}

func TestIncident20260820DryRunDisclosesExactApprovedL7CommandAndLoss(t *testing.T) {
	registry, server, root := incident20260820RecoveryFixture(t)
	blank := newFakeTmuxPane(server.mint("%"))
	server.sessions[0].windows[0].panes = append(server.sessions[0].windows[0].panes, blank)
	store := &fakeResourceStore{registry: registry, dirs: map[string]bool{root: true}, now: resourceFixtureClock}
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00incident-approval": server}}
	command := &resourceReconcileCommand{
		runner: runner, resources: store.store(), lookupEnv: func(string) string { return "" },
		newReconciler: reconcileFixtureReconciler(root, "alpha"),
	}
	before := server.state()
	stdout, _, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "incident-approval", "-o", "json")
	if err != nil {
		t.Fatalf("incident dry-run: %v\n%s", err, stdout)
	}
	for _, want := range []string{
		`"action": "L8-discard-mirror"`,
		`"approvalCommand": "projmux reconcile resources --socket 'incident-approval' --import-orphan-mirrors --yes"`,
		`"lossKind": "identity-provenance"`,
		`"lossCount": 2`,
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("incident dry-run missing %q:\n%s", want, stdout)
		}
	}
	if store.writes != 0 || tmuxMutationCallCount(server) != 0 || server.state() != before {
		t.Fatalf("incident dry-run mutated state: registry=%d tmux=%d", store.writes, tmuxMutationCallCount(server))
	}

	if _, _, err := runReconcile(t, command, "resources", "--socket", "incident-approval", "--import-orphan-mirrors"); err == nil || !strings.Contains(err.Error(), "requires --yes") {
		t.Fatalf("approved import without --yes error = %v", err)
	}
	if store.writes != 0 || tmuxMutationCallCount(server) != 0 {
		t.Fatal("missing --yes changed Registry or tmux")
	}
}

func TestApprovedL7ImportsExactD3OnlyAndLeavesBlankD2Unattributed(t *testing.T) {
	registry, server, root := incident20260820RecoveryFixture(t)
	blank := newFakeTmuxPane(server.mint("%"))
	server.sessions[0].windows[0].panes = append(server.sessions[0].windows[0].panes, blank)
	store := &fakeResourceStore{registry: registry, dirs: map[string]bool{root: true}, now: resourceFixtureClock}
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00incident-approved": server}}
	command := &resourceReconcileCommand{
		runner: runner, resources: store.store(), lookupEnv: func(string) string { return "" },
		newReconciler: reconcileFixtureReconciler(root, "alpha"),
	}
	stdout, _, err := runReconcile(t, command, "resources", "--socket", "incident-approved", "--import-orphan-mirrors", "--yes", "-o", "json")
	if err != nil {
		t.Fatalf("approved D3 import: %v\n%s", err, stdout)
	}
	if len(store.registry.Panes) != 4 {
		t.Fatalf("approved import left %d Pane rows, want two sentinels + exact D3 count 2\n%s", len(store.registry.Panes), stdout)
	}
	if blank.opts[tmuxopts.PaneUID] != "" {
		t.Fatalf("blank D2 sibling was imported as %q", blank.opts[tmuxopts.PaneUID])
	}
	if !strings.Contains(stdout, `"lossKind": "identity-provenance"`) || !strings.Contains(stdout, `"lossCount": 2`) {
		t.Fatalf("approved report omitted exact loss disclosure:\n%s", stdout)
	}
}

func TestApprovedL7StillRefusesD4Contamination(t *testing.T) {
	registry, server, root := incident20260820RecoveryFixture(t)
	knownWindowUID := registry.Windows[0].Metadata.UID
	server.sessions[0].windows[0].panes[0].opts[tmuxopts.PaneUID] = knownWindowUID
	store := &fakeResourceStore{registry: registry, dirs: map[string]bool{root: true}, now: resourceFixtureClock}
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00incident-d4": server}}
	command := &resourceReconcileCommand{
		runner: runner, resources: store.store(), lookupEnv: func(string) string { return "" },
		newReconciler: reconcileFixtureReconciler(root, "alpha"),
	}
	before := server.sessions[0].windows[0].panes[0].opts[tmuxopts.PaneUID]
	_, _, err := runReconcile(t, command, "resources", "--socket", "incident-d4", "--import-orphan-mirrors", "--yes", "-o", "json")
	if err == nil {
		t.Fatal("approved D3 import accepted D4-contaminated session")
	}
	if len(store.registry.Panes) != 2 || server.sessions[0].windows[0].panes[0].opts[tmuxopts.PaneUID] != before {
		t.Fatal("D4 contamination changed Registry identity or live mirror")
	}
}
