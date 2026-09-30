package app

import (
	"maps"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
)

// TestAReplyOnlyAgentRefusesWhatItsFixedLaunchCannotCarry is acceptance 3:
// on an Agent that records the reply-only activation, Running or Offline,
// every request to launch it with a profile, instructions, a model, an effort,
// or a reset is refused with its reason, and nothing changes -- no Registry
// transaction, no snapshot, no Pane closed, no provider launched.
func TestAReplyOnlyAgentRefusesWhatItsFixedLaunchCannotCarry(t *testing.T) {
	for _, test := range []struct {
		name   string
		args   []string
		reason string
	}{
		{"relaunch --profile", []string{"relaunch", "uid:" + personaAttachAgent, "--profile", "lead"}, profileReasonLaneUnsupported},
		{"relaunch --profile none", []string{"relaunch", "uid:" + personaAttachAgent, "--profile", "none"}, profileReasonLaneUnsupported},
		{"relaunch --instructions", []string{"relaunch", "uid:" + personaAttachAgent, "--instructions", "go-reviewer"}, persona.ReasonProviderUnsupported},
		{"relaunch --instructions none", []string{"relaunch", "uid:" + personaAttachAgent, "--instructions", "none"}, persona.ReasonProviderUnsupported},
		{"relaunch --reset instructions", []string{"relaunch", "uid:" + personaAttachAgent, "--reset", "instructions"}, persona.ReasonProviderUnsupported},
		{"relaunch --model", []string{"relaunch", "uid:" + personaAttachAgent, "--model", "opus"}, replyOnlyReasonLaunchFixed},
		{"relaunch --effort", []string{"relaunch", "uid:" + personaAttachAgent, "--effort", "max"}, replyOnlyReasonLaunchFixed},
		{"relaunch --reset all", []string{"relaunch", "uid:" + personaAttachAgent, "--reset", "all"}, persona.ReasonProviderUnsupported},
		{"relaunch --reset effort", []string{"relaunch", "uid:" + personaAttachAgent, "--reset", "effort"}, replyOnlyReasonLaunchFixed},
		{"relaunch --dry-run --model", []string{"relaunch", "uid:" + personaAttachAgent, "--model", "opus", "--dry-run"}, replyOnlyReasonLaunchFixed},
		{"instructions attach", []string{"instructions", "attach", "uid:" + personaAttachAgent, "go-reviewer"}, persona.ReasonProviderUnsupported},
		{"instructions detach", []string{"instructions", "detach", "uid:" + personaAttachAgent}, persona.ReasonProviderUnsupported},
		{"persona attach", []string{"persona", "attach", "uid:" + personaAttachAgent, "go-reviewer"}, persona.ReasonProviderUnsupported},
	} {
		for _, offline := range []bool{false, true} {
			name := test.name + " running"
			if offline {
				name = test.name + " offline"
			}
			t.Run(name, func(t *testing.T) {
				f := newPersonaAttachFixture(t)
				f.setAnnotations(map[string]string{coremetadata.AnnotationAgentDialogueReplyOnly: coremetadata.DialogueReplyOnlyOn})
				f.writePersona(t, "go-reviewer", "Review Go code.\n")
				if offline {
					stopFixtureAgent(t, f)
				}
				before, beforeAnnotations := f.store.snapshot(), maps.Clone(f.agent(t).Metadata.Annotations)
				stdout, _, err := runRoute(t, f.command, test.args...)
				if err == nil || !IsUsageError(err) {
					t.Fatalf("%v on a reply-only Agent = stdout %q err %v, want a usage refusal", test.args, stdout, err)
				}
				if !strings.Contains(err.Error(), "records the reply-only activation (--dialogue-reply-only)") ||
					!strings.HasSuffix(err.Error(), "("+test.reason+"); nothing was changed") {
					t.Fatalf("refusal = %q, want the reply-only refusal with reason %s", err, test.reason)
				}
				if stdout != "" {
					t.Fatalf("a refused run printed %q", stdout)
				}
				assertRelaunchNothingChanged(t, f, before, beforeAnnotations)
				if len(f.launcher.argv) != 0 {
					t.Fatalf("a refused run planned a resume: %q", f.launcher.argv)
				}
			})
		}
	}
}

// TestAgentResumeOfAReplyOnlyAgentRefusesAModelOrAnEffort is acceptance 3 for
// `agent resume`, which the reply-only record reaches without the flag.
func TestAgentResumeOfAReplyOnlyAgentRefusesAModelOrAnEffort(t *testing.T) {
	for _, flags := range [][]string{{"--model", "opus"}, {"--effort", "low"}} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			f := newPersonaAttachFixture(t)
			f.setAnnotations(map[string]string{coremetadata.AnnotationAgentDialogueReplyOnly: coremetadata.DialogueReplyOnlyOn})
			stopFixtureAgent(t, f)
			before, beforeAnnotations := f.store.snapshot(), maps.Clone(f.agent(t).Metadata.Annotations)
			stdout, _, err := runRoute(t, f.command, append([]string{"resume", "uid:" + personaAttachAgent}, flags...)...)
			want := "agent resume: agent/codex records the reply-only activation (--dialogue-reply-only), whose fixed launch cannot carry a model, an effort, or a reset; create a new Agent to run with it (" +
				replyOnlyReasonLaunchFixed + "); nothing was changed"
			if err == nil || !IsUsageError(err) || err.Error() != want {
				t.Fatalf("resume %v = %v, want usage error %q", flags, err, want)
			}
			if stdout != "" || f.store.transactions != 0 {
				t.Fatalf("a refused resume acted: stdout=%q transactions=%d", stdout, f.store.transactions)
			}
			assertRelaunchNothingChanged(t, f, before, beforeAnnotations)
		})
	}
}

// TestAnAgentWithoutTheReplyOnlyRecordIsNeverRefusedForIt is acceptance 4 at
// the seam: without the record no request is a reply-only refusal, and the
// record needs its exact value.
func TestAnAgentWithoutTheReplyOnlyRecordIsNeverRefusedForIt(t *testing.T) {
	t.Parallel()
	none := ""
	request := agentSettingsRequest{model: "opus", effort: "max", profile: &none, instructions: &none, reset: []string{"all"}}
	for _, annotations := range []map[string]string{
		nil,
		{coremetadata.AnnotationAgentEffort: "high"},
		{coremetadata.AnnotationAgentDialogueReplyOnly: "true"},
	} {
		if refusal := replyOnlyRefusalOf(annotations, request); refusal != (replyOnlyRefusal{}) {
			t.Fatalf("annotations %v refused %+v", annotations, refusal)
		}
	}
	if refusal := replyOnlyRefusalOf(map[string]string{coremetadata.AnnotationAgentDialogueReplyOnly: coremetadata.DialogueReplyOnlyOn}, agentSettingsRequest{}); refusal != (replyOnlyRefusal{}) {
		t.Fatalf("a plain request on a reply-only Agent refused %+v", refusal)
	}
}

// TestTheRestartOfARunningReplyOnlyAgentResumesItReplyOnly drives the restart
// seam `agent relaunch` and `agent instructions attach|detach` share through a
// stop and a resume, the way it runs whenever a Running Agent is restarted:
// the Agent the stop leaves without a Pane resumes into the reply-only
// activation it records, never into an ordinary resume.
func TestTheRestartOfARunningReplyOnlyAgentResumesItReplyOnly(t *testing.T) {
	f := newPersonaAttachFixture(t)
	f.setAnnotations(map[string]string{coremetadata.AnnotationAgentDialogueReplyOnly: coremetadata.DialogueReplyOnlyOn})
	dialogue := &replyOnlyResumeLauncher{exactArgvResumeLauncher: f.launcher}
	f.command.rebind.launcher = dialogue
	target := f.agent(t)
	refuse := func(reason, detail string) error { return usageError(detail + " (" + reason + ")") }
	restart := f.command.newAgentRestart(agentRelaunchSpelling, f.store.registry.Clone(), target, aiModeClaude, relaunchTokens, refuse)
	if err := restart.checkTarget(); err != nil {
		t.Fatal(err)
	}
	if err := f.command.plan(restart, agentSettingsRequest{}, deleteSocketFlags{}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	newPane, err := f.command.run(restart, agentRestartSteps{
		yes:          true,
		stopFailed:   func(err error, _ personaPaneLiveness, _ error) error { return err },
		resumeFailed: func(err error) error { return err },
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("restart: %v (stdout=%q stderr=%q)", err, stdout.String(), stderr.String())
	}
	after := f.assertRestartedOnTheSameConversationBy(t, personaAttachPane, dialogue.argv)
	if after.Status.PaneRef != newPane || !coremetadata.RecordsDialogueReplyOnly(after.Metadata.Annotations) {
		t.Fatalf("restarted Agent pane %q annotations %v, want pane %q and the reply-only record", after.Status.PaneRef, after.Metadata.Annotations, newPane)
	}
	if len(dialogue.conversations) != 1 || dialogue.conversations[0] != personaResumeConversation || len(f.launcher.argv) != 0 {
		t.Fatalf("restart launches: reply-only %q, ordinary %q; want one reply-only launch", dialogue.conversations, f.launcher.argv)
	}
	if call := strings.Join(splitWindowCalls(f.tmux)[0], " "); !strings.Contains(call, "--"+claudeDialogueReplyOnlyFlag+" --") {
		t.Fatalf("restart launched %q, want the supervised reply-only activation", call)
	}
}

// replyOnlyResumeLauncher is the persona attach fixture's resume launcher
// with the reply-only lane: it records every reply-only launch it plans,
// beside the ordinary resumes the embedded launcher records.
type replyOnlyResumeLauncher struct {
	*exactArgvResumeLauncher
	conversations []string
	argv          [][]string
}

// PlanAgentGuidance plans the default agent guidance, as the production
// launcher does, so a relaunch that compares the guidance an Agent recorded
// finds that a reply-only Agent recorded none.
func (l *replyOnlyResumeLauncher) PlanAgentGuidance(provider string, recorded map[string]string) agentGuidanceLaunch {
	return l.planner.PlanAgentGuidance(provider, recorded)
}

func (l *replyOnlyResumeLauncher) PlanClaudeDialogueLaunch(_ coremetadata.AgentWorkspace, conversation string) (string, []string, error) {
	argv := []string{"claude-reply-only", "--resume", conversation}
	l.conversations = append(l.conversations, conversation)
	l.argv = append(l.argv, argv)
	return "claude:reply-only", argv, nil
}

// assertRestartedOnTheSameConversationBy is assertRestartedOnTheSameConversation
// for a launch the fixture's ordinary resume launcher did not plan: the last
// of planned is the child argv the split carried.
func (f *personaAttachFixture) assertRestartedOnTheSameConversationBy(t *testing.T, oldPane string, planned [][]string) coremetadata.Agent {
	t.Helper()
	after := f.agent(t)
	if after.Metadata.UID != personaAttachAgent || after.Status.Phase != coremetadata.PhaseRunning || after.Status.PaneRef == "" || after.Status.PaneRef == oldPane {
		t.Fatalf("agent after restart = uid %s phase %s pane %q, want the same Agent Running on a new Pane", after.Metadata.UID, after.Status.Phase, after.Status.PaneRef)
	}
	if !after.Status.SessionRef.SameConversation(claudeConversationRef(personaResumeConversation)) {
		t.Fatalf("session ref changed: %+v", after.Status.SessionRef)
	}
	if len(f.deletes.killed) != 1 || f.deletes.killed[0].PaneUID != oldPane {
		t.Fatalf("delete pane killed %+v, want exactly the old managed pane %s", f.deletes.killed, oldPane)
	}
	calls := splitWindowCalls(f.tmux)
	if len(calls) != 1 || len(planned) == 0 {
		t.Fatalf("split-window calls = %v, planned = %v; want one launch", calls, planned)
	}
	if tail := strings.Join(calls[0], " "); !strings.HasSuffix(tail, "-- "+strings.Join(planned[len(planned)-1], " ")) {
		t.Fatalf("launched child argv = %q, want the planned %q", calls[0], planned[len(planned)-1])
	}
	return after
}
