package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
)

// personaAttachAgent is the fixture Agent `agent persona` targets: the
// Running agt-alpha-codex, made a Claude Agent with a stored conversation.
const personaAttachAgent = "agt-alpha-codex"

// personaAttachPane is its managed Pane before any attach.
const personaAttachPane = "pan-alpha-codex"

// personaAttachRecovery is the recovery command a failed resume prints for the
// fixture Agent.
const personaAttachRecovery = "projmux agent resume uid:agt-alpha-codex --project uid:prj-alpha --window uid:win-alpha-main"

// personaAttachFixture is one wired `agent` namespace over the in-memory
// registry, tmux server, and delete runtime, with the real resume seam.
type personaAttachFixture struct {
	store    *fakeResourceStore
	tmux     *fakeTmux
	command  *agentCommand
	planner  *aiCommand
	launcher *exactArgvResumeLauncher
	deletes  *fakePaneDeleteRuntime
	delete   *deleteCommand
	env      map[string]string
}

func newPersonaAttachFixture(t *testing.T) *personaAttachFixture {
	t.Helper()
	store := newFakeResourceStore(t)
	agent, _ := store.registry.Agent(personaAttachAgent)
	agent.Spec.Provider = aiModeClaude
	agent.Metadata.Labels = nil
	agent.Status.SessionRef = claudeConversationRef(personaResumeConversation)
	agent.Status.Interaction = coremetadata.AgentInteraction{
		Kind: coremetadata.InteractionIdle, ObservedAt: resourceFixtureClock,
		Source: string(coremetadata.InteractionSourceProviderHook),
	}
	if err := store.registry.Validate(); err != nil {
		t.Fatal(err)
	}

	tmux := newFakeTmux()
	command, recorder, _, _ := newTestAgentResumeCommand(t, store, tmux)
	planner := agentLaunchArgvTestCommand(t)
	launcher := &exactArgvResumeLauncher{fakeResumeLauncher: recorder, planner: planner}
	command.rebind.launcher = launcher
	deletes := newFixturePaneDeleteRuntime()
	deleteCmd := newTestDeleteCommand(store, false, false, nil)
	deleteCmd.panes = deletes
	command.paneDelete = deleteCmd
	command.personaStore = func() (persona.Store, error) { return personaStoreFor(t, planner), nil }
	env := map[string]string{"TMUX": testDeleteEnvironment["TMUX"]}
	command.lookupEnv = func(name string) string { return env[name] }
	// The live half of the fixture is the fake delete runtime, so a managed
	// Pane is alive until that runtime has killed it.
	command.managedPaneLive = func(_ tmuxTransport, paneUID string) (bool, error) {
		return !slices.ContainsFunc(deletes.killed, func(killed paneLiveDeleteTarget) bool { return killed.PaneUID == paneUID }), nil
	}
	return &personaAttachFixture{
		store: store, tmux: tmux, command: command, planner: planner,
		launcher: launcher, deletes: deletes, delete: deleteCmd, env: env,
	}
}

func (f *personaAttachFixture) agent(t *testing.T) coremetadata.Agent {
	t.Helper()
	agent, ok := f.store.registry.Agent(personaAttachAgent)
	if !ok {
		t.Fatalf("agent %s disappeared", personaAttachAgent)
	}
	return agent.Clone()
}

func (f *personaAttachFixture) writePersona(t *testing.T, name, content string) {
	t.Helper()
	if _, err := personaStoreFor(t, f.planner).Write(name, []byte(content)); err != nil {
		t.Fatal(err)
	}
}

func (f *personaAttachFixture) snapshotPath(t *testing.T, content string) string {
	t.Helper()
	path, err := personaStoreFor(t, f.planner).SnapshotPath(persona.Digest([]byte(content)))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// snapshotFiles lists the snapshot directory, which does not exist until the
// first snapshot is written.
func (f *personaAttachFixture) snapshotFiles(t *testing.T) []string {
	t.Helper()
	paths, err := configPaths(f.planner.homeDir, f.planner.lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(paths.StateDir, persona.DirName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func (f *personaAttachFixture) setInteraction(kind coremetadata.AgentInteractionKind) {
	agent, _ := f.store.registry.Agent(personaAttachAgent)
	if kind == coremetadata.InteractionUnknown {
		agent.Status.Interaction = coremetadata.AgentInteraction{}
		return
	}
	agent.Status.Interaction = coremetadata.AgentInteraction{
		Kind: kind, ObservedAt: resourceFixtureClock, Source: string(coremetadata.InteractionSourceProviderHook),
	}
}

func (f *personaAttachFixture) setAnnotations(annotations map[string]string) {
	agent, _ := f.store.registry.Agent(personaAttachAgent)
	agent.Metadata.Annotations = annotations
}

// lastArgvTail is the provider argv of the last planned resume launch.
func (f *personaAttachFixture) lastArgvTail(t *testing.T) []string {
	t.Helper()
	if len(f.launcher.argv) == 0 {
		t.Fatal("no resume launch was planned")
	}
	return execArgvTail(t, f.launcher.argv[len(f.launcher.argv)-1], aiModeClaude)
}

// assertRestartedOnTheSameConversation checks the identity half of an attach:
// the same Agent is Running on a new managed Pane, exactly one provider launch
// happened, and it resumed the stored conversation.
func (f *personaAttachFixture) assertRestartedOnTheSameConversation(t *testing.T, oldPane string) coremetadata.Agent {
	t.Helper()
	after := f.agent(t)
	if after.Metadata.UID != personaAttachAgent || after.Status.Phase != coremetadata.PhaseRunning {
		t.Fatalf("agent after attach = uid %s phase %s, want the same Agent Running", after.Metadata.UID, after.Status.Phase)
	}
	if after.Status.PaneRef == "" || after.Status.PaneRef == oldPane {
		t.Fatalf("agent paneRef = %q, want a new managed Pane (old %q)", after.Status.PaneRef, oldPane)
	}
	pane, ok := f.store.registry.Pane(after.Status.PaneRef)
	if !ok || pane.Metadata.OwnerUID() != personaAttachAgent || pane.Spec.Role != coremetadata.PaneRoleAgent {
		t.Fatalf("new managed pane %q = %+v", after.Status.PaneRef, pane)
	}
	if _, ok := f.store.registry.Pane(oldPane); ok {
		t.Fatalf("old managed pane %q is still in the registry", oldPane)
	}
	if !after.Status.SessionRef.SameConversation(claudeConversationRef(personaResumeConversation)) {
		t.Fatalf("session ref changed: %+v", after.Status.SessionRef)
	}
	if len(f.deletes.killed) != 1 || f.deletes.killed[0].PaneUID != oldPane {
		t.Fatalf("delete pane killed %+v, want exactly the old managed pane %s", f.deletes.killed, oldPane)
	}
	assertOnlyResumeLaunches(t, f.tmux, personaResumeConversation, 1)
	calls := splitWindowCalls(f.tmux)
	separator := slices.Index(calls[0], "--")
	if separator < 0 || !slices.Equal(calls[0][separator+1:], f.launcher.argv[len(f.launcher.argv)-1]) {
		t.Fatalf("launched child argv = %q, want the planned %q", calls[0], f.launcher.argv[len(f.launcher.argv)-1])
	}
	return after
}

// assertNothingChanged is the zero-trace half of every refusal: no snapshot
// written, no Registry write, no Pane closed, no provider launched.
func (f *personaAttachFixture) assertNothingChanged(t *testing.T, before string, beforeAnnotations map[string]string) {
	t.Helper()
	if files := f.snapshotFiles(t); len(files) != 0 {
		t.Fatalf("a refused run wrote persona snapshots %v", files)
	}
	if f.store.writes != 0 || f.store.snapshot() != before {
		t.Fatalf("a refused run changed the registry: writes=%d", f.store.writes)
	}
	if got := f.agent(t).Metadata.Annotations; !mapsEqual(got, beforeAnnotations) {
		t.Fatalf("a refused run changed the annotations: %v, want %v", got, beforeAnnotations)
	}
	if f.deletes.preflights != 0 || len(f.deletes.killed) != 0 {
		t.Fatalf("a refused run reached delete pane: preflights=%d killed=%+v", f.deletes.preflights, f.deletes.killed)
	}
	if calls := splitWindowCalls(f.tmux); len(calls) != 0 {
		t.Fatalf("a refused run launched a provider: %v", calls)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if other, ok := b[key]; !ok || other != value {
			return false
		}
	}
	return true
}

// TestAgentPersonaAttachRestartsAnIdleRunningClaudeAgentWithThePersonaOnTheSameConversation
// is acceptance 1: the same Agent comes back Running on a new managed Pane,
// launched with the persona snapshot, the snapshot mode off, and the stored
// conversation, and records all three annotations.
func TestAgentPersonaAttachRestartsAnIdleRunningClaudeAgentWithThePersonaOnTheSameConversation(t *testing.T) {
	f := newPersonaAttachFixture(t)
	f.writePersona(t, "go-reviewer", personaResumeContent)
	agentCount := len(f.store.registry.Agents)

	stdout, stderr, err := runRoute(t, f.command, "persona", "attach", "uid:"+personaAttachAgent, "go-reviewer")
	if err != nil {
		t.Fatalf("attach: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if !strings.Contains(stdout, "agent/codex persona attached: now persona go-reviewer") || !strings.Contains(stdout, "restarted on the same conversation") {
		t.Fatalf("attach stdout = %q", stdout)
	}
	if len(f.store.registry.Agents) != agentCount {
		t.Fatalf("attach changed the Agent count to %d", len(f.store.registry.Agents))
	}
	after := f.assertRestartedOnTheSameConversation(t, personaAttachPane)

	snapshot := f.snapshotPath(t, personaResumeContent)
	paths, _ := configPaths(f.planner.homeDir, f.planner.lookupEnv)
	if !strings.HasPrefix(snapshot, filepath.Join(paths.StateDir, persona.DirName, "sha256-")) || !strings.HasSuffix(snapshot, ".md") {
		t.Fatalf("snapshot path %q is not <StateDir>/personas/sha256-<hex>.md", snapshot)
	}
	if content, err := os.ReadFile(snapshot); err != nil || string(content) != personaResumeContent {
		t.Fatalf("snapshot holds %q (%v)", content, err)
	}
	want := []string{"--append-system-prompt-file", snapshot, "--system-prompt-snapshot", "off", "--resume", personaResumeConversation}
	if got := f.lastArgvTail(t); !slices.Equal(got, want) {
		t.Fatalf("resumed exec argv tail = %q, want %q", got, want)
	}
	assertNoPersonaContent(t, f.launcher.argv[0], personaResumeContent)
	wantAnnotations := map[string]string{
		coremetadata.AnnotationAgentPersona:              "go-reviewer",
		coremetadata.AnnotationAgentPersonaDigest:        persona.Digest([]byte(personaResumeContent)),
		coremetadata.AnnotationAgentSystemPromptSnapshot: coremetadata.SystemPromptSnapshotOff,
	}
	if !mapsEqual(after.Metadata.Annotations, wantAnnotations) {
		t.Fatalf("annotations = %v, want %v", after.Metadata.Annotations, wantAnnotations)
	}
}

// TestSystemPromptSnapshotOffReachesBothResumeConsumers is acceptance 2: the
// resume seam adds `--system-prompt-snapshot off` for exactly the Claude
// Agents that record it, on `agent resume` and on Continue/topology replay,
// and leaves every other argv byte-identical.
func TestSystemPromptSnapshotOffReachesBothResumeConsumers(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	withPersona, snapshot := createPersonaForResume(t, planner, "go-reviewer", []byte(personaResumeContent))
	off := map[string]string{coremetadata.AnnotationAgentSystemPromptSnapshot: coremetadata.SystemPromptSnapshotOff}
	withPersonaOff := map[string]string{coremetadata.AnnotationAgentSystemPromptSnapshot: coremetadata.SystemPromptSnapshotOff}
	maps.Copy(withPersonaOff, withPersona)
	root := t.TempDir()

	for _, test := range []struct {
		name        string
		annotations map[string]string
		wantTail    []string
	}{
		{"no annotation", nil, []string{"--resume", personaResumeConversation}},
		{"persona without off", withPersona, []string{"--append-system-prompt-file", snapshot, "--resume", personaResumeConversation}},
		{"off without persona", off, []string{"--system-prompt-snapshot", "off", "--resume", personaResumeConversation}},
		{"persona and off", withPersonaOff, []string{"--append-system-prompt-file", snapshot, "--system-prompt-snapshot", "off", "--resume", personaResumeConversation}},
	} {
		t.Run("agent resume/"+test.name, func(t *testing.T) {
			_, argv, _ := resumeClaudeAgentWithAnnotations(t, planner, test.annotations)
			if got := execArgvTail(t, argv, aiModeClaude); !slices.Equal(got, test.wantTail) {
				t.Fatalf("agent resume exec argv tail = %q, want %q", got, test.wantTail)
			}
		})
		t.Run("topology replay/"+test.name, func(t *testing.T) {
			work, _ := planClaudeTopologyReplay(t, planner, root, test.annotations)
			if got := execArgvTail(t, work.argv, aiModeClaude); !slices.Equal(got, test.wantTail) {
				t.Fatalf("topology replay exec argv tail = %q, want %q", got, test.wantTail)
			}
		})
	}

	// Absent key: byte-identical to an Agent that never had any annotation,
	// on both consumers.
	_, plain, _ := resumeClaudeAgentWithAnnotations(t, planner, nil)
	_, other, _ := resumeClaudeAgentWithAnnotations(t, planner, map[string]string{coremetadata.AnnotationAgentTopic: "unrelated"})
	if !slices.Equal(plain, other) {
		t.Fatalf("an Agent without the snapshot key resumed with %q, want the unannotated %q", other, plain)
	}
	plainReplay, _ := planClaudeTopologyReplay(t, planner, root, nil)
	otherReplay, _ := planClaudeTopologyReplay(t, planner, root, map[string]string{coremetadata.AnnotationAgentTopic: "unrelated"})
	if !slices.Equal(plainReplay.argv, otherReplay.argv) {
		t.Fatalf("a replay without the snapshot key planned %q, want the unannotated %q", otherReplay.argv, plainReplay.argv)
	}

	// Another provider carrying the key gets no flag.
	workspace := coremetadata.AgentWorkspace{CWD: "/work/owner"}
	codexPlain, err := planner.PlanAgentResume(aiModeCodex, workspace, resumeFixtureConversation, nil)
	if err != nil {
		t.Fatal(err)
	}
	codexOff, err := planner.PlanAgentResume(aiModeCodex, workspace, resumeFixtureConversation, off)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(codexPlain.argv, codexOff.argv) {
		t.Fatalf("codex resume with the snapshot key = %q, want the unchanged %q", codexOff.argv, codexPlain.argv)
	}
}

// TestAgentPersonaDetachClearsThePersonaKeepsSnapshotOffAndRestarts is
// acceptance 3.
func TestAgentPersonaDetachClearsThePersonaKeepsSnapshotOffAndRestarts(t *testing.T) {
	f := newPersonaAttachFixture(t)
	f.writePersona(t, "go-reviewer", personaResumeContent)
	if _, _, err := runRoute(t, f.command, "persona", "attach", "uid:"+personaAttachAgent, "go-reviewer"); err != nil {
		t.Fatal(err)
	}
	attachedPane := f.agent(t).Status.PaneRef
	f.setInteraction(coremetadata.InteractionIdle)
	f.deletes.killed = nil
	f.tmux.calls = nil

	stdout, stderr, err := runRoute(t, f.command, "persona", "detach", "uid:"+personaAttachAgent)
	if err != nil {
		t.Fatalf("detach: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if !strings.Contains(stdout, "agent/codex persona detached: now no persona") {
		t.Fatalf("detach stdout = %q", stdout)
	}
	after := f.assertRestartedOnTheSameConversation(t, attachedPane)
	want := map[string]string{coremetadata.AnnotationAgentSystemPromptSnapshot: coremetadata.SystemPromptSnapshotOff}
	if !mapsEqual(after.Metadata.Annotations, want) {
		t.Fatalf("annotations after detach = %v, want %v", after.Metadata.Annotations, want)
	}
	tail := f.lastArgvTail(t)
	if slices.Contains(tail, "--append-system-prompt-file") {
		t.Fatalf("detached resume argv %q still passes a persona file", tail)
	}
	if wantTail := []string{"--system-prompt-snapshot", "off", "--resume", personaResumeConversation}; !slices.Equal(tail, wantTail) {
		t.Fatalf("detached resume exec argv tail = %q, want %q", tail, wantTail)
	}
}

// TestAgentPersonaAttachRefusesABusyAgentWithoutYesAndLeavesNoTrace is
// acceptance 4: a Running Agent whose interaction is in_progress or unknown
// is refused with persona-agent-busy and nothing changes; --yes proceeds; and
// the dry run reports the confirmation without changing anything.
func TestAgentPersonaAttachRefusesABusyAgentWithoutYesAndLeavesNoTrace(t *testing.T) {
	for _, kind := range []coremetadata.AgentInteractionKind{coremetadata.InteractionInProgress, coremetadata.InteractionUnknown} {
		t.Run(string(kind), func(t *testing.T) {
			f := newPersonaAttachFixture(t)
			f.writePersona(t, "go-reviewer", personaResumeContent)
			f.setInteraction(kind)
			before, beforeAnnotations := f.store.snapshot(), f.agent(t).Metadata.Annotations

			_, _, err := runRoute(t, f.command, "persona", "attach", "uid:"+personaAttachAgent, "go-reviewer")
			if err == nil || !strings.Contains(err.Error(), personaReasonAgentBusy) || !IsUsageError(err) {
				t.Fatalf("busy attach err = %v, want a %s usage refusal", err, personaReasonAgentBusy)
			}
			f.assertNothingChanged(t, before, beforeAnnotations)

			stdout, _, err := runRoute(t, f.command, "persona", "attach", "uid:"+personaAttachAgent, "go-reviewer", "--dry-run", "-o", "json")
			if err != nil {
				t.Fatalf("dry run: %v", err)
			}
			var result agentPersonaResult
			if err := json.Unmarshal([]byte(stdout), &result); err != nil {
				t.Fatalf("dry run output %q is not JSON: %v", stdout, err)
			}
			want := agentPersonaResult{
				Action: "attach", DryRun: true, Outcome: personaOutcomeWouldRestart,
				AgentUID: personaAttachAgent, AgentName: "codex", Provider: aiModeClaude,
				Phase: coremetadata.PhaseRunning, Interaction: kind, PaneUID: personaAttachPane,
				NewPersona: "go-reviewer", NewPersonaDigest: persona.Digest([]byte(personaResumeContent)),
				SystemPromptSnapshot: coremetadata.SystemPromptSnapshotOff,
				Restart:              true, ConfirmationRequired: true,
			}
			if result != want {
				t.Fatalf("dry run = %+v, want %+v", result, want)
			}
			f.assertNothingChanged(t, before, beforeAnnotations)

			if _, stderr, err := runRoute(t, f.command, "persona", "attach", "uid:"+personaAttachAgent, "go-reviewer", "--yes"); err != nil {
				t.Fatalf("attach --yes: stderr=%q err=%v", stderr, err)
			}
			f.assertRestartedOnTheSameConversation(t, personaAttachPane)
		})
	}
}

// TestAgentPersonaDryRunJSONOfAnIdleAgentNeedsNoConfirmation pins the dry-run
// JSON field names the web confirmation dialog reads.
func TestAgentPersonaDryRunJSONOfAnIdleAgentNeedsNoConfirmation(t *testing.T) {
	f := newPersonaAttachFixture(t)
	f.writePersona(t, "go-reviewer", personaResumeContent)
	before, beforeAnnotations := f.store.snapshot(), f.agent(t).Metadata.Annotations
	stdout, _, err := runRoute(t, f.command, "persona", "attach", "uid:"+personaAttachAgent, "go-reviewer", "--dry-run", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(stdout), &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"action", "dryRun", "outcome", "agentUID", "agentName", "provider", "phase", "interaction",
		"paneUID", "newPersona", "newPersonaDigest", "systemPromptSnapshot", "restart", "confirmationRequired", "unchanged"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("dry run JSON %s lacks %q", stdout, key)
		}
	}
	if fields["confirmationRequired"] != false || fields["interaction"] != "idle" || fields["outcome"] != personaOutcomeWouldRestart {
		t.Fatalf("idle dry run = %s", stdout)
	}
	f.assertNothingChanged(t, before, beforeAnnotations)
}

// TestAgentPersonaRefusalsCarryTheirReasonTokenAndLeaveNoTrace is acceptance
// 5.
func TestAgentPersonaRefusalsCarryTheirReasonTokenAndLeaveNoTrace(t *testing.T) {
	for _, test := range []struct {
		name    string
		persona string
		arrange func(*personaAttachFixture)
		reason  string
	}{
		{name: "missing persona", persona: "absent", reason: persona.ReasonNotFound},
		{name: "codex agent", persona: "go-reviewer", reason: persona.ReasonProviderUnsupported, arrange: func(f *personaAttachFixture) {
			agent, _ := f.store.registry.Agent(personaAttachAgent)
			agent.Spec.Provider = aiModeCodex
			agent.Status.SessionRef = codexConversationRef(resumeFixtureConversation)
		}},
		{name: "no session ref", persona: "go-reviewer", reason: personaReasonNoConversation, arrange: func(f *personaAttachFixture) {
			agent, _ := f.store.registry.Agent(personaAttachAgent)
			agent.Status.SessionRef = nil
		}},
		{name: "self target", persona: "go-reviewer", reason: personaReasonSelfTarget, arrange: func(f *personaAttachFixture) {
			pane, _ := f.store.registry.Pane(personaAttachPane)
			pane.Status.Activation.RuntimeID = "%77"
			f.env["TMUX_PANE"] = "%77"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPersonaAttachFixture(t)
			f.writePersona(t, "go-reviewer", personaResumeContent)
			if test.arrange != nil {
				test.arrange(f)
			}
			before, beforeAnnotations := f.store.snapshot(), f.agent(t).Metadata.Annotations
			_, _, err := runRoute(t, f.command, "persona", "attach", "uid:"+personaAttachAgent, test.persona, "--yes")
			if err == nil || !strings.Contains(err.Error(), test.reason) || !IsUsageError(err) {
				t.Fatalf("attach err = %v, want a %s usage refusal", err, test.reason)
			}
			f.assertNothingChanged(t, before, beforeAnnotations)
		})
	}
}

// TestAgentPersonaResumeFailureLeavesTheAgentOfflineWithTheNewPersonaAndARecoveryCommand
// is acceptance 6: a resume that fails after the stop keeps the annotations,
// prints the recovery command, and that command relaunches with the persona
// and the snapshot mode.
func TestAgentPersonaResumeFailureLeavesTheAgentOfflineWithTheNewPersonaAndARecoveryCommand(t *testing.T) {
	f := newPersonaAttachFixture(t)
	f.writePersona(t, "go-reviewer", personaResumeContent)
	working := f.command.rebind.launcher
	f.command.rebind.launcher = &failingPersonaResumeLauncher{exactArgvResumeLauncher: f.launcher}

	_, stderr, err := runRoute(t, f.command, "persona", "attach", "uid:"+personaAttachAgent, "go-reviewer")
	if err == nil {
		t.Fatal("attach with a failing resume succeeded")
	}
	after := f.agent(t)
	if after.Status.Phase != coremetadata.PhaseOffline {
		t.Fatalf("phase after a failed resume = %s, want Offline", after.Status.Phase)
	}
	if after.Metadata.Annotations[coremetadata.AnnotationAgentPersona] != "go-reviewer" ||
		after.Metadata.Annotations[coremetadata.AnnotationAgentPersonaDigest] != persona.Digest([]byte(personaResumeContent)) ||
		after.Metadata.Annotations[coremetadata.AnnotationAgentSystemPromptSnapshot] != coremetadata.SystemPromptSnapshotOff {
		t.Fatalf("annotations after a failed resume = %v, want the new persona kept", after.Metadata.Annotations)
	}
	if !strings.Contains(stderr, "projmux: recover with: "+personaAttachRecovery+"\n") {
		t.Fatalf("stderr = %q, want the recovery command %q", stderr, personaAttachRecovery)
	}
	if calls := splitWindowCalls(f.tmux); len(calls) != 0 {
		t.Fatalf("a failed resume launched %v", calls)
	}

	f.command.rebind.launcher = working
	recovery := strings.Fields(strings.TrimPrefix(personaAttachRecovery, "projmux agent "))
	if _, stderr, err := runRoute(t, f.command, recovery...); err != nil {
		t.Fatalf("recovery command: stderr=%q err=%v", stderr, err)
	}
	want := []string{"--append-system-prompt-file", f.snapshotPath(t, personaResumeContent), "--system-prompt-snapshot", "off", "--resume", personaResumeConversation}
	if got := f.lastArgvTail(t); !slices.Equal(got, want) {
		t.Fatalf("recovery resume exec argv tail = %q, want %q", got, want)
	}
	if f.agent(t).Status.Phase != coremetadata.PhaseRunning {
		t.Fatal("the recovery command did not bring the Agent back")
	}
}

// failingPersonaResumeLauncher fails every resume launch construction, the
// way a missing provider binary does.
type failingPersonaResumeLauncher struct {
	*exactArgvResumeLauncher
}

func (l *failingPersonaResumeLauncher) PlanAgentResume(string, coremetadata.AgentWorkspace, string, map[string]string) (agentResumeLaunch, error) {
	return agentResumeLaunch{}, errors.New("claude binary is not installed")
}

// TestAgentPersonaAttachOfTheSamePersonaAndDigestIsUnchanged is acceptance 7,
// plus its edge: an edited persona file is a different digest and restarts.
func TestAgentPersonaAttachOfTheSamePersonaAndDigestIsUnchanged(t *testing.T) {
	f := newPersonaAttachFixture(t)
	f.writePersona(t, "go-reviewer", personaResumeContent)
	f.setAnnotations(map[string]string{
		coremetadata.AnnotationAgentPersona:              "go-reviewer",
		coremetadata.AnnotationAgentPersonaDigest:        persona.Digest([]byte(personaResumeContent)),
		coremetadata.AnnotationAgentSystemPromptSnapshot: coremetadata.SystemPromptSnapshotOff,
	})
	f.setInteraction(coremetadata.InteractionInProgress)
	before, beforeAnnotations := f.store.snapshot(), f.agent(t).Metadata.Annotations

	stdout, _, err := runRoute(t, f.command, "persona", "attach", "uid:"+personaAttachAgent, "go-reviewer", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var result agentPersonaResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != personaOutcomeUnchanged || !result.Unchanged || result.Restart || result.NewPaneUID != personaAttachPane {
		t.Fatalf("same persona attach = %+v, want unchanged on pane %s", result, personaAttachPane)
	}
	if got := f.agent(t).Status.PaneRef; got != personaAttachPane {
		t.Fatalf("paneRef = %q, want unchanged %q", got, personaAttachPane)
	}
	f.assertNothingChanged(t, before, beforeAnnotations)

	// The file was edited: a new digest restarts with the new snapshot.
	const edited = "You review Go code and answer in one sentence."
	f.writePersona(t, "go-reviewer", edited)
	f.setInteraction(coremetadata.InteractionIdle)
	if _, stderr, err := runRoute(t, f.command, "persona", "attach", "uid:"+personaAttachAgent, "go-reviewer"); err != nil {
		t.Fatalf("attach of an edited persona: stderr=%q err=%v", stderr, err)
	}
	after := f.assertRestartedOnTheSameConversation(t, personaAttachPane)
	if got := after.Metadata.Annotations[coremetadata.AnnotationAgentPersonaDigest]; got != persona.Digest([]byte(edited)) {
		t.Fatalf("digest after an edited attach = %q, want the edited content's", got)
	}
	if tail := f.lastArgvTail(t); !slices.Contains(tail, f.snapshotPath(t, edited)) {
		t.Fatalf("edited attach argv %q does not pass the new snapshot", tail)
	}
}

// TestAgentPersonaAttachOutsideTmuxStopsThroughTheNamedSocket covers the
// callers with no tmux client, the web server and an isolated smoke: without
// $TMUX the stop needs --socket exactly like `delete pane`, it is refused
// with no trace when absent, and with it the delete and the resume both run.
func TestAgentPersonaAttachOutsideTmuxStopsThroughTheNamedSocket(t *testing.T) {
	f := newPersonaAttachFixture(t)
	f.writePersona(t, "go-reviewer", personaResumeContent)
	delete(f.env, "TMUX")
	f.delete.lookupEnv = func(string) string { return "" }
	before, beforeAnnotations := f.store.snapshot(), f.agent(t).Metadata.Annotations

	_, _, err := runRoute(t, f.command, "persona", "attach", "uid:"+personaAttachAgent, "go-reviewer")
	if err == nil || !strings.Contains(err.Error(), "requires --socket <name> or --socket-path <absolute> outside tmux") {
		t.Fatalf("attach outside tmux without --socket err = %v", err)
	}
	f.assertNothingChanged(t, before, beforeAnnotations)

	if _, stderr, err := runRoute(t, f.command, "persona", "attach", "uid:"+personaAttachAgent, "go-reviewer", "--socket", "isolated"); err != nil {
		t.Fatalf("attach --socket outside tmux: stderr=%q err=%v", stderr, err)
	}
	if f.deletes.boundTarget.Kind != tmuxSocketName || f.deletes.boundTarget.Value != "isolated" {
		t.Fatalf("delete pane routed to %+v, want socket name isolated", f.deletes.boundTarget)
	}
	f.assertRestartedOnTheSameConversation(t, personaAttachPane)
}

// TestAgentPersonaAttachOfAnOfflineAgentResumesWithoutAStop covers the
// Offline half of the route: no Pane is closed, the annotations change, and
// the Agent resumes with the persona.
func TestAgentPersonaAttachOfAnOfflineAgentResumesWithoutAStop(t *testing.T) {
	f := newPersonaAttachFixture(t)
	f.writePersona(t, "go-reviewer", personaResumeContent)
	if _, _, err := runRoute(t, f.delete, "pane", "uid:"+personaAttachPane, "--yes"); err != nil {
		t.Fatal(err)
	}
	if f.agent(t).Status.Phase != coremetadata.PhaseOffline {
		t.Fatal("fixture Agent is not Offline")
	}
	f.deletes.killed = nil

	stdout, stderr, err := runRoute(t, f.command, "persona", "attach", "uid:"+personaAttachAgent, "go-reviewer")
	if err != nil {
		t.Fatalf("attach Offline: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if !strings.Contains(stdout, "resumed on the same conversation") {
		t.Fatalf("offline attach stdout = %q", stdout)
	}
	if len(f.deletes.killed) != 0 {
		t.Fatalf("an Offline attach closed panes: %+v", f.deletes.killed)
	}
	if f.agent(t).Status.Phase != coremetadata.PhaseRunning {
		t.Fatal("the Offline Agent did not resume")
	}
	want := []string{"--append-system-prompt-file", f.snapshotPath(t, personaResumeContent), "--system-prompt-snapshot", "off", "--resume", personaResumeConversation}
	if got := f.lastArgvTail(t); !slices.Equal(got, want) {
		t.Fatalf("offline attach exec argv tail = %q, want %q", got, want)
	}
}

// TestAgentPersonaRequiresAnExplicitAgent pins that the restart command never
// takes its target from the active Pane.
func TestAgentPersonaRequiresAnExplicitAgent(t *testing.T) {
	f := newPersonaAttachFixture(t)
	for _, args := range [][]string{
		{"persona", "attach", "go-reviewer"},
		{"persona", "detach"},
		{"persona"},
		{"persona", "set", "uid:" + personaAttachAgent, "go-reviewer"},
	} {
		if _, _, err := runRoute(t, f.command, args...); err == nil || !IsUsageError(err) {
			t.Fatalf("%q err = %v, want a usage refusal", args, err)
		}
	}
	if f.store.writes != 0 {
		t.Fatalf("usage refusals wrote %d times", f.store.writes)
	}
}

// TestAgentPersonaStopFailureRestoresTheAnnotationsTheRunningSessionHas pins
// the one rollback: when the managed Pane cannot be closed the old provider
// session keeps running, so the Agent goes back to the annotations that
// session was launched with and a re-run is not mistaken for `unchanged`.
func TestAgentPersonaStopFailureRestoresTheAnnotationsTheRunningSessionHas(t *testing.T) {
	f := newPersonaAttachFixture(t)
	f.writePersona(t, "go-reviewer", personaResumeContent)
	f.deletes.killErr = errors.New("tmux kill-pane failed")
	beforeAnnotations := f.agent(t).Metadata.Annotations

	_, _, err := runRoute(t, f.command, "persona", "attach", "uid:"+personaAttachAgent, "go-reviewer")
	if err == nil || !strings.Contains(err.Error(), "persona annotations were restored") {
		t.Fatalf("attach with a failing stop err = %v", err)
	}
	after := f.agent(t)
	if after.Status.Phase != coremetadata.PhaseRunning || after.Status.PaneRef != personaAttachPane {
		t.Fatalf("agent after a failed stop = %s on %q, want Running on %s", after.Status.Phase, after.Status.PaneRef, personaAttachPane)
	}
	if !mapsEqual(after.Metadata.Annotations, beforeAnnotations) {
		t.Fatalf("annotations after a failed stop = %v, want the original %v", after.Metadata.Annotations, beforeAnnotations)
	}
	if calls := splitWindowCalls(f.tmux); len(calls) != 0 {
		t.Fatalf("a failed stop launched %v", calls)
	}
}

// personaAttachRerun is the command a stop failure with unobservable Pane
// liveness prints for the fixture Agent and the go-reviewer persona.
const personaAttachRerun = "projmux agent persona attach uid:agt-alpha-codex --project uid:prj-alpha --window uid:win-alpha-main go-reviewer"

// failingStopRoute is a `delete` route that reports stopErr. With closes set it
// first runs the real route to completion, the way a `delete pane` whose
// Registry commit and kill both happened fails while writing its result.
type failingStopRoute struct {
	route   rawArgvCommand
	closes  bool
	stopErr error
}

func (r failingStopRoute) Run(args []string, stdout, stderr io.Writer) error {
	if r.closes {
		if err := r.route.Run(args, stdout, stderr); err != nil {
			return err
		}
	}
	return r.stopErr
}

// assertNewPersonaAnnotations checks that the Agent records the go-reviewer
// persona with the snapshot mode off.
func (f *personaAttachFixture) assertNewPersonaAnnotations(t *testing.T) {
	t.Helper()
	want := map[string]string{
		coremetadata.AnnotationAgentPersona:              "go-reviewer",
		coremetadata.AnnotationAgentPersonaDigest:        persona.Digest([]byte(personaResumeContent)),
		coremetadata.AnnotationAgentSystemPromptSnapshot: coremetadata.SystemPromptSnapshotOff,
	}
	if got := f.agent(t).Metadata.Annotations; !mapsEqual(got, want) {
		t.Fatalf("annotations = %v, want the new persona kept %v", got, want)
	}
}

// TestAgentPersonaStopErrorAfterThePaneClosedKeepsTheNewPersonaAndRestarts is
// a stop whose Registry half committed and whose kill happened but which still
// reported an error: the old session is gone, so the new persona is kept, the
// run warns, and the Agent restarts with it.
func TestAgentPersonaStopErrorAfterThePaneClosedKeepsTheNewPersonaAndRestarts(t *testing.T) {
	f := newPersonaAttachFixture(t)
	f.writePersona(t, "go-reviewer", personaResumeContent)
	f.command.paneDelete = failingStopRoute{route: f.delete, closes: true, stopErr: errors.New("write delete result: no space left on device")}

	stdout, stderr, err := runRoute(t, f.command, "persona", "attach", "uid:"+personaAttachAgent, "go-reviewer", "-o", "json")
	if err != nil {
		t.Fatalf("attach after a closed-but-failed stop: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if !strings.Contains(stderr, "projmux: warning: closing agent/codex's managed pane "+personaAttachPane+" reported an error, but that pane is already closed") ||
		!strings.Contains(stderr, "new persona is kept and the Agent is resumed: write delete result: no space left on device\n") {
		t.Fatalf("stderr = %q, want the closed-pane warning with the stop error", stderr)
	}
	after := f.assertRestartedOnTheSameConversation(t, personaAttachPane)
	var result agentPersonaResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("attach output %q is not JSON: %v", stdout, err)
	}
	if result.Outcome != personaOutcomeRestarted || result.NewPaneUID != after.Status.PaneRef {
		t.Fatalf("attach result = %+v, want restarted on %s", result, after.Status.PaneRef)
	}
	f.assertNewPersonaAnnotations(t)
	want := []string{"--append-system-prompt-file", f.snapshotPath(t, personaResumeContent), "--system-prompt-snapshot", "off", "--resume", personaResumeConversation}
	if got := f.lastArgvTail(t); !slices.Equal(got, want) {
		t.Fatalf("resumed exec argv tail = %q, want %q", got, want)
	}
}

// TestAgentPersonaStopErrorAfterThePaneClosedWithAFailedResumePrintsTheRecoveryCommand
// is the same closed-but-failed stop followed by a resume that fails: the
// Agent is Offline with the new persona, and the printed `agent resume`
// command brings it back with that persona.
func TestAgentPersonaStopErrorAfterThePaneClosedWithAFailedResumePrintsTheRecoveryCommand(t *testing.T) {
	f := newPersonaAttachFixture(t)
	f.writePersona(t, "go-reviewer", personaResumeContent)
	f.command.paneDelete = failingStopRoute{route: f.delete, closes: true, stopErr: errors.New("write delete result: no space left on device")}
	working := f.command.rebind.launcher
	f.command.rebind.launcher = &failingPersonaResumeLauncher{exactArgvResumeLauncher: f.launcher}

	_, stderr, err := runRoute(t, f.command, "persona", "attach", "uid:"+personaAttachAgent, "go-reviewer")
	if err == nil {
		t.Fatal("attach with a closed-but-failed stop and a failing resume succeeded")
	}
	if phase := f.agent(t).Status.Phase; phase != coremetadata.PhaseOffline {
		t.Fatalf("phase = %s, want Offline", phase)
	}
	f.assertNewPersonaAnnotations(t)
	if !strings.Contains(stderr, "that pane is already closed, so its new persona is kept") ||
		!strings.Contains(stderr, "projmux: recover with: "+personaAttachRecovery+"\n") {
		t.Fatalf("stderr = %q, want the closed-pane warning and the recovery command %q", stderr, personaAttachRecovery)
	}
	if calls := splitWindowCalls(f.tmux); len(calls) != 0 {
		t.Fatalf("a failed resume launched %v", calls)
	}

	f.command.rebind.launcher = working
	recovery := strings.Fields(strings.TrimPrefix(personaAttachRecovery, "projmux agent "))
	if _, stderr, err := runRoute(t, f.command, recovery...); err != nil {
		t.Fatalf("recovery command: stderr=%q err=%v", stderr, err)
	}
	want := []string{"--append-system-prompt-file", f.snapshotPath(t, personaResumeContent), "--system-prompt-snapshot", "off", "--resume", personaResumeConversation}
	if got := f.lastArgvTail(t); !slices.Equal(got, want) {
		t.Fatalf("recovery resume exec argv tail = %q, want %q", got, want)
	}
	if f.agent(t).Status.Phase != coremetadata.PhaseRunning {
		t.Fatal("the recovery command did not bring the Agent back")
	}
}

// TestAgentPersonaStopErrorWithTheLivePaneGoneBeforeTheRegistryCommitKeepsTheNewPersona
// is a stop that killed the live Pane and then failed its Registry commit: the
// Registry still says Running, but the exact server `delete pane` addressed
// has no mirror of the Pane. The new persona is kept, and the resume, which
// the Running phase refuses, prints the recovery command.
func TestAgentPersonaStopErrorWithTheLivePaneGoneBeforeTheRegistryCommitKeepsTheNewPersona(t *testing.T) {
	f := newPersonaAttachFixture(t)
	f.writePersona(t, "go-reviewer", personaResumeContent)
	f.command.paneDelete = failingStopRoute{stopErr: errors.New("registry write failed; exact live target(s) %32/pane-uid=pan-alpha-codex were removed before the store failure")}
	var observed []string
	f.command.managedPaneLive = func(target tmuxTransport, paneUID string) (bool, error) {
		if target != testDeleteTarget {
			t.Errorf("liveness observed on %+v, want the server delete pane addresses %+v", target, testDeleteTarget)
		}
		observed = append(observed, paneUID)
		return false, nil
	}

	_, stderr, err := runRoute(t, f.command, "persona", "attach", "uid:"+personaAttachAgent, "go-reviewer")
	if err == nil {
		t.Fatal("attach whose stop left the Registry Running succeeded")
	}
	if !slices.Equal(observed, []string{personaAttachPane}) {
		t.Fatalf("liveness observed %q, want exactly %s", observed, personaAttachPane)
	}
	f.assertNewPersonaAnnotations(t)
	after := f.agent(t)
	if after.Status.Phase != coremetadata.PhaseRunning || after.Status.PaneRef != personaAttachPane {
		t.Fatalf("agent = %s on %q, want the Registry left Running on %s", after.Status.Phase, after.Status.PaneRef, personaAttachPane)
	}
	if !strings.Contains(stderr, "that pane is already closed, so its new persona is kept") ||
		!strings.Contains(stderr, "were removed before the store failure\n") ||
		!strings.Contains(stderr, "did not resume") ||
		!strings.Contains(stderr, "projmux: recover with: "+personaAttachRecovery+"\n") {
		t.Fatalf("stderr = %q, want the closed-pane warning and the recovery command", stderr)
	}
	if calls := splitWindowCalls(f.tmux); len(calls) != 0 {
		t.Fatalf("a refused resume launched %v", calls)
	}
}

// TestAgentPersonaStopErrorWithUnobservablePaneLivenessRestoresAndPrintsTheRerunCommand
// is a failed stop whose Pane liveness cannot be observed: the previous
// annotations come back as for a Pane still alive, stderr names the
// observation error and the same command to re-run, and that command finishes
// the attach.
func TestAgentPersonaStopErrorWithUnobservablePaneLivenessRestoresAndPrintsTheRerunCommand(t *testing.T) {
	f := newPersonaAttachFixture(t)
	f.writePersona(t, "go-reviewer", personaResumeContent)
	f.deletes.killErr = errors.New("tmux kill-pane failed")
	observe := f.command.managedPaneLive
	f.command.managedPaneLive = func(tmuxTransport, string) (bool, error) {
		return false, errors.New("tmux list-panes: lost server")
	}
	beforeAnnotations := f.agent(t).Metadata.Annotations

	_, stderr, err := runRoute(t, f.command, "persona", "attach", "uid:"+personaAttachAgent, "go-reviewer")
	if err == nil || !strings.Contains(err.Error(), "persona annotations were restored") || !strings.Contains(err.Error(), "tmux kill-pane failed") {
		t.Fatalf("attach with a failing stop and unobservable liveness err = %v", err)
	}
	after := f.agent(t)
	if after.Status.Phase != coremetadata.PhaseRunning || after.Status.PaneRef != personaAttachPane {
		t.Fatalf("agent = %s on %q, want Running on %s", after.Status.Phase, after.Status.PaneRef, personaAttachPane)
	}
	if !mapsEqual(after.Metadata.Annotations, beforeAnnotations) {
		t.Fatalf("annotations = %v, want the original %v", after.Metadata.Annotations, beforeAnnotations)
	}
	if !strings.Contains(stderr, "could not observe whether agent/codex's managed pane "+personaAttachPane+" is still alive (tmux list-panes: lost server)") ||
		!strings.Contains(stderr, "re-running the same command recovers: "+personaAttachRerun+"\n") {
		t.Fatalf("stderr = %q, want the observation error and the re-run command %q", stderr, personaAttachRerun)
	}
	if calls := splitWindowCalls(f.tmux); len(calls) != 0 {
		t.Fatalf("a failed stop launched %v", calls)
	}

	f.deletes.killErr = nil
	f.command.managedPaneLive = observe
	rerun := strings.Fields(strings.TrimPrefix(personaAttachRerun, "projmux agent "))
	if _, stderr, err := runRoute(t, f.command, rerun...); err != nil {
		t.Fatalf("re-run command: stderr=%q err=%v", stderr, err)
	}
	f.assertRestartedOnTheSameConversation(t, personaAttachPane)
	f.assertNewPersonaAnnotations(t)
}

// TestAgentPersonaDetachStopErrorWithUnobservablePaneLivenessRestoresThePersona
// is the detach half: the persona the running session has comes back, and
// the re-run command is a detach with no persona argument.
func TestAgentPersonaDetachStopErrorWithUnobservablePaneLivenessRestoresThePersona(t *testing.T) {
	f := newPersonaAttachFixture(t)
	f.writePersona(t, "go-reviewer", personaResumeContent)
	if _, _, err := runRoute(t, f.command, "persona", "attach", "uid:"+personaAttachAgent, "go-reviewer"); err != nil {
		t.Fatal(err)
	}
	attached := f.agent(t)
	f.setInteraction(coremetadata.InteractionIdle)
	f.tmux.calls = nil
	f.deletes.killErr = errors.New("tmux kill-pane failed")
	f.command.managedPaneLive = func(tmuxTransport, string) (bool, error) {
		return false, errors.New("tmux list-panes: lost server")
	}

	_, stderr, err := runRoute(t, f.command, "persona", "detach", "uid:"+personaAttachAgent)
	if err == nil || !strings.Contains(err.Error(), "persona annotations were restored") {
		t.Fatalf("detach with a failing stop and unobservable liveness err = %v", err)
	}
	after := f.agent(t)
	if after.Status.Phase != coremetadata.PhaseRunning || after.Status.PaneRef != attached.Status.PaneRef {
		t.Fatalf("agent = %s on %q, want Running on %s", after.Status.Phase, after.Status.PaneRef, attached.Status.PaneRef)
	}
	if !mapsEqual(after.Metadata.Annotations, attached.Metadata.Annotations) {
		t.Fatalf("annotations = %v, want the attached persona restored %v", after.Metadata.Annotations, attached.Metadata.Annotations)
	}
	const rerun = "projmux agent persona detach uid:agt-alpha-codex --project uid:prj-alpha --window uid:win-alpha-main"
	if !strings.Contains(stderr, "re-running the same command recovers: "+rerun+"\n") {
		t.Fatalf("stderr = %q, want the detach re-run command %q", stderr, rerun)
	}
	if calls := splitWindowCalls(f.tmux); len(calls) != 0 {
		t.Fatalf("a failed stop launched %v", calls)
	}
}

// TestManagedPaneMirrorLiveReadsTheExactDeleteInventory pins the production
// liveness answer to the inventory `delete pane` plans from: a Pane uid
// mirrored on the exact server is alive, an unmirrored one is not, and an
// empty or failed inventory is an error rather than absence.
func TestManagedPaneMirrorLiveReadsTheExactDeleteInventory(t *testing.T) {
	runtime, _, _ := newPaneRuntimeFixture(t, paneRuntimeInventory())
	if alive, err := managedPaneMirrorLive(context.Background(), runtime, personaAttachPane); err != nil || !alive {
		t.Fatalf("mirrored pane liveness = %t, %v; want alive", alive, err)
	}
	withoutAgentPane := strings.ReplaceAll(paneRuntimeInventory(),
		livePaneInventoryRow("$1", "alpha", "@10", "%32", "prj-alpha", "win-alpha-main", "pan-alpha-codex"), "")
	runtime, _, _ = newPaneRuntimeFixture(t, withoutAgentPane)
	if alive, err := managedPaneMirrorLive(context.Background(), runtime, personaAttachPane); err != nil || alive {
		t.Fatalf("unmirrored pane liveness = %t, %v; want absent", alive, err)
	}
	runtime, _, _ = newPaneRuntimeFixture(t, "")
	if _, err := managedPaneMirrorLive(context.Background(), runtime, personaAttachPane); err == nil || !strings.Contains(err.Error(), "was empty") {
		t.Fatalf("empty inventory err = %v, want an error", err)
	}
	runtime, runner, _ := newPaneRuntimeFixture(t, paneRuntimeInventory())
	format := tmuxRowFormat("#{session_id}", "#{session_name}", "#{window_id}", "#{pane_id}",
		"#{@projmux_project_uid}", "#{@projmux_window_uid}", "#{@projmux_pane_uid}")
	runner.errors = map[string]error{
		recordedTmuxCallKey("tmux", "-S", testDeleteTarget.Value, "list-panes", "-a", "-F", format): errors.New("lost server"),
	}
	if _, err := managedPaneMirrorLive(context.Background(), runtime, personaAttachPane); err == nil || !strings.Contains(err.Error(), "lost server") {
		t.Fatalf("failed inventory err = %v, want the tmux error", err)
	}
}
