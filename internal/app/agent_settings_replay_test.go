package app

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
)

// layeredTopologyLauncher is the topology replay launcher over the real resume
// seam of planner, with the settings resolver and the model launch it offers.
type layeredTopologyLauncher struct {
	*profileTopologyLauncher
}

func (l *layeredTopologyLauncher) ResolveAgentSettingsRequest(provider string, annotations map[string]string, request agentSettingsRequest) (agentSettingsLaunch, error) {
	return l.planner.ResolveAgentSettingsRequest(provider, annotations, request)
}

func (l *layeredTopologyLauncher) PlanAgentResumeWithModel(provider string, workspace coremetadata.AgentWorkspace, conversationID string, annotations map[string]string, model string) (agentResumeLaunch, error) {
	launch, err := l.planner.PlanAgentResumeWithModel(provider, workspace, conversationID, annotations, model)
	if err == nil {
		l.argv = append(l.argv, slices.Clone(launch.argv))
	}
	return launch, err
}

// unlayeredTopologyLauncher is the real resume seam of planner without the
// settings resolver: the replay launch before settings layers existed, which
// reads the annotations as they are. It keeps the guidance and link rules
// seams, so only the layers differ.
type unlayeredTopologyLauncher struct {
	planner *aiCommand
}

func (l unlayeredTopologyLauncher) RequireAgentEnabled(provider string) error {
	return l.planner.RequireAgentEnabled(provider)
}

func (l unlayeredTopologyLauncher) PlanAgentLaunch(provider string, workspace coremetadata.AgentWorkspace, payload []string) (string, []string, error) {
	return l.planner.PlanAgentLaunch(provider, workspace, payload)
}

func (l unlayeredTopologyLauncher) PlanAgentResume(provider string, workspace coremetadata.AgentWorkspace, conversationID string, annotations map[string]string) (agentResumeLaunch, error) {
	return l.planner.PlanAgentResume(provider, workspace, conversationID, annotations)
}

func (l unlayeredTopologyLauncher) BindAgentPaneOnRoute(ctx context.Context, runner tmuxCommandRunner, binding agentPaneBinding) error {
	return l.planner.BindAgentPaneOnRoute(ctx, runner, binding)
}

func (l unlayeredTopologyLauncher) PlanAgentGuidance(provider string, recorded map[string]string) agentGuidanceLaunch {
	return l.planner.PlanAgentGuidance(provider, recorded)
}

func (l unlayeredTopologyLauncher) PlanProjectLinks(provider string, project coremetadata.Project, recorded map[string]string) projectLinksLaunch {
	return l.planner.PlanProjectLinks(provider, project, recorded)
}

// replaySettingsAgent brings a Claude Agent carrying annotations back through
// Continue/topology replay over the real resume seam of planner, and returns
// the replayed Agent, the one argv it launched, and the replay's stderr.
func replaySettingsAgent(t *testing.T, planner *aiCommand, annotations map[string]string) (coremetadata.Agent, []string, string) {
	t.Helper()
	command, store, _, _, root, _ := newTopologyMaterializeFixture(t)
	launcher := &layeredTopologyLauncher{&profileTopologyLauncher{fakeTopologyAgentLauncher: command.agents.(*fakeTopologyAgentLauncher), planner: planner}}
	command.agents = launcher
	agent := addTopologyFixtureAgent(t, store, topologyFixtureAgent{name: "claude", provider: "claude", cwd: root, ref: claudeConversationRef(personaResumeConversation)})
	stored, _ := store.registry.Agent(agent.Metadata.UID)
	stored.Metadata.Annotations = maps.Clone(annotations)
	markTopologyAgentInterrupted(t, store, agent.Metadata.UID, "")

	_, stderr, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
	if err != nil {
		t.Fatalf("materialize: %v (%s)", err, stderr)
	}
	if len(launcher.argv) != 1 {
		t.Fatalf("replay planned %d argv, want 1 (stderr %q)", len(launcher.argv), stderr)
	}
	after, _ := store.registry.Agent(agent.Metadata.UID)
	if after.Status.Phase != coremetadata.PhaseRunning {
		t.Fatalf("replayed Agent is %s, want Running (stderr %q)", after.Status.Phase, stderr)
	}
	return after.Clone(), launcher.argv[0], stderr
}

// TestAContinueReplayLaunchesAndRecordsWhatAgentResumeDoes is Task 4
// acceptance 1 and 5: after the profile's effort and model and its
// instructions' content are edited, the Continue replay of an Agent launches
// the model, effort and instructions `agent resume` of the same Agent
// launches -- the new ones, on the items the Agent takes from its profile,
// and its own on the item it overrides -- and records the same values and
// sources.
func TestAContinueReplayLaunchesAndRecordsWhatAgentResumeDoes(t *testing.T) {
	f := newSettingsFixture(t)
	fromProfile := withAnnotations(f.created,
		coremetadata.AnnotationAgentProfileSource, coremetadata.SettingSourceRole,
		coremetadata.AnnotationAgentInstructionsSource, coremetadata.SettingSourceProfile,
		coremetadata.AnnotationAgentModelSource, coremetadata.SettingSourceProfile,
		coremetadata.AnnotationAgentEffortSource, coremetadata.SettingSourceProfile)
	overridden := withAnnotations(fromProfile,
		coremetadata.AnnotationAgentEffort, "low",
		coremetadata.AnnotationAgentEffortSource, coremetadata.SettingSourceRelaunch)
	next := f.writeInstructions(t, "lead", "You lead one Task, delegate the code, and review it.")
	edited := f.profiles.writeProfile(t, "role", "instructions = \"lead\"\nmodel = \"sonnet\"\neffort = \"max\"\n")
	snapshot, err := f.profiles.personas.SnapshotPath(next)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name        string
		annotations map[string]string
		effort      string
		source      string
	}{
		{name: "every item from the profile", annotations: fromProfile, effort: "max", source: coremetadata.SettingSourceProfile},
		{name: "effort overridden", annotations: overridden, effort: "low", source: coremetadata.SettingSourceRelaunch},
	} {
		t.Run(test.name, func(t *testing.T) {
			resumed, resumeArgv, resumeStderr := resumeSettingsAgent(t, f.planner, test.annotations, true)
			replayed, replayArgv, replayStderr := replaySettingsAgent(t, f.planner, test.annotations)
			want := []string{"--model", "sonnet", "--effort", test.effort, "--append-system-prompt-file", snapshot, "--system-prompt-snapshot", "off", "--resume", personaResumeConversation}
			if got := execArgvTail(t, resumeArgv, aiModeClaude); !slices.Equal(got, want) || resumeStderr != "" {
				t.Fatalf("agent resume exec tail = %q (stderr %q), want %q", got, resumeStderr, want)
			}
			if got := execArgvTail(t, replayArgv, aiModeClaude); !slices.Equal(got, want) || strings.Contains(replayStderr, "projmux: agent/") {
				t.Fatalf("replay exec tail = %q (stderr %q), want the agent resume one %q", got, replayStderr, want)
			}
			wantAnnotations := withAnnotations(test.annotations,
				coremetadata.AnnotationAgentProfileDigest, edited,
				coremetadata.AnnotationAgentPersonaDigest, next,
				coremetadata.AnnotationAgentSystemPromptSnapshot, coremetadata.SystemPromptSnapshotOff,
				coremetadata.AnnotationAgentModel, "sonnet",
				coremetadata.AnnotationAgentEffort, test.effort,
				coremetadata.AnnotationAgentEffortSource, test.source)
			if !maps.Equal(resumed.Metadata.Annotations, wantAnnotations) {
				t.Fatalf("agent resume annotations = %v\nwant %v", resumed.Metadata.Annotations, wantAnnotations)
			}
			if !maps.Equal(replayed.Metadata.Annotations, wantAnnotations) {
				t.Fatalf("replay annotations = %v\nwant %v", replayed.Metadata.Annotations, wantAnnotations)
			}
		})
	}
}

// TestAContinueReplayOfAnAgentRecordedBeforeSourcesLaunchesTheArgvItHadBefore
// is Task 4 acceptance 3: an Agent without source keys -- created from a
// profile, with a flag over it, with a value the profile never gave it, or
// without any profile -- is replayed with exactly the argv the replay built
// before settings layers existed, and without a reason to differ (O-1).
func TestAContinueReplayOfAnAgentRecordedBeforeSourcesLaunchesTheArgvItHadBefore(t *testing.T) {
	f := newSettingsFixture(t)
	withoutProfile := withAnnotations(f.created, coremetadata.AnnotationAgentProfile, "", coremetadata.AnnotationAgentProfileDigest, "")
	for _, test := range []struct {
		name        string
		annotations map[string]string
	}{
		{name: "created from the whole profile", annotations: f.created},
		{name: "effort flag over the profile", annotations: withAnnotations(f.created, coremetadata.AnnotationAgentEffort, "low")},
		{name: "profile model never recorded", annotations: withAnnotations(f.created, coremetadata.AnnotationAgentModel, "")},
		{name: "no profile", annotations: withoutProfile},
		{name: "nothing recorded"},
	} {
		t.Run(test.name, func(t *testing.T) {
			agent := coremetadata.Agent{
				Metadata: coremetadata.ObjectMeta{Name: "reviewer", Annotations: test.annotations},
				Spec:     coremetadata.AgentSpec{Provider: aiModeClaude, Workspace: coremetadata.AgentWorkspace{CWD: t.TempDir()}},
				Status:   coremetadata.AgentStatus{SessionRef: claudeConversationRef(personaResumeConversation)},
			}
			project := coremetadata.Project{Spec: coremetadata.ProjectSpec{Root: agent.Spec.Workspace.CWD}}
			beforePlan, afterPlan := &registryTopologyPlan{}, &registryTopologyPlan{}
			before, ok := planTopologyAgentReplay(beforePlan, project, agent, "main/reviewer", unlayeredTopologyLauncher{planner: f.planner})
			if !ok {
				t.Fatalf("unlayered replay skipped the Agent: %v", beforePlan.notices)
			}
			after, ok := planTopologyAgentReplay(afterPlan, project, agent, "main/reviewer", f.planner)
			if !ok {
				t.Fatalf("layered replay skipped the Agent: %v", afterPlan.notices)
			}
			if !slices.Equal(after.argv, before.argv) || !slices.Equal(afterPlan.notices, beforePlan.notices) {
				t.Fatalf("layered replay argv = %q notices %v\nwant the unlayered %q notices %v", after.argv, afterPlan.notices, before.argv, beforePlan.notices)
			}
			if !after.settings.layered || len(after.settings.resolution.Reasons) != 0 || after.settings.resolution.PassModel {
				t.Fatalf("layered replay settings = %+v, want layered with no reason and no model passed", after.settings)
			}
		})
	}
}

// TestAContinueReplayOfACodexAgentDisclosesInstructionsItCannotApply is O-6
// on Continue replay: a Codex thread keeps the developer instructions it
// started with, so the replay discloses the edited instructions, keeps the
// recorded ones, and still takes the profile's new effort.
func TestAContinueReplayOfACodexAgentDisclosesInstructionsItCannotApply(t *testing.T) {
	f := newSettingsFixture(t)
	annotations := withAnnotations(f.created,
		coremetadata.AnnotationAgentInstructionsSource, coremetadata.SettingSourceProfile,
		coremetadata.AnnotationAgentEffortSource, coremetadata.SettingSourceProfile)
	f.writeInstructions(t, "lead", "You lead one Task, delegate the code, and review it.")
	f.profiles.writeProfile(t, "role", "instructions = \"lead\"\nmodel = \"opus\"\neffort = \"max\"\n")
	root := t.TempDir()
	agent := coremetadata.Agent{
		Metadata: coremetadata.ObjectMeta{Name: "coder", Annotations: annotations},
		Spec:     coremetadata.AgentSpec{Provider: aiModeCodex, Workspace: coremetadata.AgentWorkspace{CWD: root}},
		Status:   coremetadata.AgentStatus{SessionRef: codexConversationRef(resumeFixtureConversation)},
	}
	plan := &registryTopologyPlan{}
	work, ok := planTopologyAgentReplay(plan, coremetadata.Project{Spec: coremetadata.ProjectSpec{Root: root}}, agent, "main/coder", f.planner)
	if !ok {
		t.Fatalf("replay skipped the Codex Agent: %v", plan.notices)
	}
	if !strings.Contains(strings.Join(work.argv, " "), "'-c' 'model_reasoning_effort=max'") {
		t.Fatalf("Codex replay argv %q does not carry the profile's new effort", work.argv)
	}
	wantNotice := "projmux: agent/main/coder resumed with the instructions its Codex thread started with; instructions lead were not applied (" + personaReasonCodexInstructionsImmutable + ")"
	if len(plan.notices) != 1 || !strings.HasPrefix(plan.notices[0], wantNotice) {
		t.Fatalf("Codex replay notices = %v, want one starting %q", plan.notices, wantNotice)
	}
	if got := work.settings.resolution.New.Instructions; got.Value != "lead" || work.settings.resolution.InstructionsChanged() {
		t.Fatalf("Codex replay instructions = %+v, want the recorded ones unchanged", got)
	}
}

// TestResumePickerLaunchesWhatTheResumeOfItsAgentWould is Task 4 acceptance 2:
// a resume-picker create inherits the holders' launch values (holders that
// differ only in their sources still agree) and then takes them through the
// settings layers, so inherited instructions whose file was edited launch
// with a snapshot of the new content and the snapshot mode off, are recorded
// that way with their inherited source, and launch exactly what `agent
// resume` of the Agent it records launches.
func TestResumePickerLaunchesWhatTheResumeOfItsAgentWould(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	turnAgentGuidanceOff(t, planner)
	personaAnnotations, _ := createPersonaForResume(t, planner, "go-reviewer", []byte(personaResumeContent))
	bundle := claudeLaunchBundle(personaAnnotations, "low")
	edited := []byte("You are a pirate. Answer only in shanties.")
	store := personaStoreFor(t, planner)
	if _, err := store.Write("go-reviewer", edited); err != nil {
		t.Fatal(err)
	}
	editedSnapshot, err := store.SnapshotPath(persona.Digest(edited))
	if err != nil {
		t.Fatal(err)
	}

	f := newPickerLaunchValuesFixture(t, planner)
	f.hold(t, "agt-alpha-codex", aiModeClaude, personaResumeConversation, plusSources(bundle, sources(instructionsFlag, effortFlag)...), nil)
	f.hold(t, "agt-beta-codex", aiModeClaude, personaResumeConversation, bundle, nil)
	agent, argv, stderr := f.pick(t, false, aiModeClaude, personaResumeConversation, "")
	want := []string{"--effort", "low", "--append-system-prompt-file", editedSnapshot, "--system-prompt-snapshot", "off", "--resume", personaResumeConversation}
	if got := execArgvTail(t, argv, aiModeClaude); !slices.Equal(got, want) || stderr != "" {
		t.Fatalf("picker exec tail = %q (stderr %q), want %q", got, stderr, want)
	}
	wantAnnotations := plusSources(withAnnotations(bundle, coremetadata.AnnotationAgentPersonaDigest, persona.Digest(edited)), sources(instructionsInh, effortInherited)...)
	if !maps.Equal(agent.Metadata.Annotations, withUICreator(wantAnnotations)) {
		t.Fatalf("picker Agent annotations = %v\nwant %v", agent.Metadata.Annotations, wantAnnotations)
	}

	_, resumeArgv, _ := resumeSettingsAgent(t, planner, agent.Metadata.Annotations, true)
	if got := execArgvTail(t, resumeArgv, aiModeClaude); !slices.Equal(got, want) {
		t.Fatalf("agent resume of the picker Agent exec tail = %q, want the picker's %q", got, want)
	}
}

// promptPartsResumeLauncher is the exact-argv resume launcher that also reads
// the agent guidance and the Project's label link rules from planner, as the
// production resume launcher does.
type promptPartsResumeLauncher struct {
	*exactArgvResumeLauncher
}

func (l *promptPartsResumeLauncher) PlanProjectLinks(provider string, project coremetadata.Project, recorded map[string]string) projectLinksLaunch {
	return l.planner.PlanProjectLinks(provider, project, recorded)
}

func (l *promptPartsResumeLauncher) PlanAgentGuidance(provider string, recorded map[string]string) agentGuidanceLaunch {
	return l.planner.PlanAgentGuidance(provider, recorded)
}

// TestAPlainRelaunchRestartsAnAgentWhoseGuidanceOrLinkRulesChanged is Task 4
// acceptance 4 (Epic decision T4-0): the agent guidance and the Project's
// label link rules are part of the "relaunch needed" answer. With both as the
// Agent recorded them a plain relaunch is unchanged; editing the guidance
// alone, then adding link rules, gives guidance-changed and then
// link-rules-changed after it; the plain relaunch restarts the Agent with
// both and records them, and is unchanged again afterwards.
func TestAPlainRelaunchRestartsAnAgentWhoseGuidanceOrLinkRulesChanged(t *testing.T) {
	f := newRelaunchFixture(t)
	alpha, _ := reUIDLinkRulesProjects(t, f.store)
	f.command.rebind.launcher = &promptPartsResumeLauncher{f.launcher}
	paths := linkRulesPaths(t, f.planner)
	v1 := []byte("Prefer projmux create agent and projmux agent message send.")
	writeAgentGuidance(t, paths, v1)
	_, v1Digest := agentGuidanceSnapshot(t, paths, v1)
	f.setAnnotations(withAnnotations(effortAnnotations("high"), coremetadata.AnnotationAgentGuidanceDigest, v1Digest))

	relaunchJSON := func(t *testing.T, args ...string) string {
		t.Helper()
		stdout, stderr, err := runRoute(t, f.command, append([]string{"relaunch", "uid:" + personaAttachAgent}, args...)...)
		if err != nil {
			t.Fatalf("relaunch %v: %v (%s)", args, err, stderr)
		}
		return stdout
	}
	reasons := func(t *testing.T, stdout string, want ...string) {
		t.Helper()
		suffix := `"relaunchReasons":[]}` + "\n"
		if len(want) > 0 {
			suffix = `"relaunchReasons":["` + strings.Join(want, `","`) + `"]}` + "\n"
		}
		if !strings.HasSuffix(stdout, suffix) {
			t.Fatalf("relaunch JSON = %s, want it to end %s", stdout, suffix)
		}
	}

	stdout := relaunchJSON(t, "--dry-run", "-o", "json")
	reasons(t, stdout)
	if !strings.Contains(stdout, `"outcome":"unchanged"`) {
		t.Fatalf("relaunch of an Agent with its recorded guidance = %s, want unchanged", stdout)
	}

	v2 := []byte("Prefer projmux create agent, projmux agent message send, and projmux label.")
	writeAgentGuidance(t, paths, v2)
	stdout = relaunchJSON(t, "--dry-run", "-o", "json")
	reasons(t, stdout, agentsettings.ReasonGuidanceChanged)
	if !strings.Contains(stdout, `"outcome":"would-restart"`) || !strings.Contains(stdout, `"restart":true`) {
		t.Fatalf("relaunch dry run after a guidance edit = %s, want would-restart", stdout)
	}

	writeLinkRules(t, paths, alpha, linkRulesAlpha)
	stdout = relaunchJSON(t, "--dry-run", "-o", "json")
	reasons(t, stdout, agentsettings.ReasonGuidanceChanged, agentsettings.ReasonLinkRulesChanged)
	if len(splitWindowCalls(f.tmux)) != 0 || len(f.deletes.killed) != 0 {
		t.Fatal("a dry run changed something")
	}

	stdout = relaunchJSON(t, "-o", "json")
	if !strings.Contains(stdout, `"outcome":"restarted"`) {
		t.Fatalf("plain relaunch = %s, want restarted", stdout)
	}
	after := f.assertRestartedOnTheSameConversation(t, personaAttachPane)
	_, v2Digest := agentGuidanceSnapshot(t, paths, v2)
	_, rulesDigest := linkRulesSnapshot(t, paths, linkRulesAlpha, linkRulesProject(t, f.store, alpha))
	if got := after.Metadata.Annotations; got[coremetadata.AnnotationAgentGuidanceDigest] != v2Digest ||
		got[coremetadata.AnnotationAgentProjectLinkRulesDigest] != rulesDigest ||
		got[coremetadata.AnnotationAgentSystemPromptSnapshot] != coremetadata.SystemPromptSnapshotOff {
		t.Fatalf("relaunched annotations = %v, want guidance %s, rules %s and the snapshot mode off", got, v2Digest, rulesDigest)
	}
	if tail := f.lastArgvTail(t); !slices.Contains(tail, "--append-system-prompt-file") || !slices.Contains(tail, "off") {
		t.Fatalf("relaunch exec tail = %q, want the new system prompt with the snapshot mode off", tail)
	}

	f.setInteraction(coremetadata.InteractionIdle)
	calls := len(splitWindowCalls(f.tmux))
	stdout = relaunchJSON(t, "-o", "json")
	reasons(t, stdout)
	if !strings.Contains(stdout, `"outcome":"unchanged"`) || len(splitWindowCalls(f.tmux)) != calls {
		t.Fatalf("second plain relaunch = %s (split-window %d -> %d), want unchanged", stdout, calls, len(splitWindowCalls(f.tmux)))
	}
}
