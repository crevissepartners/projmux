package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// resumedPaneNameRealTmuxEnv makes tmux mandatory for the real-tmux name
// handoff tests. test/integration/resumed-agent-pane-names.sh sets it so a
// missing tmux fails the integration suite instead of skipping.
const resumedPaneNameRealTmuxEnv = "PMX_TEST_RESUMED_PANE_NAME_REAL_TMUX"

// realTmuxNameHandoffLauncher is the provider seam of both routes with a fake
// provider: every resume launches `tail -f /dev/null`, so the new Pane stays
// alive without a real claude or codex binary.
type realTmuxNameHandoffLauncher struct{}

func (realTmuxNameHandoffLauncher) RequireAgentEnabled(string) error { return nil }

func (realTmuxNameHandoffLauncher) PlanAgentLaunch(string, coremetadata.AgentWorkspace, []string) (string, []string, error) {
	return "", nil, errors.New("the name handoff test only resumes conversations")
}

func (realTmuxNameHandoffLauncher) PlanAgentResume(provider string, _ coremetadata.AgentWorkspace, _ string, _ map[string]string) (agentResumeLaunch, error) {
	return agentResumeLaunch{title: provider, argv: []string{"tail", "-f", "/dev/null"}}, nil
}

func (realTmuxNameHandoffLauncher) BindAgentPaneOnRoute(context.Context, tmuxCommandRunner, agentPaneBinding) error {
	return nil
}

// realTmuxNameHandoffServer is one isolated tmux server on a short private
// socket, reachable as the app-owned logical socket `pnh`.
type realTmuxNameHandoffServer struct {
	root, socket, logical string
	environment           []string
	tmux                  func(args ...string) (string, error)
}

func startRealTmuxNameHandoffServer(t *testing.T, ctx context.Context) realTmuxNameHandoffServer {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		if os.Getenv(resumedPaneNameRealTmuxEnv) == "1" {
			t.Fatalf("%s=1 requires tmux: %v", resumedPaneNameRealTmuxEnv, err)
		}
		t.Skip("tmux is not installed")
	}
	root, err := os.MkdirTemp("", "pnh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if root, err = filepath.EvalSymlinks(root); err != nil {
		t.Fatal(err)
	}
	environment := []string{"TMUX_TMPDIR=" + root}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "TMUX" || key == "TMUX_PANE" || key == "TMUX_TMPDIR" || key == runtimeMutationAnchorPaneEnv {
			continue
		}
		environment = append(environment, entry)
	}
	const logical = "pnh"
	socketDir := filepath.Join(root, fmt.Sprintf("tmux-%d", os.Getuid()))
	if err := os.Mkdir(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	server := realTmuxNameHandoffServer{root: root, socket: filepath.Join(socketDir, logical), logical: logical, environment: environment}
	server.tmux = func(args ...string) (string, error) {
		command := exec.CommandContext(ctx, "tmux", append([]string{"-S", server.socket, "-f", "/dev/null"}, args...)...)
		command.Env = environment
		out, err := command.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	return server
}

// killServer stops exactly this isolated server. It runs on its own context:
// the test body's context is already cancelled when t.Cleanup runs, and a
// kill-server bound to it would never start, orphaning the server once its
// socket root is removed. It is registered after the root removal, so LIFO
// cleanup kills the server first.
func (s realTmuxNameHandoffServer) killServer() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "tmux", "-S", s.socket, "-f", "/dev/null", "kill-server")
	command.Env = s.environment
	_ = command.Run()
}

// seed marks the server app-owned and gives every Pane a large, quiet shell.
func (s realTmuxNameHandoffServer) seed(t *testing.T) {
	t.Helper()
	for _, option := range [][]string{
		{"-g", tmuxopts.AppGlobal, "1"},
		{"-g", runtimeMutationSocketNameOption, s.logical},
		{"-g", "default-shell", "/bin/sh"},
		{"-g", "default-size", "400x100"},
	} {
		if out, err := s.tmux(append([]string{"set-option"}, option...)...); err != nil {
			t.Fatalf("seed isolated tmux %q: %v: %s", option, err, out)
		}
	}
}

// livePaneLabels maps each live Pane uid on the isolated server to its
// @projmux_pane_label mirror.
func (s realTmuxNameHandoffServer) livePaneLabels(t *testing.T) map[string][]string {
	t.Helper()
	out, err := s.tmux("list-panes", "-a", "-F", "#{"+tmuxopts.PaneUID+"}\t#{"+tmuxopts.PaneName+"}")
	if err != nil {
		t.Fatalf("list isolated tmux panes: %v: %s", err, out)
	}
	labels := map[string][]string{}
	for line := range strings.SplitSeq(out, "\n") {
		uid, label, _ := strings.Cut(line, "\t")
		if uid != "" {
			labels[uid] = append(labels[uid], label)
		}
	}
	return labels
}

// realTmuxNameHandoffRegistry is one Project with a single shell-anchored
// Window, the shape both routes materialize Agent Panes into.
func realTmuxNameHandoffRegistry(t *testing.T, projectRoot, sessionName string, live bool) *fakeResourceStore {
	t.Helper()
	const projectUID, windowUID, paneUID = "prj-name-handoff", "win-name-handoff", "pan-name-handoff"
	ownedBy := func(kind coremetadata.Kind, uid string) *coremetadata.OwnerRef {
		return &coremetadata.OwnerRef{Kind: kind, UID: uid}
	}
	registry := coremetadata.NewRegistry()
	registry.Projects = []coremetadata.Project{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
		Metadata: coremetadata.ObjectMeta{UID: projectUID, Name: "name-handoff", CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.ProjectSpec{Root: projectRoot, PrimaryWindowRef: windowUID},
		Status:   coremetadata.ProjectStatus{Session: &coremetadata.SessionProjection{Name: sessionName, Live: live}},
	}}
	registry.Windows = []coremetadata.Window{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
		Metadata: coremetadata.ObjectMeta{UID: windowUID, Name: "main", OwnerRef: ownedBy(coremetadata.KindProject, projectUID), CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.WindowSpec{AnchorPaneRef: paneUID},
	}}
	registry.Panes = []coremetadata.Pane{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
		Metadata: coremetadata.ObjectMeta{UID: paneUID, Name: "shell", OwnerRef: ownedBy(coremetadata.KindWindow, windowUID), CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: projectRoot},
	}}
	registry.NameReservations = []coremetadata.NameReservation{
		{Scope: "", Kind: coremetadata.KindProject, Name: "name-handoff", UID: projectUID},
		{Scope: projectUID, Kind: coremetadata.KindWindow, Name: "main", UID: windowUID},
		{Scope: projectUID, Kind: coremetadata.KindPane, Name: "shell", UID: paneUID},
	}
	registry = registry.Normalize()
	if err := registry.Validate(); err != nil {
		t.Fatalf("name handoff fixture is not a valid Registry: %v", err)
	}
	return &fakeResourceStore{registry: registry, dirs: map[string]bool{projectRoot: true}, now: resourceFixtureClock}
}

// realTmuxNameHandoffCase is one Agent with one old Pane row. An empty paneName
// is the automatic-name control.
type realTmuxNameHandoffCase struct {
	agentName, paneName string
	agentUID, oldUID    string
	oldName             string
}

func realTmuxNameHandoffCases() []realTmuxNameHandoffCase {
	return []realTmuxNameHandoffCase{
		{agentName: "explicit", paneName: "reviewer"},
		{agentName: "default", paneName: "default-pane"},
		{agentName: "flag", paneName: "-L"},
		{agentName: "automatic"},
	}
}

// addRealTmuxNameHandoffAgent creates one claude Agent and its old managed Pane
// row. terminated records the unplanned-exit evidence Continue requires;
// otherwise the row is released the way an Offline Agent keeps it.
func addRealTmuxNameHandoffAgent(t *testing.T, store *fakeResourceStore, projectRoot string, test *realTmuxNameHandoffCase, terminated bool) {
	t.Helper()
	mutator := store.mutator()
	agent, err := mutator.CreateAgent(&store.registry, "win-name-handoff", coremetadata.CreateAgentOptions{
		Name: test.agentName, Provider: "claude", Workspace: coremetadata.AgentWorkspace{CWD: projectRoot}, OperationID: "op-name-handoff-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := store.registry.Agent(agent.Metadata.UID)
	stored.Status.Phase = coremetadata.PhaseOffline
	stored.Status.SessionRef = claudeConversationRef("conv-" + test.agentName)
	old := attachTopologyAgentPane(t, store, agent.Metadata.UID, test.paneName, projectRoot)
	if terminated {
		markTopologyAgentInterrupted(t, store, agent.Metadata.UID, old.Metadata.UID)
	} else if _, err := mutator.TransitionAgent(&store.registry, agent.Metadata.UID, coremetadata.PhaseOffline, "earlier activation"); err != nil {
		t.Fatal(err)
	}
	test.agentUID, test.oldUID, test.oldName = agent.Metadata.UID, old.Metadata.UID, old.Metadata.Name
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("Agent %s fixture: %v", test.agentName, err)
	}
}

// assertRealTmuxNameHandoff checks one Agent after its new Pane exists: a new
// UID, the carried (or automatic) Registry name held by that Pane alone, and
// the same name in the live Pane's @projmux_pane_label.
func assertRealTmuxNameHandoff(t *testing.T, store *fakeResourceStore, labels map[string][]string, test realTmuxNameHandoffCase, oldRowReleased bool) {
	t.Helper()
	agent, ok := store.registry.Agent(test.agentUID)
	if !ok || agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef == "" || agent.Status.PaneRef == test.oldUID {
		t.Fatalf("agent/%s = %+v, want Running on a new Pane UID", test.agentName, agent.Status)
	}
	pane, ok := store.registry.Pane(agent.Status.PaneRef)
	if !ok {
		t.Fatalf("agent/%s paneRef %q resolves to no Pane", test.agentName, agent.Status.PaneRef)
	}
	want := test.oldName
	if test.paneName == "" {
		want = pane.Metadata.UID
	}
	if pane.Metadata.Name != want {
		t.Fatalf("agent/%s new pane/%s (uid:%s), want name %q", test.agentName, pane.Metadata.Name, pane.Metadata.UID, want)
	}
	if holders := paneNameReservationHolders(store.registry, "prj-name-handoff", want); len(holders) != 1 || holders[0] != pane.Metadata.UID {
		t.Fatalf("agent/%s name %q holders = %v, want only %s", test.agentName, want, holders, pane.Metadata.UID)
	}
	if _, retained := store.registry.Pane(test.oldUID); retained == oldRowReleased {
		t.Fatalf("agent/%s old row %s retained=%t, want released=%t", test.agentName, test.oldUID, retained, oldRowReleased)
	}
	if got := labels[pane.Metadata.UID]; len(got) != 1 || got[0] != want {
		t.Fatalf("agent/%s live %s of %s = %q, want exactly one Pane labelled %q", test.agentName, tmuxopts.PaneName, pane.Metadata.UID, got, want)
	}
	if _, live := labels[test.oldUID]; live {
		t.Fatalf("agent/%s old Pane UID %s is live", test.agentName, test.oldUID)
	}
}

// TestContinueReplayCarriesOldAgentPaneNamesThroughRealTmux drives Continue
// topology replay -- `reconcile resources --materialize-project` over the real
// planner, owner guard, and Agent split -- on an isolated tmux server. Agents
// whose old Pane was named `reviewer`, `default-pane` (the `create agent`
// default shape), and `-L` get new Pane UIDs that carry those names in the
// Registry and in @projmux_pane_label; the automatic-name control gets its own
// new UID as its name.
func TestContinueReplayCarriesOldAgentPaneNamesThroughRealTmux(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	server := startRealTmuxNameHandoffServer(t, ctx)
	if out, err := server.tmux("new-session", "-d", "-s", "keeper", "tail", "-f", "/dev/null"); err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, out)
	}
	t.Cleanup(server.killServer)
	server.seed(t)
	projectRoot := filepath.Join(server.root, "project")
	if err := os.Mkdir(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	const sessionName = "name-handoff"
	store := realTmuxNameHandoffRegistry(t, projectRoot, sessionName, false)
	cases := realTmuxNameHandoffCases()
	for i := range cases {
		addRealTmuxNameHandoffAgent(t, store, projectRoot, &cases[i], true)
	}

	runner := shellTmuxExecRunner{env: func() []string { return server.environment }}
	target := tmuxTransport{Kind: tmuxSocketName, Value: server.logical, Source: tmuxSocketNameSource}
	command := &resourceReconcileCommand{
		runner: runner, resources: store.store(), lookupEnv: func(string) string { return "" },
		agents:         realTmuxNameHandoffLauncher{},
		newReconciler:  reconcileFixtureReconciler(projectRoot, sessionName),
		newOperationID: newCreateOperationID,
		newGeneration:  coremetadata.NewGeneration,
		newMaterializer: func(exact tmuxCommandRunner, warn io.Writer) *materializer {
			return &materializer{
				runner: exact, mirror: intmetadata.NewMirror(exact), sessions: defaultTmuxClientWithSocketRunner(exact, server.logical), target: target, warn: warn,
				executable: func() (string, error) { return "", errors.New("no supervisor in the name handoff test") },
			}
		},
	}
	var stdout, stderr bytes.Buffer
	if err := command.Run([]string{"resources", "--socket", server.logical, "--materialize-project", "name-handoff", "-o", "json"}, &stdout, &stderr); err != nil {
		t.Fatalf("Continue replay failed: %v\nstderr=%s\nstdout=%s", err, stderr.String(), stdout.String())
	}
	if strings.Contains(stderr.String(), "keeps an automatic name") {
		t.Fatalf("Continue replay disclosed a lost name: %s", stderr.String())
	}
	labels := server.livePaneLabels(t)
	for _, test := range cases {
		assertRealTmuxNameHandoff(t, store, labels, test, true)
	}
}

// TestAgentResumeCarriesOldAgentPaneNamesThroughRealTmux drives explicit
// `agent resume` -- the rebinder's transaction, the server-wide non-live proof,
// the old-row release, and the split into the live Window -- on an isolated
// tmux server. The named old rows are released and their names land on the new
// Panes in the Registry and in @projmux_pane_label; the automatic-name control
// keeps its old row as evidence and names its new Pane by the new UID.
func TestAgentResumeCarriesOldAgentPaneNamesThroughRealTmux(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	server := startRealTmuxNameHandoffServer(t, ctx)
	projectRoot := filepath.Join(server.root, "project")
	if err := os.Mkdir(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	const sessionName = "name-handoff"
	created, err := server.tmux("new-session", "-d", "-s", sessionName, "-n", "main", "-x", "400", "-y", "100", "-c", projectRoot,
		"-P", "-F", "#{session_id}\t#{window_id}\t#{pane_id}\t#{pid}\t#{socket_path}", "tail", "-f", "/dev/null")
	if err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, created)
	}
	t.Cleanup(server.killServer)
	fields := strings.Split(created, "\t")
	if len(fields) != 5 || fields[4] != server.socket {
		t.Fatalf("isolated tmux receipt = %q, want session/window/pane/pid on %s", created, server.socket)
	}
	sessionID, windowID, paneID, serverPID := fields[0], fields[1], fields[2], fields[3]
	server.seed(t)
	for _, option := range [][]string{
		{"-t", sessionID, tmuxopts.ProjectUIDSession, "prj-name-handoff"},
		{"-t", sessionID, tmuxopts.ProjectPathSession, projectRoot},
		{"-w", "-t", windowID, tmuxopts.AutomaticRenameWindow, "off"},
		{"-w", "-t", windowID, tmuxopts.WindowUID, "win-name-handoff"},
		{"-w", "-t", windowID, tmuxopts.WindowName, "main"},
		{"-p", "-t", paneID, tmuxopts.PaneUID, "pan-name-handoff"},
	} {
		if out, err := server.tmux(append([]string{"set-option"}, option...)...); err != nil {
			t.Fatalf("seed isolated tmux %q: %v: %s", option, err, out)
		}
	}
	store := realTmuxNameHandoffRegistry(t, projectRoot, sessionName, true)
	cases := realTmuxNameHandoffCases()
	for i := range cases {
		addRealTmuxNameHandoffAgent(t, store, projectRoot, &cases[i], false)
	}

	inherited := map[string]string{"TMUX": server.socket + "," + serverPID + ",0", "TMUX_PANE": paneID}
	lookupEnv := func(key string) string { return inherited[key] }
	runner := shellTmuxExecRunner{env: func() []string { return server.environment }}
	target := tmuxTransport{Kind: tmuxSocketName, Value: server.logical, Source: tmuxSocketNameSource}
	routed := explicitTmuxRunner{runner: runner, target: target}
	client := defaultTmuxClientWithRunner(routed)
	create := &createCommand{
		store:      store.store(),
		reconciler: newRegistryReconciler(routed, client),
		runtime: &materializer{
			runner: routed, mirror: intmetadata.NewMirror(routed), sessions: client, target: target,
			warn:       testWarnWriter{t},
			executable: func() (string, error) { return "", errors.New("no supervisor in the name handoff test") },
			lookupEnv:  lookupEnv,
		},
		shell:          "/bin/sh",
		sessionNameFor: filepath.Base,
		newOperationID: newCreateOperationID,
		now:            time.Now,
		newGeneration:  coremetadata.NewGeneration,
	}
	bind := func(ctx context.Context, explicit bool) error {
		route, err := resolveInvocationRuntimeMutationRouteWithPolicy(ctx, runner, lookupEnv, create.routeAnchor, explicit)
		if err != nil {
			return err
		}
		exact := explicitTmuxRunner{runner: runner, target: route.target}
		exactClient := defaultTmuxClientWithSocketRunner(exact, route.socketName)
		create.reconciler = newRegistryReconcilerWithRoute(exact, exactClient, route)
		// Keep the reconciler off the real $HOME Project discovery.
		create.reconciler.discoverRoots = func() ([]string, error) { return nil, nil }
		create.runtime.runner = exact
		create.runtime.mirror = intmetadata.NewMirror(exact)
		create.runtime.sessions = exactClient
		create.runtime.target = route.target
		create.runtime.expectedSocketPath = route.expectedSocketPath
		create.runtime.socketName = route.socketName
		create.runtime.routeAuthority = route.authority
		return nil
	}
	create.bindRuntime = func(ctx context.Context) error { return bind(ctx, false) }
	create.bindExplicitRuntime = func(ctx context.Context) error { return bind(ctx, true) }
	resolveWorkspace := func(_ string, _ coremetadata.Registry, owner coremetadata.Project, _, cwd string, additional []string) (coremetadata.AgentWorkspace, error) {
		if strings.TrimSpace(cwd) == "" {
			cwd = owner.Spec.Root
		}
		return coremetadata.AgentWorkspace{CWD: cwd, AdditionalWritableRoots: additional}, nil
	}
	rebinder := newAgentRebinder(create, realTmuxNameHandoffLauncher{})
	rebinder.resolveWorkspace = resolveWorkspace
	agents := &agentCommand{
		loadRegistry: store.store().load, store: store.store(), now: time.Now,
		resolveWorkspace: resolveWorkspace, rebind: rebinder,
	}

	for _, test := range cases {
		stdout, stderr, err := runRoute(t, agents, "resume", "uid:"+test.agentUID)
		if err != nil || stdout != "agent/"+test.agentName+" resumed\n" {
			t.Fatalf("agent resume %s: err=%v stdout=%q stderr=%q", test.agentName, err, stdout, stderr)
		}
		if strings.Contains(stderr, "keeps an automatic name") {
			t.Fatalf("agent resume %s disclosed a lost name: %q", test.agentName, stderr)
		}
	}
	labels := server.livePaneLabels(t)
	for _, test := range cases {
		assertRealTmuxNameHandoff(t, store, labels, test, test.paneName != "")
	}
}
