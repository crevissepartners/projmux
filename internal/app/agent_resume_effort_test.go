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
				case provider == aiModeClaude && test.name == "invalid":
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
