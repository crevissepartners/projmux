package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// recordingStopContinueLauncher is the name handoff fake provider that also
// records every conversation a Continue replay resumes.
type recordingStopContinueLauncher struct {
	realTmuxNameHandoffLauncher
	resumed *[]string
}

func (l recordingStopContinueLauncher) PlanAgentResume(provider string, workspace coremetadata.AgentWorkspace, conversationID string, annotations map[string]string) (agentResumeLaunch, error) {
	*l.resumed = append(*l.resumed, provider+"/"+conversationID)
	return l.realTmuxNameHandoffLauncher.PlanAgentResume(provider, workspace, conversationID, annotations)
}

// addRunningStopAgent creates one claude Agent Running on a managed Pane whose
// activation names runtimeID, the shape a launched Agent has before any exit.
func addRunningStopAgent(t *testing.T, store *fakeResourceStore, windowUID, name, cwd, runtimeID string, ref *coremetadata.AgentSessionRef) (string, string) {
	t.Helper()
	mutator := store.mutator()
	agent, err := mutator.CreateAgent(&store.registry, windowUID, coremetadata.CreateAgentOptions{
		Name: name, Provider: "claude", Workspace: coremetadata.AgentWorkspace{CWD: cwd}, OperationID: "op-stop-offline-" + name,
	})
	if err != nil {
		t.Fatal(err)
	}
	pane, err := mutator.AttachAgentPane(&store.registry, agent.Metadata.UID, coremetadata.BootstrapPane{Name: name + "-pane", CWD: cwd}, "op-stop-offline-"+name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mutator.RecordPaneActivation(&store.registry, pane.Metadata.UID, coremetadata.PaneActivationOptions{
		Generation: "gen-" + name, RuntimeID: runtimeID, AgentUID: agent.Metadata.UID, OperationID: "op-launch-" + name,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := mutator.TransitionAgent(&store.registry, agent.Metadata.UID, coremetadata.PhaseRunning, "launched"); err != nil {
		t.Fatal(err)
	}
	stored, _ := store.registry.Agent(agent.Metadata.UID)
	stored.Status.SessionRef = ref
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("Running Agent %s fixture: %v", name, err)
	}
	return agent.Metadata.UID, pane.Metadata.UID
}

// addOtherStopProject adds a second Project with one Window, the Project a stop
// of the first must not touch.
func addOtherStopProject(t *testing.T, store *fakeResourceStore, root, sessionName string) {
	t.Helper()
	const projectUID, windowUID, paneUID = "prj-other", "win-other", "pan-other-shell"
	ownedBy := func(kind coremetadata.Kind, uid string) *coremetadata.OwnerRef {
		return &coremetadata.OwnerRef{Kind: kind, UID: uid}
	}
	store.registry.Projects = append(store.registry.Projects, coremetadata.Project{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
		Metadata: coremetadata.ObjectMeta{UID: projectUID, Name: "other", CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.ProjectSpec{Root: root, PrimaryWindowRef: windowUID},
		Status:   coremetadata.ProjectStatus{Session: &coremetadata.SessionProjection{Name: sessionName, Live: true}},
	})
	store.registry.Windows = append(store.registry.Windows, coremetadata.Window{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
		Metadata: coremetadata.ObjectMeta{UID: windowUID, Name: "main", OwnerRef: ownedBy(coremetadata.KindProject, projectUID), CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.WindowSpec{AnchorPaneRef: paneUID},
	})
	store.registry.Panes = append(store.registry.Panes, coremetadata.Pane{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
		Metadata: coremetadata.ObjectMeta{UID: paneUID, Name: "shell", OwnerRef: ownedBy(coremetadata.KindWindow, windowUID), CreatedAt: resourceFixtureClock},
		Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell, CWD: root},
	})
	store.registry.NameReservations = append(store.registry.NameReservations,
		coremetadata.NameReservation{Scope: "", Kind: coremetadata.KindProject, Name: "other", UID: projectUID},
		coremetadata.NameReservation{Scope: projectUID, Kind: coremetadata.KindWindow, Name: "main", UID: windowUID},
		coremetadata.NameReservation{Scope: projectUID, Kind: coremetadata.KindPane, Name: "shell", UID: paneUID},
	)
	store.registry = store.registry.Normalize()
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("other Project fixture: %v", err)
	}
}

// TestProjectStopLowersInterruptedAgentsOfflineThroughRealTmux is the Project
// stop half of the unregistered-Agent convergence, taken against a real tmux
// server. The stopped Project runs two claude Agents: one whose provider hook
// recorded a session ref and one whose hook never ran. Another Project runs a
// third Agent on the same server.
//
// When the stop returns, with no other command in between, both interrupted
// Agents are Offline with no paneRef and the interrupted reason, while the
// other Project's Agent is still Running on its live Pane. A Continue replay
// then resumes the registered Agent's exact conversation and skips the
// unregistered one with a notice that names `projmux create agent`.
func TestProjectStopLowersInterruptedAgentsOfflineThroughRealTmux(t *testing.T) {
	if os.Getenv(resumeNoAnchorRealTmuxEnv) == "1" {
		t.Setenv(resumedPaneNameRealTmuxEnv, "1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	server := startRealTmuxNameHandoffServer(t, ctx)
	projectRoot := filepath.Join(server.root, "project")
	otherRoot := filepath.Join(server.root, "other")
	for _, dir := range []string{projectRoot, otherRoot} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const sessionName, otherSession = "name-handoff", "other"

	// The other Project's session is the first one, so the server outlives the
	// stopped session.
	created, err := server.tmux("new-session", "-d", "-s", otherSession, "-n", "main", "-x", "400", "-y", "100", "-c", otherRoot,
		"-P", "-F", "#{pid}\t#{socket_path}\t#{session_id}\t#{pane_id}", "tail", "-f", "/dev/null")
	if err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, created)
	}
	t.Cleanup(server.killServer)
	fields := strings.Split(created, "\t")
	if len(fields) != 4 || fields[1] != server.socket {
		t.Fatalf("isolated tmux receipt = %q, want pid/socket/session/pane on %s", created, server.socket)
	}
	serverPID, otherSessionID, otherPaneID := fields[0], fields[2], fields[3]
	server.seed(t)
	created, err = server.tmux("new-session", "-d", "-s", sessionName, "-n", "main", "-c", projectRoot,
		"-P", "-F", "#{session_id}\t#{window_id}\t#{pane_id}", "tail", "-f", "/dev/null")
	if err != nil {
		t.Fatalf("start Project session: %v: %s", err, created)
	}
	fields = strings.Split(created, "\t")
	if len(fields) != 3 {
		t.Fatalf("Project session receipt = %q", created)
	}
	sessionID, windowID, shellPaneID := fields[0], fields[1], fields[2]
	agentPaneIDs := make([]string, 0, 2)
	for range 2 {
		paneID, err := server.tmux("split-window", "-d", "-t", windowID, "-c", projectRoot, "-P", "-F", "#{pane_id}", "tail", "-f", "/dev/null")
		if err != nil {
			t.Fatalf("split Agent Pane: %v: %s", err, paneID)
		}
		agentPaneIDs = append(agentPaneIDs, paneID)
	}

	store := realTmuxNameHandoffRegistry(t, projectRoot, sessionName, true)
	addOtherStopProject(t, store, otherRoot, otherSession)
	registeredUID, registeredPane := addRunningStopAgent(t, store, "win-name-handoff", "registered", projectRoot, agentPaneIDs[0], claudeConversationRef("conv-registered"))
	unregisteredUID, unregisteredPane := addRunningStopAgent(t, store, "win-name-handoff", "unregistered", projectRoot, agentPaneIDs[1], nil)
	otherUID, otherPane := addRunningStopAgent(t, store, "win-other", "bystander", otherRoot, otherPaneID, claudeConversationRef("conv-bystander"))
	for _, option := range [][]string{
		{"-t", sessionID, tmuxopts.ProjectUIDSession, "prj-name-handoff"},
		{"-t", sessionID, tmuxopts.ProjectPathSession, projectRoot},
		{"-t", otherSessionID, tmuxopts.ProjectUIDSession, "prj-other"},
		{"-t", otherSessionID, tmuxopts.ProjectPathSession, otherRoot},
		{"-w", "-t", windowID, tmuxopts.WindowUID, "win-name-handoff"},
		{"-p", "-t", shellPaneID, tmuxopts.PaneUID, "pan-name-handoff"},
		{"-p", "-t", agentPaneIDs[0], tmuxopts.PaneUID, registeredPane},
		{"-p", "-t", agentPaneIDs[1], tmuxopts.PaneUID, unregisteredPane},
		{"-p", "-t", otherPaneID, tmuxopts.PaneUID, otherPane},
	} {
		if out, err := server.tmux(append([]string{"set-option"}, option...)...); err != nil {
			t.Fatalf("seed isolated tmux %q: %v: %s", option, err, out)
		}
	}

	runner := shellTmuxExecRunner{env: func() []string { return server.environment }}
	target := managedRuntimeStopTarget{
		SessionID: sessionID, SessionName: sessionName, RootKind: coremetadata.KindProject, RootUID: "prj-name-handoff",
		Route: runtimeMutationRoute{
			target:     tmuxTransport{Kind: tmuxSocketName, Value: server.logical, Source: tmuxSocketNameSource},
			socketName: server.logical, expectedSocketPath: server.socket,
			authority: &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: serverPID},
		},
	}
	stopStore := store.store()
	if err := executeManagedRuntimeStop(ctx, runner, target, managedRuntimeStopRegistryAuthority(stopStore.snapshot), stopStore); err != nil {
		t.Fatalf("stop Project over the real server: %v", err)
	}

	sessions, err := server.tmux("list-sessions", "-F", "#{session_name}")
	if err != nil || sessions != otherSession {
		t.Fatalf("isolated tmux sessions after the stop = %q (%v), want only %q", sessions, err, otherSession)
	}
	for _, uid := range []string{registeredUID, unregisteredUID} {
		agent, ok := store.registry.Agent(uid)
		if !ok || agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" ||
			agent.Status.Reason != coremetadata.TerminationReasonInterrupted {
			t.Fatalf("agent %s after the stop = %+v, want Offline with no paneRef and reason %q",
				uid, agent.Status, coremetadata.TerminationReasonInterrupted)
		}
		if receipt := agent.Status.LastTermination; receipt == nil || receipt.Source != coremetadata.TerminationSourceControlAction ||
			receipt.Classification != coremetadata.TerminationInterrupted {
			t.Fatalf("agent %s termination after the stop = %+v, want the control-action interruption", uid, receipt)
		}
	}
	bystander, ok := store.registry.Agent(otherUID)
	if !ok || bystander.Status.Phase != coremetadata.PhaseRunning || bystander.Status.PaneRef != otherPane || bystander.Status.LastTermination != nil {
		t.Fatalf("the other Project's Agent after the stop = %+v, want Running on %s", bystander.Status, otherPane)
	}
	project, ok := store.registry.Project("prj-name-handoff")
	if !ok || project.Status.Session == nil || project.Status.Session.Live {
		t.Fatalf("stopped Project status = %+v, want a not-live session", project.Status)
	}

	var resumed []string
	materializeTarget := tmuxTransport{Kind: tmuxSocketName, Value: server.logical, Source: tmuxSocketNameSource}
	command := &resourceReconcileCommand{
		runner: runner, resources: store.store(), lookupEnv: func(string) string { return "" },
		agents: recordingStopContinueLauncher{resumed: &resumed},
		newReconciler: func(runner tmuxCommandRunner, sessions sessionLister) *registryReconciler {
			reconciler := reconcileFixtureReconciler(projectRoot, sessionName)(runner, sessions)
			reconciler.discoverRoots = func() ([]string, error) { return []string{projectRoot, otherRoot}, nil }
			reconciler.sessionNameFor = func(root string) string {
				if root == otherRoot {
					return otherSession
				}
				return sessionName
			}
			return reconciler
		},
		newOperationID: newCreateOperationID,
		newGeneration:  coremetadata.NewGeneration,
		newMaterializer: func(exact tmuxCommandRunner, warn io.Writer) *materializer {
			return &materializer{
				runner: exact, mirror: intmetadata.NewMirror(exact), sessions: defaultTmuxClientWithSocketRunner(exact, server.logical), target: materializeTarget, warn: warn,
				executable: func() (string, error) { return "", errors.New("no supervisor in the stop Continue test") },
			}
		},
	}
	var stdout, stderr bytes.Buffer
	if err := command.Run([]string{"resources", "--socket", server.logical, "--materialize-project", "name-handoff", "-o", "json"}, &stdout, &stderr); err != nil {
		t.Fatalf("Continue after the stop failed: %v\nstderr=%s\nstdout=%s", err, stderr.String(), stdout.String())
	}
	if len(resumed) != 1 || resumed[0] != "claude/conv-registered" {
		t.Fatalf("Continue resumed %v, want exactly the registered Agent's conversation", resumed)
	}
	registered, _ := store.registry.Agent(registeredUID)
	if registered.Status.Phase != coremetadata.PhaseRunning || registered.Status.PaneRef == "" || registered.Status.PaneRef == registeredPane {
		t.Fatalf("registered Agent after Continue = %+v, want Running on a new Pane", registered.Status)
	}
	unregistered, _ := store.registry.Agent(unregisteredUID)
	if unregistered.Status.Phase != coremetadata.PhaseOffline || unregistered.Status.PaneRef != "" {
		t.Fatalf("unregistered Agent after Continue = %+v, want it still Offline", unregistered.Status)
	}
	notice := stderr.String()
	for _, want := range []string{
		"agent/main/unregistered was not restored",
		"no provider session ref is recorded",
		"run `projmux create agent --provider claude`",
	} {
		if !strings.Contains(notice, want) {
			t.Fatalf("Continue stderr = %q, want it to contain %q", notice, want)
		}
	}
	bystander, _ = store.registry.Agent(otherUID)
	if bystander.Status.Phase != coremetadata.PhaseRunning || bystander.Status.PaneRef != otherPane {
		t.Fatalf("the other Project's Agent after Continue = %+v, want Running on %s", bystander.Status, otherPane)
	}
}
