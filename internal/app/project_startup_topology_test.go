package app

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// fakeProjectTopologyMaterializer records what closed-Project activation asked
// for without running any engine, so the switch-level ordering contract
// (materialize, then move the client) can be asserted on its own.
type fakeProjectTopologyMaterializer struct {
	calls        []string
	materialized bool
	err          error
}

func (f *fakeProjectTopologyMaterializer) MaterializeProjectTopology(_ context.Context, root, sessionName string) (bool, error) {
	f.calls = append(f.calls, "topology:"+root+":"+sessionName)
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
	server := newFakeTmux()
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
		warn: io.Discard,
	}
	return activation, store, server, root, logs
}

// TestClosedProjectStartupMaterializesFullRegistryTopology is acceptance 1 at the
// engine boundary: opening a closed Project rebuilds every Registry Window and
// Window-owned shell Pane on the exact app socket under the stored uids, runs no
// stored command, and converges to a repeat no-op.
func TestClosedProjectStartupMaterializesFullRegistryTopology(t *testing.T) {
	activation, store, server, root, logs := newProjectStartupTopologyFixture(t)

	materialized, err := activation.MaterializeProjectTopology(context.Background(), root, "beta")
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
	materialized, err = activation.MaterializeProjectTopology(context.Background(), root, "beta")
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
	materialized, err := activation.MaterializeProjectTopology(context.Background(), unregistered, "beta")
	if err != nil || materialized {
		t.Fatalf("unregistered root = %t, %v; want false, nil", materialized, err)
	}

	// Strip the Project's Windows: the Project exists, but declares no topology.
	store.registry.Windows = nil
	store.registry.Panes = nil
	materialized, err = activation.MaterializeProjectTopology(context.Background(), root, "beta")
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

// TestClosedProjectStartupRefusesForeignSessionProjection is acceptance 3 at the
// preflight: when the Registry projects a different session name than the one the
// open targets, materializing it would populate a session the client never moves
// to, so the plan is refused with nothing created.
func TestClosedProjectStartupRefusesForeignSessionProjection(t *testing.T) {
	activation, store, server, root, _ := newProjectStartupTopologyFixture(t)

	materialized, err := activation.MaterializeProjectTopology(context.Background(), root, "work-alpha")
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
	requireSwitchEntryLabel(t, startupOptions.Entries, "Project topology")
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
func TestSwitchClosedProjectOpenSnapshotSelectionsNeverMixSources(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	enableSidebarStartupPickerForTest(t, home)
	stateDir := filepath.Join(home, "state", "projmux", "sessions")
	saveSwitchProjectStartupSnapshot(t, sessionstate.NewStore(stateDir), "workspace")

	topology := &fakeProjectTopologyMaterializer{materialized: true}
	executor := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true}
	runner, native := scriptedPicker(t, []pickerStep{
		{reply: intpickercompat.Result{Value: projectStartupValueLatest}},
	})
	cmd := &switchCommand{
		sessions: executor,
		identity: stubSwitchIdentityResolver{name: "workspace"},
		homeDir:  func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			switch name {
			case "XDG_STATE_HOME":
				return filepath.Join(home, "state")
			case "XDG_CONFIG_HOME":
				return filepath.Join(home, "config")
			default:
				return ""
			}
		},
		runner:          runner,
		nativePicker:    native,
		projectTopology: topology,
	}

	if err := cmd.openProjectTarget(context.Background(), "/tmp/workspace", "workspace"); err != nil {
		t.Fatalf("openProjectTarget() error = %v", err)
	}
	if len(topology.calls) != 0 {
		t.Fatalf("snapshot startup activated Registry topology: %q", topology.calls)
	}
	if got, want := executor.calls, []string{"authorize:/tmp/workspace", "restore:workspace:" + sessionstate.SourceAutosave, "open:workspace"}; !equalStrings(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

// TestSwitchClosedProjectOpenTopologyFailureDoesNotMoveClient is acceptance 3 at
// the switch boundary: a failed activation surfaces the exact cause and performs
// no open and no EnsureSession fallback, so a partially built runtime is never
// presented as a successful start.
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
