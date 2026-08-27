package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func TestProjectStartupActionRowsAreExactlyContinueAndOpenFresh(t *testing.T) {
	candidates := (&switchCommand{}).projectStartupCandidates("ignored", "/ignored")
	options := projectStartupPickerOptions(candidates)
	if len(options.Entries) != 2 {
		t.Fatalf("startup action rows=%d, want 2: %#v", len(options.Entries), options.Entries)
	}
	got := []string{options.Entries[0].Value, options.Entries[1].Value}
	want := []string{projectStartupValueTopology, projectStartupValueNew}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("startup values=%q, want %q", got, want)
	}
	joined := strings.ToLower(options.Entries[0].Label + options.Entries[1].Label + options.Footer)
	for _, forbidden := range []string{"latest snapshot", "named snapshot", "project topology", "reconcile", "back row"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("startup surface exposes %q: %s", forbidden, joined)
		}
	}
}

func TestProjectStartupExactTwoRowGolden(t *testing.T) {
	candidates := (&switchCommand{}).projectStartupCandidates("ignored", "/ignored")
	options := projectStartupPickerOptions(candidates)
	var got strings.Builder
	fmt.Fprintf(&got, "ui=%s\nheader=%s\nfooter=%s\n", options.UI, options.Header, options.Footer)
	for index, candidate := range candidates {
		fmt.Fprintf(&got, "row[%d]=%s|%s|%s|%s\n", index, candidate.Kind,
			options.Entries[index].Value, candidate.Label, candidate.Description)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "project-startup-rows.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != string(want) {
		t.Fatalf("Project startup row golden mismatch:\ngot:\n%swant:\n%s", got.String(), want)
	}
}

// fakeProjectTopologyMaterializer records what closed-Project activation asked
// for without running any engine, so the switch-level ordering contract
// (materialize, then move the client) can be asserted on its own.
type fakeProjectTopologyMaterializer struct {
	calls        []string
	requests     []projectTopologyMaterializeRequest
	materialized bool
	err          error
}

func (f *fakeProjectTopologyMaterializer) MaterializeProjectTopology(_ context.Context, request projectTopologyMaterializeRequest) (bool, error) {
	f.calls = append(f.calls, "topology:"+request.Root+":"+request.SessionName)
	f.requests = append(f.requests, request)
	if f.err != nil {
		return false, f.err
	}
	return f.materialized, nil
}

// newProjectStartupTopologyFixture builds the same offline two-Window,
// three-shell-Pane Project the explicit materialization tests use, wired to a
// startup activation instead of the reconcile route. Stored commands are
// deliberately `DO-NOT-EXECUTE ...` so a replay would be visible in argv.
func newProjectStartupTopologyFixture(t *testing.T) (*registryProjectTopologyMaterializer, *fakeResourceStore, *fakeTmux, string, string) {
	t.Helper()
	root, logs := t.TempDir(), t.TempDir()
	store := newFakeResourceStore(t)
	project, _ := store.registry.Project("prj-beta")
	project.Spec.Root = root
	for i := range store.registry.Panes {
		pane := &store.registry.Panes[i]
		if pane.Metadata.OwnerUID() == "win-beta-main" {
			pane.Spec.CWD = root
		}
	}
	store.dirs = map[string]bool{root: true, logs: true}
	mutator := store.mutator()
	if _, err := mutator.AddPane(&store.registry, "win-beta-main", coremetadata.BootstrapPane{
		Name: "logs", CWD: logs, Command: "DO-NOT-EXECUTE --pane",
	}, "/bin/zsh", "op-startup-fixture"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mutator.AddWindow(&store.registry, "prj-beta", coremetadata.BootstrapWindow{
		Name: "review", Command: "DO-NOT-EXECUTE --window",
		Panes: []coremetadata.BootstrapPane{{CWD: root, Command: "DO-NOT-EXECUTE --primary"}},
	}, "/bin/zsh", "op-startup-fixture"); err != nil {
		t.Fatal(err)
	}
	// The shared startup fixture stays shell-only for the same reason the
	// reconcile one does: Agent replay has its own fixtures, so every parity
	// assertion here keeps measuring the Window/shell-Pane behavior it did
	// before Agents entered the plan.
	if err := mutator.DeleteAgent(&store.registry, "agt-beta-codex"); err != nil {
		t.Fatal(err)
	}
	server := newFakeTmux()
	server.socketPath = "/tmp/fake-tmux/projmux"
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00projmux": server}}
	sessions := &fakeSessionMaterializer{tmux: server}
	target, err := tmuxSocketNameTarget(defaultAppSocket)
	if err != nil {
		t.Fatal(err)
	}
	activation := &registryProjectTopologyMaterializer{
		resources:      store.store(),
		runner:         runner,
		target:         target,
		newReconciler:  reconcileFixtureReconciler(root, "beta"),
		newOperationID: func() (string, error) { return "op-startup", nil },
		newMaterializer: func(exact tmuxCommandRunner, warn io.Writer) *materializer {
			return &materializer{runner: exact, mirror: intmetadata.NewMirror(exact), sessions: sessions, warn: warn}
		},
		warn:    io.Discard,
		agents:  newFakeTopologyAgentLauncher(),
		notices: &bytes.Buffer{},
	}
	return activation, store, server, root, logs
}

// TestClosedProjectStartupMaterializesFullRegistryTopology is acceptance 1 at the
// engine boundary: opening a closed Project rebuilds every Registry Window and
// Window-owned shell Pane on the exact app socket under the stored uids, runs no
// stored command, and converges to a repeat no-op.
func TestClosedProjectStartupMaterializesFullRegistryTopology(t *testing.T) {
	activation, store, server, root, logs := newProjectStartupTopologyFixture(t)

	materialized, err := activation.MaterializeProjectTopology(context.Background(), projectTopologyMaterializeRequest{Root: root, SessionName: "beta"})
	if err != nil || !materialized {
		t.Fatalf("MaterializeProjectTopology() = %t, %v; want true, nil", materialized, err)
	}
	session := server.session("beta")
	if session == nil || session.opts[tmuxopts.ProjectUIDSession] != "prj-beta" || len(session.windows) != 2 {
		t.Fatalf("closed Project startup topology = %s", server.state())
	}
	if got := len(session.windows[0].panes); got != 2 {
		t.Fatalf("main window panes = %d, want 2\n%s", got, server.state())
	}
	for _, call := range server.calls {
		if len(call) > 0 && slices.Contains([]string{"new-session", "new-window", "split-window"}, call[0]) &&
			strings.Contains(strings.Join(call, " "), "DO-NOT-EXECUTE") {
			t.Fatalf("startup replayed a stored Pane command: %v", call)
		}
	}
	for _, cwd := range []string{root, logs} {
		if !slices.ContainsFunc(server.calls, func(call []string) bool { return flagValue(call, "-c") == cwd }) {
			t.Fatalf("no exact -c %s create call: %#v", cwd, server.calls)
		}
	}
	project, _ := store.registry.Project("prj-beta")
	if project.Status.Session == nil || project.Status.Session.Name != "beta" || !project.Status.Session.Live {
		t.Fatalf("Project status.session = %+v", project.Status.Session)
	}

	server.calls = nil
	writesBefore := store.writes
	materialized, err = activation.MaterializeProjectTopology(context.Background(), projectTopologyMaterializeRequest{Root: root, SessionName: "beta"})
	if err != nil || !materialized {
		t.Fatalf("repeat MaterializeProjectTopology() = %t, %v; want true, nil", materialized, err)
	}
	if store.writes != writesBefore {
		t.Fatalf("repeat activation wrote the Registry: %d -> %d", writesBefore, store.writes)
	}
	for _, call := range server.calls {
		if len(call) > 0 && slices.Contains([]string{"new-session", "new-window", "split-window"}, call[0]) {
			t.Fatalf("repeat activation mutated the runtime with %v", call)
		}
	}
}

// TestClosedProjectStartupWithoutDesiredTopologyStaysOnEnsureSession proves the
// activation degrades instead of failing: an unregistered root and a Project
// with no Registry Window both report "nothing to materialize" with zero
// Registry transactions and zero tmux calls, which is what keeps a first open
// of a plain directory unchanged.
func TestClosedProjectStartupWithoutDesiredTopologyStaysOnEnsureSession(t *testing.T) {
	activation, store, server, root, _ := newProjectStartupTopologyFixture(t)

	unregistered := t.TempDir()
	materialized, err := activation.MaterializeProjectTopology(context.Background(), projectTopologyMaterializeRequest{Root: unregistered, SessionName: "beta"})
	if err != nil || materialized {
		t.Fatalf("unregistered root = %t, %v; want false, nil", materialized, err)
	}

	// Strip the Project's Windows: the Project exists, but declares no topology.
	store.registry.Windows = nil
	store.registry.Panes = nil
	materialized, err = activation.MaterializeProjectTopology(context.Background(), projectTopologyMaterializeRequest{Root: root, SessionName: "beta"})
	if err != nil || materialized {
		t.Fatalf("windowless Project = %t, %v; want false, nil", materialized, err)
	}
	if store.transactions != 0 || store.writes != 0 {
		t.Fatalf("read-only topology probe opened a transaction: transactions=%d writes=%d", store.transactions, store.writes)
	}
	if len(server.calls) != 0 {
		t.Fatalf("read-only topology probe called tmux: %#v", server.calls)
	}
}

func TestZeroWindowContinuePreservesProjectAndAllocatesCanonicalWindowShellUIDs(t *testing.T) {
	_, store, _, root, _ := newProjectStartupTopologyFixture(t)
	mutator := store.mutator()
	for _, window := range store.registry.WindowsOf("prj-beta") {
		if err := mutator.DeleteWindow(&store.registry, window.Metadata.UID); err != nil {
			t.Fatal(err)
		}
	}
	projectBefore, ok := store.registry.Project("prj-beta")
	if !ok || len(store.registry.WindowsOf(projectBefore.Metadata.UID)) != 0 {
		t.Fatalf("zero-Window fixture = %+v", store.registry)
	}

	starter := &registryProjectFreshStarter{resources: store.store()}
	opened, err := starter.ContinueProject(context.Background(), root, "beta")
	if err != nil {
		t.Fatal(err)
	}
	if opened.project.Metadata.UID != projectBefore.Metadata.UID || !opened.bootstrapped || opened.materializeTopology {
		t.Fatalf("zero-Window Continue = %+v, want same Project with canonical first-session bootstrap", opened)
	}
	projectAfter, ok := store.registry.Project("prj-beta")
	if !ok || projectAfter.Metadata.UID != projectBefore.Metadata.UID {
		t.Fatalf("Project identity after Continue = %+v", projectAfter)
	}
	windows := store.registry.WindowsOf(projectAfter.Metadata.UID)
	if len(windows) != 1 || windows[0].Metadata.UID == "" {
		t.Fatalf("canonical Window after Continue = %+v", windows)
	}
	panes := store.registry.PanesOf(windows[0].Metadata.UID)
	if len(panes) != 1 || panes[0].Metadata.UID == "" || panes[0].Spec.Role != coremetadata.PaneRoleShell {
		t.Fatalf("canonical shell after Continue = %+v", panes)
	}
	if windows[0].Spec.AnchorPaneRef != panes[0].Metadata.UID || windows[0].Spec.DefaultShellPaneRef != panes[0].Metadata.UID ||
		projectAfter.Spec.PrimaryWindowRef != windows[0].Metadata.UID {
		t.Fatalf("canonical refs Project=%+v Window=%+v Pane=%+v", projectAfter, windows[0], panes[0])
	}
}

// TestClosedProjectStartupRefusesForeignSessionProjection is acceptance 3 at the
// preflight: when the Registry projects a different session name than the one the
// open targets, materializing it would populate a session the client never moves
// to, so the plan is refused with nothing created.
func TestClosedProjectStartupRefusesForeignSessionProjection(t *testing.T) {
	activation, store, server, root, _ := newProjectStartupTopologyFixture(t)

	materialized, err := activation.MaterializeProjectTopology(context.Background(), projectTopologyMaterializeRequest{Root: root, SessionName: "work-alpha"})
	if err == nil || materialized {
		t.Fatalf("foreign session projection = %t, %v; want false and an error", materialized, err)
	}
	if !strings.Contains(err.Error(), "topology prevalidation") {
		t.Fatalf("error = %v, want the topology prevalidation stage", err)
	}
	if server.session("beta") != nil || server.session("work-alpha") != nil {
		t.Fatalf("refused activation created a session: %s", server.state())
	}
	if store.writes != 0 {
		t.Fatalf("refused activation wrote the Registry %d time(s)", store.writes)
	}
}

// TestSwitchClosedProjectOpenWithPickerOffMaterializesThenMovesClient is
// acceptance 1 at the switch boundary: with the startup picker off, the default
// closed-Project open materializes the Registry topology and only then opens the
// session; it never falls back to a bare EnsureSession.
func TestSwitchClosedProjectOpenWithPickerOffMaterializesThenMovesClient(t *testing.T) {
	t.Parallel()

	topology := &fakeProjectTopologyMaterializer{materialized: true}
	executor := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true}
	cmd := &switchCommand{
		sessions:        executor,
		identity:        stubSwitchIdentityResolver{name: "workspace"},
		homeDir:         func() (string, error) { return t.TempDir(), nil },
		lookupEnv:       func(string) string { return "" },
		projectTopology: topology,
	}

	if err := cmd.openProjectTarget(context.Background(), "/tmp/workspace", "workspace"); err != nil {
		t.Fatalf("openProjectTarget() error = %v", err)
	}
	if got, want := topology.calls, []string{"topology:/tmp/workspace:workspace"}; !equalStrings(got, want) {
		t.Fatalf("topology calls = %q, want %q", got, want)
	}
	if got, want := executor.calls, []string{"authorize:/tmp/workspace", "open:workspace"}; !equalStrings(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	if executor.ensureSessionName != "" {
		t.Fatalf("materialized startup also called EnsureSession(%q)", executor.ensureSessionName)
	}
}

// TestSwitchClosedProjectOpenPickerTopologyRowUsesTheSameEngine is acceptance 2's
// positive half: the picker's `Project topology` row is the same activation the
// picker-off default performs.
func TestSwitchClosedProjectOpenPickerTopologyRowUsesTheSameEngine(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	enableSidebarStartupPickerForTest(t, home)
	topology := &fakeProjectTopologyMaterializer{materialized: true}
	executor := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true}
	var startupOptions intpickercompat.Options
	runner, native := scriptedPicker(t, []pickerStep{
		{observe: func(o intpickercompat.Options) { startupOptions = o },
			reply: intpickercompat.Result{Value: projectStartupValueTopology}},
	})
	cmd := &switchCommand{
		sessions: executor,
		identity: stubSwitchIdentityResolver{name: "workspace"},
		homeDir:  func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "XDG_CONFIG_HOME" {
				return filepath.Join(home, "config")
			}
			return ""
		},
		runner:          runner,
		nativePicker:    native,
		projectTopology: topology,
	}
	if err := cmd.openProjectTarget(context.Background(), "/tmp/workspace", "workspace"); err != nil {
		t.Fatalf("openProjectTarget() error = %v", err)
	}
	requireSwitchEntryLabel(t, startupOptions.Entries, "Continue project")
	requireSwitchEntryLabel(t, startupOptions.Entries, projectTopologyStartupDescription)
	for _, entry := range startupOptions.Entries {
		if strings.Contains(entry.Label, "Empty session") {
			t.Fatalf("startup picker still advertises Empty session: %q", entry.Label)
		}
	}
	if got, want := topology.calls, []string{"topology:/tmp/workspace:workspace"}; !equalStrings(got, want) {
		t.Fatalf("topology calls = %q, want %q", got, want)
	}
	if got, want := executor.calls, []string{"authorize:/tmp/workspace", "open:workspace"}; !equalStrings(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

// TestSwitchClosedProjectOpenSnapshotSelectionsNeverMixSources is acceptance 2's
// negative half: a snapshot selection stays on the snapshot engine and performs
// zero Registry topology activation.
func TestSwitchClosedProjectOpenTopologyFailureDoesNotMoveClient(t *testing.T) {
	t.Parallel()

	topology := &fakeProjectTopologyMaterializer{err: errors.New("topology materialization: pane cwd is gone")}
	executor := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true}
	cmd := &switchCommand{
		sessions:        executor,
		identity:        stubSwitchIdentityResolver{name: "workspace"},
		homeDir:         func() (string, error) { return t.TempDir(), nil },
		lookupEnv:       func(string) string { return "" },
		projectTopology: topology,
	}

	err := cmd.openProjectTarget(context.Background(), "/tmp/workspace", "workspace")
	if err == nil || !strings.Contains(err.Error(), "pane cwd is gone") {
		t.Fatalf("openProjectTarget() error = %v, want the exact materialization cause", err)
	}
	if executor.openSessionName != "" || executor.ensureSessionName != "" {
		t.Fatalf("failed activation still touched the runtime: %#v", executor)
	}
	if got, want := executor.calls, []string{"authorize:/tmp/workspace"}; !equalStrings(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

// TestSwitchClosedProjectOpenWithoutDesiredTopologyKeepsEnsureSession pins the
// unchanged path for a directory that declares no Registry topology.
func TestSwitchClosedProjectOpenWithoutDesiredTopologyKeepsEnsureSession(t *testing.T) {
	t.Parallel()

	topology := &fakeProjectTopologyMaterializer{}
	executor := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true}
	cmd := &switchCommand{
		sessions:        executor,
		identity:        stubSwitchIdentityResolver{name: "workspace"},
		homeDir:         func() (string, error) { return t.TempDir(), nil },
		lookupEnv:       func(string) string { return "" },
		projectTopology: topology,
	}
	wireFakeProjectSessionPlan(cmd)

	if err := cmd.openProjectTarget(context.Background(), "/tmp/workspace", "workspace"); err != nil {
		t.Fatalf("openProjectTarget() error = %v", err)
	}
	if got, want := executor.calls, []string{"authorize:/tmp/workspace", "ensure:workspace", "open:workspace"}; !equalStrings(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

// TestSwitchExistingLiveProjectOpenSkipsTopologyActivation is acceptance 4's
// existing-session half: an already live Project is opened directly, with no
// activation and no create.
func TestSwitchExistingLiveProjectOpenSkipsTopologyActivation(t *testing.T) {
	t.Parallel()

	topology := &fakeProjectTopologyMaterializer{materialized: true}
	executor := &capturingSwitchSessionExecutor{exists: map[string]bool{"workspace": true}}
	cmd := &switchCommand{
		sessions:        executor,
		identity:        stubSwitchIdentityResolver{name: "workspace"},
		homeDir:         func() (string, error) { return t.TempDir(), nil },
		lookupEnv:       func(string) string { return "" },
		projectTopology: topology,
	}

	if err := cmd.openProjectTarget(context.Background(), "/tmp/workspace", "workspace"); err != nil {
		t.Fatalf("openProjectTarget() error = %v", err)
	}
	if len(topology.calls) != 0 {
		t.Fatalf("existing live open activated topology: %q", topology.calls)
	}
	if got, want := executor.calls, []string{"open:workspace"}; !equalStrings(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	if executor.authorizeCalled {
		t.Fatal("existing live open ran the trust gate")
	}
}

// TestNewSwitchCommandWiresExactSocketTopologyActivation keeps the production
// wiring honest: without it a nil seam would silently restore the old
// single-Session behavior while the copy kept promising the full topology.
func TestNewSwitchCommandWiresExactSocketTopologyActivation(t *testing.T) {
	t.Parallel()

	activation, ok := newSwitchCommand().projectTopology.(*registryProjectTopologyMaterializer)
	if !ok {
		t.Fatalf("newSwitchCommand().projectTopology = %T, want the Registry topology activation", newSwitchCommand().projectTopology)
	}
	want := explicitTmuxTarget{flag: "-L", value: defaultAppSocket}
	if !reflect.DeepEqual(activation.target, want) {
		t.Fatalf("activation target = %+v, want %+v", activation.target, want)
	}
	if activation.resources == nil || activation.runner == nil {
		t.Fatalf("activation is not fully wired: %+v", activation)
	}
}

// TestClosedProjectStartupReplaysStoredAgents is acceptance 1 at the startup
// boundary: the `Project topology` row brings saved Agents back into their own
// managed Panes on the exact app socket, resumes the one whose Registry
// `status.sessionRef` names a conversation, and discloses the one it could not.
func TestClosedProjectStartupReplaysStoredAgents(t *testing.T) {
	activation, store, server, root, _ := newProjectStartupTopologyFixture(t)
	notices := activation.notices.(*bytes.Buffer)
	resumed := addTopologyFixtureAgent(t, store, topologyFixtureAgent{
		name: "claude", provider: "claude", cwd: root, ref: claudeConversationRef("conv-startup"),
	})
	fresh := addTopologyFixtureAgent(t, store, topologyFixtureAgent{name: "codex", provider: "codex", cwd: root})

	materialized, err := activation.MaterializeProjectTopology(context.Background(), projectTopologyMaterializeRequest{Root: root, SessionName: "beta"})
	if err != nil || !materialized {
		t.Fatalf("MaterializeProjectTopology() = %t, %v; want true, nil", materialized, err)
	}
	for _, agentUID := range []string{resumed.Metadata.UID, fresh.Metadata.UID} {
		agent, ok := store.registry.Agent(agentUID)
		if !ok || agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef == "" {
			t.Fatalf("closed Project startup left agent %s unmanaged: %+v", agentUID, agent)
		}
		if !slices.ContainsFunc(server.session("beta").windows[0].panes, func(p *fakeTmuxPane) bool {
			return p.opts[tmuxopts.PaneUID] == agent.Status.PaneRef
		}) {
			t.Fatalf("agent %s managed Pane never reached tmux:\n%s", agentUID, server.state())
		}
	}
	if !server.argvContains("--resume") || !server.argvContains("conv-startup") {
		t.Fatalf("startup did not resume the stored conversation:\n%#v", server.calls)
	}
	if !strings.Contains(notices.String(), "agent/main/codex starts a new conversation instead of resuming") {
		t.Fatalf("startup never told the operator which Agent was not resumed: %q", notices.String())
	}
	if strings.Contains(notices.String(), "agent/main/claude") {
		t.Fatalf("a resumed Agent was reported as unresumed: %q", notices.String())
	}
}

// TestApplicationGraphWiresTopologyAgentReplay keeps the production wiring
// honest on both surfaces. A nil launcher would silently restore the shell-only
// restore while the picker copy kept promising Agents.
func TestApplicationGraphWiresTopologyAgentReplay(t *testing.T) {
	app := New()
	if app.reconcile == nil || app.reconcile.agents == nil {
		t.Fatal("reconcile resources --materialize-project has no Agent launch seam")
	}
	topology, ok := app.switcher.projectTopology.(*registryProjectTopologyMaterializer)
	if !ok {
		t.Fatalf("switcher.projectTopology = %T, want the Registry topology activation", app.switcher.projectTopology)
	}
	if topology.agents == nil {
		t.Fatal("closed-Project startup has no Agent launch seam")
	}
	if topology.notices == nil {
		t.Fatal("closed-Project startup has nowhere to disclose an unresumed Agent")
	}
}
