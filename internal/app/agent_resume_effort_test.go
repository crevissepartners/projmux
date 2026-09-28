package app

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// effortInvalidFixture is a hand-edited effort Claude does not take.
const effortInvalidFixture = "extreme"

// effortAnnotations is the annotation set an Agent created with --effort
// records.
func effortAnnotations(effort string) map[string]string {
	return map[string]string{coremetadata.AnnotationAgentEffort: effort}
}

// wantEffortInvalidNotice is the exact disclosure of a skipped effort.
func wantEffortInvalidNotice(label, effort string) string {
	return "projmux: agent/" + label + " resumed without its effort \"" + effort +
		"\" (effort-invalid): not one of low, medium, high, xhigh, max"
}

// TestCreateClaudeAgentWithEffortRecordsItOnTheAgent pins the create half: the
// same create that launches Claude with --effort records it on the Agent, and
// on the Agent only.
func TestCreateClaudeAgentWithEffortRecordsItOnTheAgent(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	create, launcher := newTestAgentCreateCommand(t, store, newFakeTmux())
	if _, _, err := runRoute(t, create,
		"agent", "--provider", "claude", "--effort", "low", "--model", "sonnet",
		"--project", "alpha", "--window", "review", "--", "review this"); err != nil {
		t.Fatal(err)
	}
	if len(launcher.plans) != 1 || launcher.plans[0].effort != "low" || launcher.plans[0].model != "sonnet" {
		t.Fatalf("plans = %+v, want the first launch with --effort low", launcher.plans)
	}
	agent := agentNamed(t, store, "win-alpha-review", "agent-test-1")
	if want := effortAnnotations("low"); !maps.Equal(agent.Metadata.Annotations, want) {
		t.Fatalf("Agent annotations = %v, want only %v (the model is not recorded)", agent.Metadata.Annotations, want)
	}
	pane, ok := store.registry.Pane(agent.Status.PaneRef)
	if !ok {
		t.Fatalf("Agent pane %q missing", agent.Status.PaneRef)
	}
	if _, found := pane.Metadata.Annotations[coremetadata.AnnotationAgentEffort]; found {
		t.Fatalf("Pane carries %s: %v", coremetadata.AnnotationAgentEffort, pane.Metadata.Annotations)
	}
}

// TestCreateClaudeAgentWithoutEffortStoresWhatItStoredBefore pins that a
// create without --effort, with or without --model, writes no annotation map
// at all: the stored Agent is byte-identical to the one before the effort was
// recorded.
func TestCreateClaudeAgentWithoutEffortStoresWhatItStoredBefore(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][]string{
		"no launch options": {"agent", "--provider", "claude"},
		"model only":        {"agent", "--provider", "claude", "--model", "opus"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			create, _ := newTestAgentCreateCommand(t, store, newFakeTmux())
			argv := append(args, "--project", "alpha", "--window", "review", "--", "review this")
			if _, _, err := runRoute(t, create, argv...); err != nil {
				t.Fatal(err)
			}
			agent := agentNamed(t, store, "win-alpha-review", "agent-test-1")
			if agent.Metadata.Annotations != nil {
				t.Fatalf("Agent annotations = %#v, want nil", agent.Metadata.Annotations)
			}
			raw, err := json.Marshal(agent.Metadata)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "annotations") || strings.Contains(string(raw), "effort") || strings.Contains(string(raw), "opus") {
				t.Fatalf("Agent metadata gained launch-option state: %s", raw)
			}
		})
	}
}

// TestEffortAnnotationMergesWithTheOtherCreateKeys is the merge table of the
// effort helper: no effort returns base itself (nil stays nil), an effort
// alone is one key, and an effort over the creator and persona keys adds one
// key without writing into their map.
func TestEffortAnnotationMergesWithTheOtherCreateKeys(t *testing.T) {
	t.Parallel()
	creator := coremetadata.CreatorAnnotations("agent-creator", "pane-creator")
	withPersona := personaLaunch{name: "reviewer", snapshot: persona.Snapshot{Digest: persona.Digest([]byte("x"))}}
	base := withPersona.withAnnotations(creator)

	if got := withEffortAnnotation("", nil); got != nil {
		t.Fatalf("no base, no effort = %#v, want nil", got)
	}
	if got := withEffortAnnotation("", base); !maps.Equal(got, base) {
		t.Fatalf("no effort = %v, want base %v", got, base)
	}
	if got := withEffortAnnotation("high", nil); !maps.Equal(got, effortAnnotations("high")) {
		t.Fatalf("effort only = %v", got)
	}
	before := maps.Clone(base)
	got := withEffortAnnotation("high", base)
	want := maps.Clone(base)
	want[coremetadata.AnnotationAgentEffort] = "high"
	if !maps.Equal(got, want) {
		t.Fatalf("effort over creator and persona = %v, want %v", got, want)
	}
	if !maps.Equal(base, before) {
		t.Fatalf("adding the effort wrote into base: %v", base)
	}
}

// TestResumeSeamRepassesOnlyAValidClaudeEffort is the seam table: recorded
// effort {valid, invalid, absent} x provider {claude, codex, antigravity}.
// Only a Claude Agent with a valid effort gets --effort, ahead of the variadic
// --add-dir; an invalid one resumes without it and discloses effort-invalid;
// every other cell is byte-identical to the unannotated resume and discloses
// nothing.
func TestResumeSeamRepassesOnlyAValidClaudeEffort(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	conversations := map[string]string{
		aiModeClaude:      personaResumeConversation,
		aiModeCodex:       resumeFixtureConversation,
		aiModeAntigravity: personaResumeConversation,
	}

	for _, provider := range []string{aiModeClaude, aiModeCodex, aiModeAntigravity} {
		// Antigravity takes no additional writable roots.
		workspace := coremetadata.AgentWorkspace{CWD: "/work/owner"}
		if provider != aiModeAntigravity {
			workspace.AdditionalWritableRoots = []string{"/work/extra"}
		}
		plain, err := planner.PlanAgentResume(provider, workspace, conversations[provider], nil)
		if err != nil {
			t.Fatalf("%s resume without annotations: %v", provider, err)
		}
		if notice := plain.effortNotice("reviewer"); notice != "" {
			t.Fatalf("%s resume without annotations disclosed %q", provider, notice)
		}
		for _, test := range []struct {
			name        string
			annotations map[string]string
		}{
			{"valid", effortAnnotations("low")},
			{"invalid", effortAnnotations(effortInvalidFixture)},
			{"absent", map[string]string{coremetadata.AnnotationAgentTopic: "unrelated"}},
		} {
			t.Run(provider+"/"+test.name, func(t *testing.T) {
				launch, err := planner.PlanAgentResume(provider, workspace, conversations[provider], test.annotations)
				if err != nil {
					t.Fatalf("resume failed: %v", err)
				}
				notice := launch.effortNotice("reviewer")
				switch {
				case provider == aiModeClaude && test.name == "valid":
					want := []string{"--effort", "low", "--add-dir", "/work/extra", "--resume", personaResumeConversation}
					if got := execArgvTail(t, launch.argv, provider); !slices.Equal(got, want) {
						t.Fatalf("exec argv tail = %q, want %q", got, want)
					}
					if notice != "" {
						t.Fatalf("a valid effort disclosed %q", notice)
					}
				case provider == aiModeCodex && test.name == "valid":
					want := []string{"-c", "model_reasoning_effort=low", "-C", "/work/owner", "--add-dir", "/work/extra", "resume", resumeFixtureConversation}
					if got := execArgvTail(t, launch.argv, provider); !slices.Equal(got, want) {
						t.Fatalf("exec argv tail = %q, want %q", got, want)
					}
					if notice != "" {
						t.Fatalf("a valid effort disclosed %q", notice)
					}
				case (provider == aiModeClaude || provider == aiModeCodex) && test.name == "invalid":
					if !slices.Equal(launch.argv, plain.argv) {
						t.Fatalf("argv = %q, want the effort-free %q", launch.argv, plain.argv)
					}
					if want := wantEffortInvalidNotice("reviewer", effortInvalidFixture); notice != want {
						t.Fatalf("notice = %q, want %q", notice, want)
					}
				default:
					if !slices.Equal(launch.argv, plain.argv) {
						t.Fatalf("argv = %q, want the unannotated %q", launch.argv, plain.argv)
					}
					if slices.Contains(launch.argv, "--effort") || strings.Contains(strings.Join(launch.argv, " "), "--effort") {
						t.Fatalf("argv %q carries --effort", launch.argv)
					}
					if notice != "" {
						t.Fatalf("notice = %q, want none", notice)
					}
				}
			})
		}
	}
}

// TestResumeRepassesEffortWhereCreatePutsItBesideThePersona pins the order of
// the Claude options on resume: --effort, then the persona snapshot, as
// create spells them.
func TestResumeRepassesEffortWhereCreatePutsItBesideThePersona(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	withPersona, snapshot := createPersonaForResume(t, planner, "go-reviewer", []byte(personaResumeContent))
	annotations := withEffortAnnotation("max", withPersona)

	launch, err := planner.PlanAgentResume(aiModeClaude, coremetadata.AgentWorkspace{CWD: "/work/owner"}, personaResumeConversation, annotations)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--effort", "max", "--append-system-prompt-file", snapshot, "--resume", personaResumeConversation}
	if got := execArgvTail(t, launch.argv, aiModeClaude); !slices.Equal(got, want) {
		t.Fatalf("exec argv tail = %q, want %q", got, want)
	}
}

// TestEffortRecordedAtCreateReachesEveryResumeConsumer chains the create write
// to both resume consumers: the annotations the create stored put --effort low
// on `agent resume` and on Continue/topology replay, with no disclosure.
func TestEffortRecordedAtCreateReachesEveryResumeConsumer(t *testing.T) {
	store := newFakeResourceStore(t)
	create, _ := newTestAgentCreateCommand(t, store, newFakeTmux())
	if _, _, err := runRoute(t, create,
		"agent", "--provider", "claude", "--effort", "low",
		"--project", "alpha", "--window", "review", "--", "review this"); err != nil {
		t.Fatal(err)
	}
	recorded := agentNamed(t, store, "win-alpha-review", "agent-test-1").Metadata.Annotations

	planner := agentLaunchArgvTestCommand(t)
	want := []string{"--effort", "low", "--resume", personaResumeConversation}

	_, argv, stderr := resumeClaudeAgentWithAnnotations(t, planner, recorded)
	if got := execArgvTail(t, argv, aiModeClaude); !slices.Equal(got, want) {
		t.Fatalf("agent resume exec argv tail = %q, want %q", got, want)
	}
	if strings.Contains(stderr, claudeEffortReasonInvalid) {
		t.Fatalf("agent resume disclosed %s: %q", claudeEffortReasonInvalid, stderr)
	}

	work, plan := planClaudeTopologyReplay(t, planner, t.TempDir(), recorded)
	if got := execArgvTail(t, work.argv, aiModeClaude); !slices.Equal(got, want) {
		t.Fatalf("topology replay exec argv tail = %q, want %q", got, want)
	}
	if len(plan.notices) != 0 {
		t.Fatalf("topology replay noted %v", plan.notices)
	}
}

// TestAnInvalidRecordedEffortResumesWithoutItOnBothConsumers pins the
// hand-edited case on both consumers: the Agent still comes back, with the
// argv of an Agent that never recorded an effort, and one effort-invalid line
// is disclosed where a persona-unavailable line would be.
func TestAnInvalidRecordedEffortResumesWithoutItOnBothConsumers(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	invalid := effortAnnotations(effortInvalidFixture)

	_, plain, _ := resumeClaudeAgentWithAnnotations(t, planner, nil)
	name, argv, stderr := resumeClaudeAgentWithAnnotations(t, planner, invalid)
	if !slices.Equal(argv, plain) {
		t.Fatalf("agent resume argv = %q, want the effort-free %q", argv, plain)
	}
	wantNotice := wantEffortInvalidNotice(name, effortInvalidFixture)
	if !strings.Contains(stderr, wantNotice+"\n") || strings.Count(stderr, claudeEffortReasonInvalid) != 1 {
		t.Fatalf("agent resume stderr = %q, want one line %q", stderr, wantNotice)
	}

	root := t.TempDir()
	plainReplay, _ := planClaudeTopologyReplay(t, planner, root, nil)
	work, plan := planClaudeTopologyReplay(t, planner, root, invalid)
	if !slices.Equal(work.argv, plainReplay.argv) {
		t.Fatalf("topology replay argv = %q, want the effort-free %q", work.argv, plainReplay.argv)
	}
	wantReplayNotice := wantEffortInvalidNotice("main/reviewer", effortInvalidFixture)
	if len(plan.notices) != 1 || plan.notices[0] != wantReplayNotice {
		t.Fatalf("topology replay notices = %v, want [%q]", plan.notices, wantReplayNotice)
	}
	if len(plan.agentSkips) != 0 {
		t.Fatalf("an invalid effort skipped the Agent: %v", plan.agentSkips)
	}
	var disclosed bytes.Buffer
	plan.writeNotices(&disclosed)
	if !strings.Contains(disclosed.String(), wantReplayNotice) {
		t.Fatalf("writeNotices = %q, want the effort notice", disclosed.String())
	}
}

// TestAgentPersonaAttachAndDetachRestartKeepTheRecordedEffort covers the third
// consumer: `agent persona attach|detach` restarts through the `agent resume`
// rebinder, so the restarted Agent comes back with its recorded effort, and
// the persona change leaves the effort annotation where it was.
func TestAgentPersonaAttachAndDetachRestartKeepTheRecordedEffort(t *testing.T) {
	f := newPersonaAttachFixture(t)
	f.writePersona(t, "go-reviewer", personaResumeContent)
	f.setAnnotations(effortAnnotations("high"))

	stdout, stderr, err := runRoute(t, f.command, "persona", "attach", "uid:"+personaAttachAgent, "go-reviewer")
	if err != nil {
		t.Fatalf("attach: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	f.assertRestartedOnTheSameConversation(t, personaAttachPane)
	snapshot := f.snapshotPath(t, personaResumeContent)
	want := []string{"--effort", "high", "--append-system-prompt-file", snapshot, "--system-prompt-snapshot", "off", "--resume", personaResumeConversation}
	if got := f.lastArgvTail(t); !slices.Equal(got, want) {
		t.Fatalf("attach restart exec argv tail = %q, want %q", got, want)
	}
	if got := f.agent(t).Metadata.Annotations[coremetadata.AnnotationAgentEffort]; got != "high" {
		t.Fatalf("attach changed the recorded effort to %q", got)
	}
	if strings.Contains(stderr, claudeEffortReasonInvalid) {
		t.Fatalf("attach disclosed %s: %q", claudeEffortReasonInvalid, stderr)
	}

	// Detach restarts the now-Running Agent again; the effort stays.
	f.setInteraction(coremetadata.InteractionIdle)
	attachedPane := f.agent(t).Status.PaneRef
	f.deletes.killed = nil
	stdout, stderr, err = runRoute(t, f.command, "persona", "detach", "uid:"+personaAttachAgent)
	if err != nil {
		t.Fatalf("detach: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if len(f.deletes.killed) != 1 || f.deletes.killed[0].PaneUID != attachedPane {
		t.Fatalf("detach killed %+v, want the attached pane %s", f.deletes.killed, attachedPane)
	}
	want = []string{"--effort", "high", "--system-prompt-snapshot", "off", "--resume", personaResumeConversation}
	if got := f.lastArgvTail(t); !slices.Equal(got, want) {
		t.Fatalf("detach restart exec argv tail = %q, want %q", got, want)
	}
	if got := f.agent(t).Metadata.Annotations[coremetadata.AnnotationAgentEffort]; got != "high" {
		t.Fatalf("detach changed the recorded effort to %q", got)
	}
}

// TestAgentPersonaAttachWithAnInvalidRecordedEffortStillRestarts pins that the
// persona restart discloses a skipped effort the way `agent resume` does and
// still restarts the Agent.
func TestAgentPersonaAttachWithAnInvalidRecordedEffortStillRestarts(t *testing.T) {
	f := newPersonaAttachFixture(t)
	f.writePersona(t, "go-reviewer", personaResumeContent)
	f.setAnnotations(effortAnnotations(effortInvalidFixture))

	stdout, stderr, err := runRoute(t, f.command, "persona", "attach", "uid:"+personaAttachAgent, "go-reviewer")
	if err != nil {
		t.Fatalf("attach: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	f.assertRestartedOnTheSameConversation(t, personaAttachPane)
	want := []string{"--append-system-prompt-file", f.snapshotPath(t, personaResumeContent), "--system-prompt-snapshot", "off", "--resume", personaResumeConversation}
	if got := f.lastArgvTail(t); !slices.Equal(got, want) {
		t.Fatalf("attach restart exec argv tail = %q, want %q", got, want)
	}
	if wantNotice := wantEffortInvalidNotice(f.agent(t).Metadata.Name, effortInvalidFixture); !strings.Contains(stderr, wantNotice) {
		t.Fatalf("attach stderr = %q, want %q", stderr, wantNotice)
	}
}

// PlanAgentResumeWithModel lets the exact-argv recorder carry an
// `agent resume --model` through the real seam of its planner.
func (l *exactArgvResumeLauncher) PlanAgentResumeWithModel(provider string, workspace coremetadata.AgentWorkspace, conversationID string, annotations map[string]string, model string) (agentResumeLaunch, error) {
	launch, err := l.planner.PlanAgentResumeWithModel(provider, workspace, conversationID, annotations, model)
	if err == nil {
		l.argv = append(l.argv, slices.Clone(launch.argv))
	}
	return launch, err
}

// resumeClaudeAgentWithFlags is resumeClaudeAgentWithAnnotations with extra
// `agent resume` flags. It returns the store, so the caller can read what the
// resume recorded, and the one argv the provider was launched with.
func resumeClaudeAgentWithFlags(t *testing.T, planner *aiCommand, annotations map[string]string, flags ...string) (*fakeResourceStore, []string) {
	t.Helper()
	store := newFakeResourceStore(t)
	target, _ := store.registry.Agent("agt-beta-codex")
	target.Spec.Provider = aiModeClaude
	target.Status.SessionRef = claudeConversationRef(personaResumeConversation)
	target.Metadata.Annotations = maps.Clone(annotations)

	tmux := newFakeTmux()
	command, recorder, _, _ := newTestAgentResumeCommand(t, store, tmux)
	launcher := &exactArgvResumeLauncher{fakeResumeLauncher: recorder, planner: planner}
	command.rebind.launcher = launcher

	args := append([]string{"resume", "uid:" + target.Metadata.UID}, flags...)
	stdout, stderr, err := runRoute(t, command, args...)
	if err != nil || !strings.Contains(stdout, "resumed") {
		t.Fatalf("resume %v: stdout=%q stderr=%q err=%v", flags, stdout, stderr, err)
	}
	if len(launcher.argv) != 1 {
		t.Fatalf("planned %d provider argv values, want 1", len(launcher.argv))
	}
	calls := splitWindowCalls(tmux)
	if len(calls) != 1 {
		t.Fatalf("split-window calls = %v, want exactly one provider launch", calls)
	}
	separator := slices.Index(calls[0], "--")
	if separator < 0 || !slices.Equal(calls[0][separator+1:], launcher.argv[0]) {
		t.Fatalf("launched child argv = %q, want exact %q", calls[0], launcher.argv[0])
	}
	return store, launcher.argv[0]
}

// TestAgentResumeWithModelAndEffortPassesBothAndRecordsOnlyTheEffort is the
// override chain on a Claude Agent: `agent resume --model X --effort Y`
// launches with both where create puts them and records only Y, and the next
// plain resume of that Agent re-passes Y and no model.
func TestAgentResumeWithModelAndEffortPassesBothAndRecordsOnlyTheEffort(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	topic := map[string]string{coremetadata.AnnotationAgentTopic: "review"}

	store, argv := resumeClaudeAgentWithFlags(t, planner, withEffortAnnotation("low", topic), "--model", "opus", "--effort", "max")
	want := []string{"--model", "opus", "--effort", "max", "--resume", personaResumeConversation}
	if got := execArgvTail(t, argv, aiModeClaude); !slices.Equal(got, want) {
		t.Fatalf("override resume exec argv tail = %q, want %q", got, want)
	}
	agent, _ := store.registry.Agent("agt-beta-codex")
	recorded := agent.Metadata.Annotations
	if wantRecorded := withEffortAnnotation("max", topic); !maps.Equal(recorded, wantRecorded) {
		t.Fatalf("Agent annotations = %v, want %v (the model is not recorded)", recorded, wantRecorded)
	}
	if raw, _ := json.Marshal(agent.Metadata); strings.Contains(string(raw), "opus") {
		t.Fatalf("Agent metadata recorded the model: %s", raw)
	}

	_, argv = resumeClaudeAgentWithFlags(t, planner, recorded)
	want = []string{"--effort", "max", "--resume", personaResumeConversation}
	if got := execArgvTail(t, argv, aiModeClaude); !slices.Equal(got, want) {
		t.Fatalf("next plain resume exec argv tail = %q, want %q", got, want)
	}
}

// TestAgentResumeWithOnlyAModelRecordsNothing pins A-1 alone: a model-only
// override launches with --model and leaves an Agent without annotations
// without any.
func TestAgentResumeWithOnlyAModelRecordsNothing(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	store, argv := resumeClaudeAgentWithFlags(t, planner, nil, "--model", "sonnet")
	want := []string{"--model", "sonnet", "--resume", personaResumeConversation}
	if got := execArgvTail(t, argv, aiModeClaude); !slices.Equal(got, want) {
		t.Fatalf("exec argv tail = %q, want %q", got, want)
	}
	if agent, _ := store.registry.Agent("agt-beta-codex"); agent.Metadata.Annotations != nil {
		t.Fatalf("a model-only resume annotated the Agent: %v", agent.Metadata.Annotations)
	}
}

// TestAgentResumeWithoutOverridesKeepsTheArgvItHadBefore pins that the new
// seam without a model is PlanAgentResume byte for byte on every provider and
// every recorded effort, and that a flagless `agent resume` stores nothing new.
func TestAgentResumeWithoutOverridesKeepsTheArgvItHadBefore(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	conversations := map[string]string{
		aiModeClaude:      personaResumeConversation,
		aiModeCodex:       resumeFixtureConversation,
		aiModeAntigravity: personaResumeConversation,
	}
	for provider, conversation := range conversations {
		workspace := coremetadata.AgentWorkspace{CWD: "/work/owner"}
		if provider != aiModeAntigravity {
			workspace.AdditionalWritableRoots = []string{"/work/extra"}
		}
		for _, annotations := range []map[string]string{nil, effortAnnotations("high"), effortAnnotations(effortInvalidFixture)} {
			before, err := planner.PlanAgentResume(provider, workspace, conversation, annotations)
			if err != nil {
				t.Fatalf("%s PlanAgentResume: %v", provider, err)
			}
			after, err := planner.PlanAgentResumeWithModel(provider, workspace, conversation, annotations, "")
			if err != nil {
				t.Fatalf("%s PlanAgentResumeWithModel: %v", provider, err)
			}
			if !slices.Equal(after.argv, before.argv) || after.title != before.title || after.effortSkipped != before.effortSkipped {
				t.Fatalf("%s %v: argv = %q, want %q", provider, annotations, after.argv, before.argv)
			}
		}
	}

	store, argv := resumeClaudeAgentWithFlags(t, planner, nil)
	if got, want := execArgvTail(t, argv, aiModeClaude), []string{"--resume", personaResumeConversation}; !slices.Equal(got, want) {
		t.Fatalf("plain resume exec argv tail = %q, want %q", got, want)
	}
	if agent, _ := store.registry.Agent("agt-beta-codex"); agent.Metadata.Annotations != nil {
		t.Fatalf("a plain resume annotated the Agent: %v", agent.Metadata.Annotations)
	}
}

// TestResumeSeamPassesTheModelOverrideBeforeTheWorkspace pins the argv
// placement of the override on both CLI lanes: the model and the effort come
// before the workspace arguments, so Claude's variadic --add-dir cannot take
// them, in the order create spells them.
func TestResumeSeamPassesTheModelOverrideBeforeTheWorkspace(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	workspace := coremetadata.AgentWorkspace{CWD: "/work/owner", AdditionalWritableRoots: []string{"/work/extra"}}
	for _, test := range []struct {
		provider, conversation string
		want                   []string
	}{
		{aiModeClaude, personaResumeConversation, []string{"--model", "opus[1m]", "--effort", "xhigh", "--add-dir", "/work/extra", "--resume", personaResumeConversation}},
		{aiModeCodex, resumeFixtureConversation, []string{"-m", "gpt-6", "-c", "model_reasoning_effort=xhigh", "-C", "/work/owner", "--add-dir", "/work/extra", "resume", resumeFixtureConversation}},
	} {
		model := "opus[1m]"
		if test.provider == aiModeCodex {
			model = "gpt-6"
		}
		launch, err := planner.PlanAgentResumeWithModel(test.provider, workspace, test.conversation, effortAnnotations("xhigh"), model)
		if err != nil {
			t.Fatalf("%s: %v", test.provider, err)
		}
		if got := execArgvTail(t, launch.argv, test.provider); !slices.Equal(got, test.want) {
			t.Fatalf("%s exec argv tail = %q, want %q", test.provider, got, test.want)
		}
	}
}

// TestNativeCodexAgentResumePassesTheOverridesAtBothPlanningSites pins the
// override on the native Codex lane: the preflight plan and the plan after
// thread/resume both carry -m and the effort, the launched argv spells them,
// and only the effort is recorded.
func TestNativeCodexAgentResumePassesTheOverridesAtBothPlanningSites(t *testing.T) {
	store := newFakeResourceStore(t)
	route := nativeTestRoute("generation-override", coremetadata.CodexGenerationCurrent)
	ref := nativeTestSessionRef(route, resumeFixtureConversation)
	ref.ObservedAt = resourceFixtureClock
	setFixtureSessionRef(t, store, "agt-beta-codex", ref)
	tmux := newFakeTmux()
	command, legacy, _, _ := newTestAgentResumeCommand(t, store, tmux)
	panes := &fakeNativePaneLauncher{}
	command.rebind.launcher = &fakeNativeResumeLauncher{fakeResumeLauncher: legacy, fakeNativePaneLauncher: panes}
	command.rebind.create.codexNative = &fakeNativeThreadController{resolvedRoute: route, resumeBinding: codexappserver.ThreadBinding{ThreadID: resumeFixtureConversation}}

	stdout, stderr, err := runRoute(t, command, "resume", "uid:agt-beta-codex", "--model", "gpt-6", "--effort", "high")
	if err != nil || stdout != "agent/codex resumed\n" {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if len(panes.plans) != 2 {
		t.Fatalf("native pane plans = %+v, want the preflight and the post-resume plan", panes.plans)
	}
	for i, plan := range panes.plans {
		if plan.model != "gpt-6" || plan.effort != "high" {
			t.Fatalf("native pane plan %d = %+v, want model gpt-6 effort high", i, plan)
		}
	}
	calls := splitWindowCalls(tmux)
	if len(calls) != 1 || !strings.Contains(strings.Join(calls[0], " "), "-m gpt-6 -c model_reasoning_effort=high resume") {
		t.Fatalf("split-window calls = %v, want one launch with the overrides", calls)
	}
	agent, _ := store.registry.Agent("agt-beta-codex")
	if want := effortAnnotations("high"); !maps.Equal(agent.Metadata.Annotations, want) {
		t.Fatalf("Agent annotations = %v, want %v", agent.Metadata.Annotations, want)
	}
}

// TestAgentResumeRefusesModelAndEffortItCannotHonorWithZeroMutations is the
// preflight table of the overrides: each refusal is create's, ending
// "nothing was changed", and leaves zero transactions, writes, and tmux calls.
func TestAgentResumeRefusesModelAndEffortItCannotHonorWithZeroMutations(t *testing.T) {
	t.Parallel()
	claudeAgent := func(t *testing.T, store *fakeResourceStore) {
		target, _ := store.registry.Agent("agt-beta-codex")
		target.Spec.Provider = aiModeClaude
		target.Status.SessionRef = claudeConversationRef(personaResumeConversation)
	}
	antigravityAgent := func(t *testing.T, store *fakeResourceStore) {
		target, _ := store.registry.Agent("agt-beta-codex")
		target.Spec.Provider = aiModeAntigravity
		ref, ok := coremetadata.NewAgentSessionRef(coremetadata.AgentSessionObservation{Provider: aiModeAntigravity, SessionID: personaResumeConversation}, resourceFixtureClock)
		if !ok {
			t.Fatal("antigravity session fixture was rejected")
		}
		target.Status.SessionRef = ref
	}
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, *fakeResourceStore)
		flags   []string
		want    string
	}{
		{"model that reads as an option", claudeAgent, []string{"--model=-x"}, `agent resume --model "-x" is not a model name; nothing was changed`},
		{"model with a space", claudeAgent, []string{"--model", "a b"}, `agent resume --model "a b" is not a model name; nothing was changed`},
		{"unknown effort", claudeAgent, []string{"--effort", "turbo"}, "agent resume --effort must be one of: low, medium, high, xhigh, max; nothing was changed"},
		{"unsupported provider", antigravityAgent, []string{"--effort", "high"}, "agent resume --model and --effort apply only to --provider claude or codex; nothing was changed"},
		{"reply-only with a model", claudeAgent, []string{"--dialogue-reply-only", "--model", "opus"}, "agent resume --model and --effort cannot be combined with --dialogue-reply-only; nothing was changed"},
		{"reply-only with an effort", claudeAgent, []string{"--dialogue-reply-only", "--effort", "low"}, "agent resume --model and --effort cannot be combined with --dialogue-reply-only; nothing was changed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			test.prepare(t, store)
			tmux := newFakeTmux()
			command, launcher, _, _ := newTestAgentResumeCommand(t, store, tmux)
			before, beforeTmux := store.snapshot(), tmux.state()

			args := append([]string{"resume", "uid:agt-beta-codex"}, test.flags...)
			stdout, _, err := runRoute(t, command, args...)
			if err == nil || !IsUsageError(err) || err.Error() != test.want {
				t.Fatalf("resume %v = %v, want usage error %q", test.flags, err, test.want)
			}
			if stdout != "" || store.transactions != 0 || store.writes != 0 || store.snapshot() != before {
				t.Fatalf("refused resume acted: stdout=%q transactions=%d writes=%d", stdout, store.transactions, store.writes)
			}
			if len(tmux.calls) != 0 || tmux.state() != beforeTmux || len(launcher.plans) != 0 || len(launcher.gated) != 0 {
				t.Fatalf("refused resume reached tmux or the launcher: tmux=%v plans=%v gated=%v", tmux.calls, launcher.plans, launcher.gated)
			}
		})
	}
}

// TestAFailedEffortOverrideResumeRecordsNoEffort pins what a failed launch
// leaves: the effort is written inside the rebind transaction, and a split
// that fails rolls that transaction back, so the Agent keeps the effort it
// recorded before.
func TestAFailedEffortOverrideResumeRecordsNoEffort(t *testing.T) {
	store := newFakeResourceStore(t)
	target, _ := store.registry.Agent("agt-beta-codex")
	target.Spec.Provider = aiModeClaude
	target.Status.SessionRef = claudeConversationRef(personaResumeConversation)
	target.Metadata.Annotations = effortAnnotations("low")
	tmux := newFakeTmux()
	tmux.fail = []string{"split-window"}
	tmux.failMessage = "no space for new pane"
	command, recorder, _, _ := newTestAgentResumeCommand(t, store, tmux)
	command.rebind.launcher = &exactArgvResumeLauncher{fakeResumeLauncher: recorder, planner: agentLaunchArgvTestCommand(t)}
	before := store.snapshot()

	if _, _, err := runRoute(t, command, "resume", "uid:agt-beta-codex", "--effort", "max"); err == nil {
		t.Fatal("resume succeeded despite a failing split")
	}
	if store.writes != 0 || store.snapshot() != before {
		t.Fatalf("a rolled-back override resume committed %d writes", store.writes)
	}
	if agent, _ := store.registry.Agent("agt-beta-codex"); !maps.Equal(agent.Metadata.Annotations, effortAnnotations("low")) {
		t.Fatalf("Agent annotations = %v, want the effort it recorded before", agent.Metadata.Annotations)
	}
}
