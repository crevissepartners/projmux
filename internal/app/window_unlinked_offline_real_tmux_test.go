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

// TestWindowUnlinkedLowersAgentsOfAKilledSessionOfflineThroughRealTmux is the
// external-kill half of the unregistered-Agent convergence, taken against a
// real tmux server and the hook's own controller fast path. The Project runs
// two claude Agents in one Window: one whose provider hook recorded a session
// ref and one whose hook never ran. Another Project runs a third Agent on the
// same server.
//
// Moving the Agents' Window to the other session and back fires two
// window-unlinked events whose Panes are all still live, and neither lowers any
// Agent. A raw `tmux kill-session` of the Project then fires one more, and that
// single hook pass, with no other command, lowers both Agents to Offline with
// no paneRef and the supervisor's killed reason. The other Project's Agent stays
// Running. A Continue replay then resumes the registered Agent's exact
// conversation and skips the unregistered one.
func TestWindowUnlinkedLowersAgentsOfAKilledSessionOfflineThroughRealTmux(t *testing.T) {
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
	// killed session.
	created, err := server.tmux("new-session", "-d", "-s", otherSession, "-n", "main", "-x", "400", "-y", "100", "-c", otherRoot,
		"-P", "-F", "#{session_id}\t#{window_id}\t#{pane_id}", "tail", "-f", "/dev/null")
	if err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, created)
	}
	t.Cleanup(server.killServer)
	fields := strings.Split(created, "\t")
	if len(fields) != 3 {
		t.Fatalf("isolated tmux receipt = %q", created)
	}
	otherSessionID, otherWindowID, otherPaneID := fields[0], fields[1], fields[2]
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
	// An unmanaged keeper Window holds the Project session open while the
	// managed Window is moved away and back.
	if out, err := server.tmux("new-window", "-d", "-t", sessionID, "-n", "keeper", "tail", "-f", "/dev/null"); err != nil {
		t.Fatalf("add keeper Window: %v: %s", err, out)
	}
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
	// Record the exact bindings a materialization leaves: each Window's last
	// `$N/@N` handles, and the physical server each session projection names.
	for i := range store.registry.Windows {
		window := &store.registry.Windows[i]
		switch window.Metadata.UID {
		case "win-name-handoff":
			window.Status.RuntimeSessionID, window.Status.RuntimeID = sessionID, windowID
		case "win-other":
			window.Status.RuntimeSessionID, window.Status.RuntimeID = otherSessionID, otherWindowID
		}
	}
	for i := range store.registry.Projects {
		if session := store.registry.Projects[i].Status.Session; session != nil {
			session.SocketPath = server.socket
		}
	}
	for _, option := range [][]string{
		{"-t", sessionID, tmuxopts.ProjectUIDSession, "prj-name-handoff"},
		{"-t", sessionID, tmuxopts.ProjectPathSession, projectRoot},
		{"-t", otherSessionID, tmuxopts.ProjectUIDSession, "prj-other"},
		{"-t", otherSessionID, tmuxopts.ProjectPathSession, otherRoot},
		{"-w", "-t", windowID, tmuxopts.WindowUID, "win-name-handoff"},
		{"-w", "-t", otherWindowID, tmuxopts.WindowUID, "win-other"},
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
	hookTarget, err := tmuxSocketPathTarget(server.socket)
	if err != nil {
		t.Fatal(err)
	}
	journal := terminationJournal{path: filepath.Join(t.TempDir(), terminationJournalFile)}
	controller := &controllerTriggerRunner{
		runner: runner, store: store.store(),
		events: controllerEventLog{dir: t.TempDir()}, receipts: journal,
		newReconciler: func(tmuxCommandRunner, sessionLister) *registryReconciler {
			t.Error("the window-unlinked hook ran the widened reconciliation pass")
			return nil
		},
	}
	unlink := func(session string) controllerTriggerOutcome {
		t.Helper()
		outcome, err := controller.run(ctx, controllerTrigger{
			reason: controllerTriggerWindowUnlinked, target: hookTarget, session: session, hookWindow: windowID,
		})
		if err != nil {
			t.Fatalf("window-unlinked hook for %s/%s: %v", session, windowID, err)
		}
		return outcome
	}
	requireRunning := func(stage string) {
		t.Helper()
		for uid, pane := range map[string]string{registeredUID: registeredPane, unregisteredUID: unregisteredPane, otherUID: otherPane} {
			agent, ok := store.registry.Agent(uid)
			if !ok || agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef != pane || agent.Status.LastTermination != nil {
				t.Fatalf("agent %s %s = %+v, want Running on %s", uid, stage, agent.Status, pane)
			}
		}
	}

	// A Window moved away keeps every Pane live; its unlink is no absence.
	if out, err := server.tmux("move-window", "-d", "-s", windowID, "-t", otherSessionID+":"); err != nil {
		t.Fatalf("move the Agents' Window away: %v: %s", err, out)
	}
	before := store.snapshot()
	if outcome := unlink(sessionID); outcome.changed != 0 {
		t.Fatalf("unlink of the moved Window = %s, want no change", outcome.describe())
	}
	requireRunning("after the Window moved away")
	if out, err := server.tmux("move-window", "-d", "-s", windowID, "-t", sessionID+":"); err != nil {
		t.Fatalf("move the Agents' Window back: %v: %s", err, out)
	}
	if outcome := unlink(otherSessionID); outcome.changed != 0 {
		t.Fatalf("unlink of the returned Window = %s, want no change", outcome.describe())
	}
	requireRunning("after the Window moved back")
	if store.snapshot() != before {
		t.Fatal("a live Window's unlink wrote the Registry")
	}

	// The supervisors receive SIGHUP from the killed session and write their
	// killed receipts, as projmux internal supervise does.
	if out, err := server.tmux("kill-session", "-t", sessionID); err != nil {
		t.Fatalf("kill the Project session outside projmux: %v: %s", err, out)
	}
	for _, receipt := range []struct{ agent, pane, generation string }{
		{registeredUID, registeredPane, "gen-registered"},
		{unregisteredUID, unregisteredPane, "gen-unregistered"},
	} {
		if err := journal.append(coremetadata.TerminationEvidence{
			Source: coremetadata.TerminationSourceSupervisor, Classification: coremetadata.TerminationKilled,
			ObservedAt: resourceFixtureClock, PaneUID: receipt.pane, AgentUID: receipt.agent,
			Generation: receipt.generation, Signal: "HUP",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if outcome := unlink(sessionID); outcome.changed == 0 {
		t.Fatalf("unlink of the killed session's Window = %s, want the Agents projected", outcome.describe())
	}

	sessions, err := server.tmux("list-sessions", "-F", "#{session_name}")
	if err != nil || sessions != otherSession {
		t.Fatalf("isolated tmux sessions after the kill = %q (%v), want only %q", sessions, err, otherSession)
	}
	for _, uid := range []string{registeredUID, unregisteredUID} {
		agent, ok := store.registry.Agent(uid)
		if !ok || agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != "" ||
			agent.Status.Reason != coremetadata.TerminationReasonKilled {
			t.Fatalf("agent %s after the kill = %+v, want Offline with no paneRef and reason %q",
				uid, agent.Status, coremetadata.TerminationReasonKilled)
		}
	}
	bystander, ok := store.registry.Agent(otherUID)
	if !ok || bystander.Status.Phase != coremetadata.PhaseRunning || bystander.Status.PaneRef != otherPane || bystander.Status.LastTermination != nil {
		t.Fatalf("the other Project's Agent after the kill = %+v, want Running on %s", bystander.Status, otherPane)
	}
	project, ok := store.registry.Project("prj-name-handoff")
	if !ok || project.Status.Session == nil || project.Status.Session.Live {
		t.Fatalf("killed Project status = %+v, want a not-live session", project.Status)
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
				executable: func() (string, error) { return "", errors.New("no supervisor in the kill-session Continue test") },
			}
		},
	}
	var stdout, stderr bytes.Buffer
	if err := command.Run([]string{"resources", "--socket", server.logical, "--materialize-project", "name-handoff", "-o", "json"}, &stdout, &stderr); err != nil {
		t.Fatalf("Continue after the kill failed: %v\nstderr=%s\nstdout=%s", err, stderr.String(), stdout.String())
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
	if notice := stderr.String(); !strings.Contains(notice, "agent/main/unregistered was not restored") ||
		!strings.Contains(notice, "run `projmux create agent --provider claude`") {
		t.Fatalf("Continue stderr = %q, want the unregistered Agent skipped with the create agent notice", notice)
	}
	bystander, _ = store.registry.Agent(otherUID)
	if bystander.Status.Phase != coremetadata.PhaseRunning || bystander.Status.PaneRef != otherPane {
		t.Fatalf("the other Project's Agent after Continue = %+v, want Running on %s", bystander.Status, otherPane)
	}
}
