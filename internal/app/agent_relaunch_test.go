package app

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// relaunchAgentRef is the exact reference every printed relaunch command
// names the fixture Agent by.
const relaunchAgentRef = "uid:agt-alpha-codex --project uid:prj-alpha --window uid:win-alpha-main"

// newRelaunchFixture is the persona attach fixture, whose Running Claude Agent
// records effort high.
func newRelaunchFixture(t *testing.T) *personaAttachFixture {
	t.Helper()
	f := newPersonaAttachFixture(t)
	f.setAnnotations(effortAnnotations("high"))
	return f
}

// stopFixtureAgent makes the fixture Agent Offline through the real `delete
// pane`, then forgets that stop.
func stopFixtureAgent(t *testing.T, f *personaAttachFixture) {
	t.Helper()
	if _, _, err := runRoute(t, f.command.paneDelete, "pane", "uid:"+personaAttachPane, "--yes"); err != nil {
		t.Fatal(err)
	}
	if f.agent(t).Status.Phase != coremetadata.PhaseOffline {
		t.Fatal("fixture Agent is not Offline")
	}
	f.deletes.killed = nil
	f.deletes.preflights = 0
	f.store.writes = 0
	f.store.transactions = 0
}

// failingRelaunchResumeLauncher fails every resume launch construction, with
// or without a model, the way a missing provider binary does.
type failingRelaunchResumeLauncher struct {
	*exactArgvResumeLauncher
}

func (l *failingRelaunchResumeLauncher) PlanAgentResume(string, coremetadata.AgentWorkspace, string, map[string]string) (agentResumeLaunch, error) {
	return agentResumeLaunch{}, errors.New("claude binary is not installed")
}

func (l *failingRelaunchResumeLauncher) PlanAgentResumeWithModel(string, coremetadata.AgentWorkspace, string, map[string]string, string) (agentResumeLaunch, error) {
	return agentResumeLaunch{}, errors.New("claude binary is not installed")
}

// assertRelaunchNothingChanged is assertNothingChanged plus no Registry
// transaction at all.
func assertRelaunchNothingChanged(t *testing.T, f *personaAttachFixture, before string, beforeAnnotations map[string]string) {
	t.Helper()
	f.assertNothingChanged(t, before, beforeAnnotations)
	if f.store.transactions != 0 {
		t.Fatalf("a refused relaunch opened %d transactions", f.store.transactions)
	}
}

func TestAgentRelaunchRestartsAnIdleRunningClaudeAgentWithTheModelAndEffortOnTheSameConversation(t *testing.T) {
	f := newRelaunchFixture(t)
	agentCount := len(f.store.registry.Agents)

	stdout, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--model", "opus", "--effort", "max")
	if err != nil {
		t.Fatalf("relaunch: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	// Text mode forwards the `delete pane` and `agent resume` lines, as
	// `agent persona` does, and ends with the relaunch line.
	if want := "agent/codex relaunched from effort=high to effort=max model=opus; restarted on the same conversation\n"; !strings.HasSuffix(stdout, "\n"+want) {
		t.Fatalf("relaunch stdout = %q, want it to end with %q", stdout, want)
	}
	if len(f.store.registry.Agents) != agentCount {
		t.Fatalf("relaunch changed the Agent count to %d", len(f.store.registry.Agents))
	}
	after := f.assertRestartedOnTheSameConversation(t, personaAttachPane)
	want := []string{"--model", "opus", "--effort", "max", "--resume", personaResumeConversation}
	if got := f.lastArgvTail(t); !slices.Equal(got, want) {
		t.Fatalf("relaunch exec argv tail = %q, want %q", got, want)
	}
	if !maps.Equal(after.Metadata.Annotations, plusSources(modelEffortAnnotations("opus", "max"), modelEffortRelaunch...)) {
		t.Fatalf("annotations = %v, want only the new model and effort", after.Metadata.Annotations)
	}
}

func TestAgentRelaunchWithOnlyAModelKeepsTheRecordedEffort(t *testing.T) {
	f := newRelaunchFixture(t)
	stdout, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--model", "sonnet")
	if err != nil {
		t.Fatalf("relaunch: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	after := f.assertRestartedOnTheSameConversation(t, personaAttachPane)
	want := []string{"--model", "sonnet", "--effort", "high", "--resume", personaResumeConversation}
	if got := f.lastArgvTail(t); !slices.Equal(got, want) {
		t.Fatalf("relaunch exec argv tail = %q, want %q", got, want)
	}
	if !maps.Equal(after.Metadata.Annotations, plusSources(modelEffortAnnotations("sonnet", "high"), coremetadata.AnnotationAgentModelSource, coremetadata.SettingSourceRelaunch)) {
		t.Fatalf("annotations = %v, want the new model and the recorded effort kept", after.Metadata.Annotations)
	}
}

func TestAgentRelaunchWithOnlyAnEffortKeepsTheRecordedModelAndDoesNotPassIt(t *testing.T) {
	f := newRelaunchFixture(t)
	f.setAnnotations(modelEffortAnnotations("haiku", "high"))
	stdout, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--effort", "max")
	if err != nil {
		t.Fatalf("relaunch: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	after := f.assertRestartedOnTheSameConversation(t, personaAttachPane)
	want := []string{"--effort", "max", "--resume", personaResumeConversation}
	if got := f.lastArgvTail(t); !slices.Equal(got, want) {
		t.Fatalf("relaunch exec argv tail = %q, want %q", got, want)
	}
	if !maps.Equal(after.Metadata.Annotations, plusSources(modelEffortAnnotations("haiku", "max"), coremetadata.AnnotationAgentEffortSource, coremetadata.SettingSourceRelaunch)) {
		t.Fatalf("annotations = %v, want the recorded model kept", after.Metadata.Annotations)
	}
}

func TestAgentRelaunchOfAnOfflineOrFailedAgentResumesWithoutAStop(t *testing.T) {
	for _, phase := range []coremetadata.AgentPhase{coremetadata.PhaseOffline, coremetadata.PhaseFailed} {
		t.Run(string(phase), func(t *testing.T) {
			f := newRelaunchFixture(t)
			stopFixtureAgent(t, f)
			agent, _ := f.store.registry.Agent(personaAttachAgent)
			agent.Status.Phase = phase

			stdout, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--effort", "high")
			if err != nil {
				t.Fatalf("relaunch %s: stdout=%q stderr=%q err=%v", phase, stdout, stderr, err)
			}
			if !strings.HasSuffix(stdout, "; resumed on the same conversation\n") {
				t.Fatalf("relaunch %s stdout = %q", phase, stdout)
			}
			if len(f.deletes.killed) != 0 || f.deletes.preflights != 0 {
				t.Fatalf("relaunch of a %s Agent reached delete pane: %+v", phase, f.deletes.killed)
			}
			after := f.agent(t)
			if after.Status.Phase != coremetadata.PhaseRunning || !after.Status.SessionRef.SameConversation(claudeConversationRef(personaResumeConversation)) {
				t.Fatalf("relaunched %s Agent = %s %+v", phase, after.Status.Phase, after.Status.SessionRef)
			}
			want := []string{"--effort", "high", "--resume", personaResumeConversation}
			if got := f.lastArgvTail(t); !slices.Equal(got, want) {
				t.Fatalf("relaunch exec argv tail = %q, want %q", got, want)
			}
		})
	}
}

func TestAgentRelaunchOfACodexAgentPassesTheCodexModelAndEffortFlags(t *testing.T) {
	f := newRelaunchFixture(t)
	route := nativeTestRoute("generation-relaunch", coremetadata.CodexGenerationCurrent)
	ref := nativeTestSessionRef(route, resumeFixtureConversation)
	ref.ObservedAt = resourceFixtureClock
	agent, _ := f.store.registry.Agent(personaAttachAgent)
	agent.Spec.Provider = aiModeCodex
	agent.Status.SessionRef = ref
	panes := &fakeNativePaneLauncher{}
	f.command.rebind.launcher = &fakeNativeResumeLauncher{fakeResumeLauncher: f.launcher.fakeResumeLauncher, fakeNativePaneLauncher: panes}
	f.command.rebind.create.codexNative = &fakeNativeThreadController{resolvedRoute: route, resumeBinding: codexappserver.ThreadBinding{ThreadID: resumeFixtureConversation}}

	stdout, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--model", "gpt-6", "--effort", "xhigh", "-o", "json")
	if err != nil {
		t.Fatalf("relaunch: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if len(f.deletes.killed) != 1 || f.deletes.killed[0].PaneUID != personaAttachPane {
		t.Fatalf("delete pane killed %+v, want the managed pane %s", f.deletes.killed, personaAttachPane)
	}
	for i, plan := range panes.plans {
		if plan.model != "gpt-6" || plan.effort != "xhigh" {
			t.Fatalf("native pane plan %d = %+v, want model gpt-6 effort xhigh", i, plan)
		}
	}
	calls := splitWindowCalls(f.tmux)
	if len(calls) != 1 || !strings.Contains(strings.Join(calls[0], " "), "-m gpt-6 -c model_reasoning_effort=xhigh resume") {
		t.Fatalf("split-window calls = %v, want one launch with -m gpt-6 -c model_reasoning_effort=xhigh", calls)
	}
	after := f.agent(t)
	if after.Metadata.UID != personaAttachAgent || after.Status.Phase != coremetadata.PhaseRunning || after.Status.PaneRef == personaAttachPane ||
		after.Status.SessionRef.ConversationID() != resumeFixtureConversation {
		t.Fatalf("codex Agent after relaunch = %s on %q %+v", after.Status.Phase, after.Status.PaneRef, after.Status.SessionRef)
	}
	if !maps.Equal(after.Metadata.Annotations, plusSources(modelEffortAnnotations("gpt-6", "xhigh"), modelEffortRelaunch...)) {
		t.Fatalf("annotations = %v, want only the new model and effort", after.Metadata.Annotations)
	}
	if !strings.Contains(stdout, `"outcome":"restarted"`) || !strings.Contains(stdout, `"provider":"codex"`) {
		t.Fatalf("codex relaunch JSON = %s", stdout)
	}
}

func TestAgentRelaunchRefusalsCarryTheirReasonAndLeaveNoTrace(t *testing.T) {
	for _, test := range []struct {
		name    string
		flags   []string
		arrange func(*personaAttachFixture)
		want    string
	}{
		{name: "neither model nor effort", want: "agent relaunch requires --model, --effort, or both; nothing was changed"},
		{name: "bad model", flags: []string{"--model", "a b"}, want: `agent relaunch --model "a b" is not a model name; nothing was changed`},
		{name: "bad effort", flags: []string{"--effort", "turbo"}, want: "agent relaunch --effort must be one of: low, medium, high, xhigh, max; nothing was changed"},
		{name: "antigravity agent", flags: []string{"--effort", "max"}, want: relaunchReasonProviderUnsupported, arrange: func(f *personaAttachFixture) {
			agent, _ := f.store.registry.Agent(personaAttachAgent)
			agent.Spec.Provider = aiModeAntigravity
		}},
		{name: "codex thread without a durable endpoint", flags: []string{"--effort", "max"}, want: relaunchReasonNoConversation, arrange: func(f *personaAttachFixture) {
			agent, _ := f.store.registry.Agent(personaAttachAgent)
			agent.Spec.Provider = aiModeCodex
			agent.Status.SessionRef = codexConversationRef(resumeFixtureConversation)
		}},
		{name: "no conversation", flags: []string{"--effort", "max"}, want: relaunchReasonNoConversation, arrange: func(f *personaAttachFixture) {
			agent, _ := f.store.registry.Agent(personaAttachAgent)
			agent.Status.SessionRef = nil
		}},
		{name: "pending phase", flags: []string{"--effort", "max"}, want: relaunchReasonNoConversation, arrange: func(f *personaAttachFixture) {
			agent, _ := f.store.registry.Agent(personaAttachAgent)
			agent.Status.Phase = coremetadata.PhasePending
		}},
		{name: "running without its managed pane", flags: []string{"--effort", "max"}, want: relaunchReasonNoConversation, arrange: func(f *personaAttachFixture) {
			agent, _ := f.store.registry.Agent(personaAttachAgent)
			agent.Status.PaneRef = "pan-absent"
		}},
		{name: "busy without yes", flags: []string{"--effort", "max"}, want: relaunchReasonAgentBusy, arrange: func(f *personaAttachFixture) {
			f.setInteraction(coremetadata.InteractionInProgress)
		}},
		{name: "unknown interaction without yes", flags: []string{"--model", "opus"}, want: relaunchReasonAgentBusy, arrange: func(f *personaAttachFixture) {
			f.setInteraction(coremetadata.InteractionUnknown)
		}},
		{name: "self target", flags: []string{"--effort", "max", "--yes"}, want: relaunchReasonSelfTarget, arrange: func(f *personaAttachFixture) {
			pane, _ := f.store.registry.Pane(personaAttachPane)
			pane.Status.Activation.RuntimeID = "%77"
			f.env["TMUX_PANE"] = "%77"
		}},
		{name: "outside tmux without a socket", flags: []string{"--effort", "max"}, want: "requires --socket <name> or --socket-path <absolute> outside tmux", arrange: func(f *personaAttachFixture) {
			delete(f.env, "TMUX")
			f.delete.lookupEnv = func(string) string { return "" }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newRelaunchFixture(t)
			if test.arrange != nil {
				test.arrange(f)
			}
			before, beforeAnnotations := f.store.snapshot(), f.agent(t).Metadata.Annotations
			args := append([]string{"relaunch", "uid:" + personaAttachAgent}, test.flags...)
			stdout, _, err := runRoute(t, f.command, args...)
			if err == nil || !IsUsageError(err) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("relaunch %v err = %v, want a usage refusal carrying %q", test.flags, err, test.want)
			}
			if !strings.HasSuffix(err.Error(), "nothing was changed") && !strings.Contains(test.want, "outside tmux") {
				t.Fatalf("refusal %q does not end with `nothing was changed`", err)
			}
			if stdout != "" {
				t.Fatalf("refused relaunch printed %q", stdout)
			}
			assertRelaunchNothingChanged(t, f, before, beforeAnnotations)
		})
	}
}

func TestAgentRelaunchOfABusyAgentWithYesRestartsIt(t *testing.T) {
	f := newRelaunchFixture(t)
	f.setInteraction(coremetadata.InteractionInProgress)
	if _, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--effort", "max", "--yes"); err != nil {
		t.Fatalf("relaunch --yes: stderr=%q err=%v", stderr, err)
	}
	f.assertRestartedOnTheSameConversation(t, personaAttachPane)
}

func TestAgentRelaunchOutsideTmuxStopsThroughTheNamedSocket(t *testing.T) {
	f := newRelaunchFixture(t)
	delete(f.env, "TMUX")
	f.delete.lookupEnv = func(string) string { return "" }
	if _, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--effort", "max", "--socket", "isolated"); err != nil {
		t.Fatalf("relaunch --socket outside tmux: stderr=%q err=%v", stderr, err)
	}
	if f.deletes.boundTarget.Kind != tmuxSocketName || f.deletes.boundTarget.Value != "isolated" {
		t.Fatalf("delete pane routed to %+v, want socket name isolated", f.deletes.boundTarget)
	}
	f.assertRestartedOnTheSameConversation(t, personaAttachPane)
}

func TestAgentRelaunchWithTheRecordedEffortOfARunningAgentIsUnchanged(t *testing.T) {
	f := newRelaunchFixture(t)
	f.setInteraction(coremetadata.InteractionInProgress)
	before, beforeAnnotations := f.store.snapshot(), f.agent(t).Metadata.Annotations

	stdout, _, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--effort", "high", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"action":"relaunch","dryRun":false,"outcome":"unchanged","agentUID":"agt-alpha-codex","agentName":"codex","provider":"claude","phase":"Running","interaction":"in_progress","paneUID":"pan-alpha-codex","newPaneUID":"pan-alpha-codex","currentEffort":"high","newEffort":"high","restart":false,"confirmationRequired":false,"unchanged":true}` + "\n"
	if stdout != want {
		t.Fatalf("unchanged relaunch JSON =\n%s\nwant\n%s", stdout, want)
	}
	assertRelaunchNothingChanged(t, f, before, beforeAnnotations)

	stdout, _, err = runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--effort", "high")
	if err != nil || stdout != "agent/codex unchanged: already running with effort=high\n" {
		t.Fatalf("unchanged relaunch text = %q, %v", stdout, err)
	}
}

func TestAgentRelaunchWithAModelAlwaysRestartsEvenWithTheRecordedEffort(t *testing.T) {
	f := newRelaunchFixture(t)
	if _, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--model", "opus", "--effort", "high"); err != nil {
		t.Fatalf("relaunch: stderr=%q err=%v", stderr, err)
	}
	f.assertRestartedOnTheSameConversation(t, personaAttachPane)
	want := []string{"--model", "opus", "--effort", "high", "--resume", personaResumeConversation}
	if got := f.lastArgvTail(t); !slices.Equal(got, want) {
		t.Fatalf("relaunch exec argv tail = %q, want %q", got, want)
	}
}

func TestAgentRelaunchOfAnOfflineAgentWithTheRecordedEffortStillResumes(t *testing.T) {
	f := newRelaunchFixture(t)
	stopFixtureAgent(t, f)
	stdout, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--effort", "high", "-o", "json")
	if err != nil {
		t.Fatalf("relaunch: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	after := f.agent(t)
	if after.Status.Phase != coremetadata.PhaseRunning {
		t.Fatal("the Offline Agent did not resume")
	}
	want := `{"action":"relaunch","dryRun":false,"outcome":"resumed","agentUID":"agt-alpha-codex","agentName":"codex","provider":"claude","phase":"Offline","interaction":"unknown","newPaneUID":"` + after.Status.PaneRef + `","currentEffort":"high","newEffort":"high","restart":false,"confirmationRequired":false,"unchanged":false}` + "\n"
	if stdout != want {
		t.Fatalf("resumed relaunch JSON =\n%s\nwant\n%s", stdout, want)
	}
}

func TestAgentRelaunchExecutedJSONOfARunningAgentReportsTheNewPane(t *testing.T) {
	f := newRelaunchFixture(t)
	f.setAnnotations(modelEffortAnnotations("haiku", "high"))
	stdout, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--model", "opus", "--effort", "max", "-o", "json")
	if err != nil {
		t.Fatalf("relaunch: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	after := f.assertRestartedOnTheSameConversation(t, personaAttachPane)
	want := `{"action":"relaunch","dryRun":false,"outcome":"restarted","agentUID":"agt-alpha-codex","agentName":"codex","provider":"claude","phase":"Running","interaction":"idle","paneUID":"pan-alpha-codex","newPaneUID":"` + after.Status.PaneRef + `","currentEffort":"high","currentModel":"haiku","newEffort":"max","newModel":"opus","restart":true,"confirmationRequired":false,"unchanged":false}` + "\n"
	if stdout != want {
		t.Fatalf("restarted relaunch JSON =\n%s\nwant\n%s", stdout, want)
	}
	if !maps.Equal(after.Metadata.Annotations, plusSources(modelEffortAnnotations("opus", "max"), modelEffortRelaunch...)) {
		t.Fatalf("annotations = %v, want the new model and effort", after.Metadata.Annotations)
	}
}

func TestAgentRelaunchDryRunJSONChangesNothing(t *testing.T) {
	t.Run("running", func(t *testing.T) {
		f := newRelaunchFixture(t)
		before, beforeAnnotations := f.store.snapshot(), f.agent(t).Metadata.Annotations
		stdout, _, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--model", "opus", "--effort", "max", "--dry-run", "-o", "json")
		if err != nil {
			t.Fatal(err)
		}
		want := `{"action":"relaunch","dryRun":true,"outcome":"would-restart","agentUID":"agt-alpha-codex","agentName":"codex","provider":"claude","phase":"Running","interaction":"idle","paneUID":"pan-alpha-codex","currentEffort":"high","newEffort":"max","newModel":"opus","restart":true,"confirmationRequired":false,"unchanged":false}` + "\n"
		if stdout != want {
			t.Fatalf("dry run JSON =\n%s\nwant\n%s", stdout, want)
		}
		assertRelaunchNothingChanged(t, f, before, beforeAnnotations)

		stdout, _, err = runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--model", "opus", "--dry-run")
		wantText := "agent relaunch: agent/codex uid=agt-alpha-codex phase=Running interaction=idle from effort=high to effort=high model=opus; would restart it on the same conversation; confirmation-required=false\ndry-run: nothing was changed\n"
		if err != nil || stdout != wantText {
			t.Fatalf("dry run text = %q, %v; want %q", stdout, err, wantText)
		}
		assertRelaunchNothingChanged(t, f, before, beforeAnnotations)
	})
	t.Run("running with a recorded model", func(t *testing.T) {
		f := newRelaunchFixture(t)
		f.setAnnotations(modelEffortAnnotations("haiku", "high"))
		before, beforeAnnotations := f.store.snapshot(), f.agent(t).Metadata.Annotations
		stdout, _, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--model", "haiku", "--dry-run", "-o", "json")
		if err != nil {
			t.Fatal(err)
		}
		// The recorded model is reported, and matching it still restarts.
		want := `{"action":"relaunch","dryRun":true,"outcome":"would-restart","agentUID":"agt-alpha-codex","agentName":"codex","provider":"claude","phase":"Running","interaction":"idle","paneUID":"pan-alpha-codex","currentEffort":"high","currentModel":"haiku","newModel":"haiku","restart":true,"confirmationRequired":false,"unchanged":false}` + "\n"
		if stdout != want {
			t.Fatalf("dry run JSON =\n%s\nwant\n%s", stdout, want)
		}
		assertRelaunchNothingChanged(t, f, before, beforeAnnotations)
	})
	t.Run("busy running", func(t *testing.T) {
		f := newRelaunchFixture(t)
		f.setInteraction(coremetadata.InteractionInProgress)
		before, beforeAnnotations := f.store.snapshot(), f.agent(t).Metadata.Annotations
		stdout, _, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--model", "opus", "--dry-run", "-o", "json")
		if err != nil {
			t.Fatal(err)
		}
		want := `{"action":"relaunch","dryRun":true,"outcome":"would-restart","agentUID":"agt-alpha-codex","agentName":"codex","provider":"claude","phase":"Running","interaction":"in_progress","paneUID":"pan-alpha-codex","currentEffort":"high","newModel":"opus","restart":true,"confirmationRequired":true,"unchanged":false}` + "\n"
		if stdout != want {
			t.Fatalf("busy dry run JSON =\n%s\nwant\n%s", stdout, want)
		}
		assertRelaunchNothingChanged(t, f, before, beforeAnnotations)
	})
	t.Run("offline", func(t *testing.T) {
		f := newRelaunchFixture(t)
		stopFixtureAgent(t, f)
		before, beforeAnnotations := f.store.snapshot(), f.agent(t).Metadata.Annotations
		stdout, _, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--effort", "low", "--dry-run", "-o", "json")
		if err != nil {
			t.Fatal(err)
		}
		want := `{"action":"relaunch","dryRun":true,"outcome":"would-resume","agentUID":"agt-alpha-codex","agentName":"codex","provider":"claude","phase":"Offline","interaction":"unknown","currentEffort":"high","newEffort":"low","restart":false,"confirmationRequired":false,"unchanged":false}` + "\n"
		if stdout != want {
			t.Fatalf("offline dry run JSON =\n%s\nwant\n%s", stdout, want)
		}
		assertRelaunchNothingChanged(t, f, before, beforeAnnotations)
	})
}

func TestAgentRelaunchStopFailureWithTheManagedPaneAliveChangesNothing(t *testing.T) {
	f := newRelaunchFixture(t)
	f.deletes.killErr = errors.New("tmux kill-pane failed")
	beforeAnnotations := f.agent(t).Metadata.Annotations

	_, _, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--model", "opus", "--effort", "max")
	if err == nil || !strings.Contains(err.Error(), "keeps running with its previous launch options") || !strings.Contains(err.Error(), "tmux kill-pane failed") {
		t.Fatalf("relaunch with a failing stop err = %v", err)
	}
	after := f.agent(t)
	if after.Status.Phase != coremetadata.PhaseRunning || after.Status.PaneRef != personaAttachPane {
		t.Fatalf("agent after a failed stop = %s on %q, want Running on %s", after.Status.Phase, after.Status.PaneRef, personaAttachPane)
	}
	if !maps.Equal(after.Metadata.Annotations, beforeAnnotations) {
		t.Fatalf("annotations after a failed stop = %v, want the original %v", after.Metadata.Annotations, beforeAnnotations)
	}
	if calls := splitWindowCalls(f.tmux); len(calls) != 0 {
		t.Fatalf("a failed stop launched %v", calls)
	}
}

func TestAgentRelaunchStopErrorAfterThePaneClosedWarnsAndResumes(t *testing.T) {
	f := newRelaunchFixture(t)
	f.command.paneDelete = failingStopRoute{route: f.delete, closes: true, stopErr: errors.New("write delete result: no space left on device")}

	stdout, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--effort", "max")
	if err != nil {
		t.Fatalf("relaunch after a closed-but-failed stop: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if !strings.Contains(stderr, "projmux: warning: closing agent/codex's managed pane "+personaAttachPane+" reported an error, but that pane is already closed, so the Agent is resumed with the new launch options: write delete result: no space left on device\n") {
		t.Fatalf("stderr = %q, want the closed-pane warning", stderr)
	}
	after := f.assertRestartedOnTheSameConversation(t, personaAttachPane)
	if !maps.Equal(after.Metadata.Annotations, plusSources(effortAnnotations("max"), coremetadata.AnnotationAgentEffortSource, coremetadata.SettingSourceRelaunch)) {
		t.Fatalf("annotations = %v, want the new effort", after.Metadata.Annotations)
	}
}

func TestAgentRelaunchStopErrorWithUnobservablePaneLivenessPrintsTheRerunCommand(t *testing.T) {
	f := newRelaunchFixture(t)
	f.deletes.killErr = errors.New("tmux kill-pane failed")
	observe := f.command.managedPaneLive
	f.command.managedPaneLive = func(tmuxTransport, string) (bool, error) {
		return false, errors.New("tmux list-panes: lost server")
	}
	beforeAnnotations := f.agent(t).Metadata.Annotations

	_, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--model", "opus", "--effort", "max", "--yes")
	if err == nil || !strings.Contains(err.Error(), "could not be observed") || !strings.Contains(err.Error(), "tmux kill-pane failed") {
		t.Fatalf("relaunch with unobservable liveness err = %v", err)
	}
	const rerun = "projmux agent relaunch " + relaunchAgentRef + " --yes --model opus --effort max"
	if !strings.Contains(stderr, "could not observe whether agent/codex's managed pane "+personaAttachPane+" is still alive (tmux list-panes: lost server)") ||
		!strings.Contains(stderr, "re-running the same command recovers: "+rerun+"\n") {
		t.Fatalf("stderr = %q, want the observation error and the re-run command %q", stderr, rerun)
	}
	if !maps.Equal(f.agent(t).Metadata.Annotations, beforeAnnotations) {
		t.Fatalf("annotations = %v, want the original %v", f.agent(t).Metadata.Annotations, beforeAnnotations)
	}
	if calls := splitWindowCalls(f.tmux); len(calls) != 0 {
		t.Fatalf("a failed stop launched %v", calls)
	}

	f.deletes.killErr = nil
	f.command.managedPaneLive = observe
	if _, stderr, err := runRoute(t, f.command, strings.Fields(strings.TrimPrefix(rerun, "projmux agent "))...); err != nil {
		t.Fatalf("re-run command: stderr=%q err=%v", stderr, err)
	}
	f.assertRestartedOnTheSameConversation(t, personaAttachPane)
	if got := f.lastArgvTail(t); !slices.Equal(got, []string{"--model", "opus", "--effort", "max", "--resume", personaResumeConversation}) {
		t.Fatalf("re-run exec argv tail = %q", got)
	}
}

func TestAgentRelaunchResumeFailureLeavesTheAgentOfflineWithItsOldEffortAndARecoveryCommand(t *testing.T) {
	f := newRelaunchFixture(t)
	working := f.command.rebind.launcher
	f.command.rebind.launcher = &failingRelaunchResumeLauncher{exactArgvResumeLauncher: f.launcher}

	_, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--model", "opus", "--effort", "max")
	if err == nil || !strings.Contains(err.Error(), "needs `agent resume`") {
		t.Fatalf("relaunch with a failing resume err = %v", err)
	}
	after := f.agent(t)
	if after.Status.Phase != coremetadata.PhaseOffline {
		t.Fatalf("phase after a failed resume = %s, want Offline", after.Status.Phase)
	}
	if !maps.Equal(after.Metadata.Annotations, effortAnnotations("high")) {
		t.Fatalf("annotations after a failed resume = %v, want the old effort", after.Metadata.Annotations)
	}
	const recovery = "projmux agent resume " + relaunchAgentRef + " --model opus --effort max"
	if !strings.Contains(stderr, "projmux: recover with: "+recovery+"\n") {
		t.Fatalf("stderr = %q, want the recovery command %q", stderr, recovery)
	}
	if calls := splitWindowCalls(f.tmux); len(calls) != 0 {
		t.Fatalf("a failed resume launched %v", calls)
	}

	f.command.rebind.launcher = working
	if _, stderr, err := runRoute(t, f.command, strings.Fields(strings.TrimPrefix(recovery, "projmux agent "))...); err != nil {
		t.Fatalf("recovery command: stderr=%q err=%v", stderr, err)
	}
	if got := f.lastArgvTail(t); !slices.Equal(got, []string{"--model", "opus", "--effort", "max", "--resume", personaResumeConversation}) {
		t.Fatalf("recovery exec argv tail = %q", got)
	}
	if after := f.agent(t); after.Status.Phase != coremetadata.PhaseRunning || !maps.Equal(after.Metadata.Annotations, plusSources(modelEffortAnnotations("opus", "max"), modelEffortResume...)) {
		t.Fatalf("recovered Agent = %s %v", after.Status.Phase, after.Metadata.Annotations)
	}
}

func TestAgentRelaunchRequiresAnExplicitAgent(t *testing.T) {
	f := newRelaunchFixture(t)
	for _, args := range [][]string{
		{"relaunch", "--effort", "max"},
		{"relaunch", "uid:" + personaAttachAgent, "other", "--effort", "max"},
		{"relaunch", "uid:" + personaAttachAgent, "--effort", "max", "-o", "yaml"},
	} {
		if _, _, err := runRoute(t, f.command, args...); err == nil || !IsUsageError(err) {
			t.Fatalf("%q err = %v, want a usage refusal", args, err)
		}
	}
	if f.store.writes != 0 || f.store.transactions != 0 {
		t.Fatalf("usage refusals wrote %d times", f.store.writes)
	}
}
