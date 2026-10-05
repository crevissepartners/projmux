package app

import (
	"context"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// The sources a resume records for the items it found in the profile layer
// of an Agent recorded before sources were (O-1): items whose recorded value,
// none included, equals the profile's.
var (
	instructionsEffortFollowProfile = []string{
		coremetadata.AnnotationAgentInstructionsSource, coremetadata.SettingSourceProfile,
		coremetadata.AnnotationAgentEffortSource, coremetadata.SettingSourceProfile,
	}
	modelEffortFollowProfile = []string{
		coremetadata.AnnotationAgentModelSource, coremetadata.SettingSourceProfile,
		coremetadata.AnnotationAgentEffortSource, coremetadata.SettingSourceProfile,
	}
	allFollowProfile = append(slices.Clone(instructionsEffortFollowProfile), coremetadata.AnnotationAgentModelSource, coremetadata.SettingSourceProfile)
)

// unlayeredResumeLauncher is the exact-argv resume launcher without the
// settings resolver: the launch the rebind made before settings layers
// existed, which reads the annotations as they are.
type unlayeredResumeLauncher struct {
	inner *exactArgvResumeLauncher
}

func (l unlayeredResumeLauncher) RequireAgentEnabled(provider string) error {
	return l.inner.RequireAgentEnabled(provider)
}

func (l unlayeredResumeLauncher) PlanAgentResume(provider string, workspace coremetadata.AgentWorkspace, conversationID string, annotations map[string]string) (agentResumeLaunch, error) {
	return l.inner.PlanAgentResume(provider, workspace, conversationID, annotations)
}

func (l unlayeredResumeLauncher) BindAgentPaneOnRoute(ctx context.Context, runner tmuxCommandRunner, binding agentPaneBinding) error {
	return l.inner.BindAgentPaneOnRoute(ctx, runner, binding)
}

// resumeSettingsAgent runs `agent resume` on a Claude Agent carrying
// annotations through the real resume seam of planner, layered or not, and
// returns the resumed Agent, the one argv it launched, and stderr.
func resumeSettingsAgent(t *testing.T, planner *aiCommand, annotations map[string]string, layered bool) (coremetadata.Agent, []string, string) {
	t.Helper()
	store := newFakeResourceStore(t)
	target, _ := store.registry.Agent("agt-beta-codex")
	target.Spec.Provider = aiModeClaude
	target.Status.SessionRef = claudeConversationRef(personaResumeConversation)
	target.Metadata.Annotations = maps.Clone(annotations)

	tmux := newFakeTmux()
	command, recorder, _, _ := newTestAgentResumeCommand(t, store, tmux)
	exact := &exactArgvResumeLauncher{fakeResumeLauncher: recorder, planner: planner}
	command.rebind.launcher = exact
	if !layered {
		command.rebind.launcher = unlayeredResumeLauncher{inner: exact}
	}
	_, stderr, err := runRoute(t, command, "resume", "uid:"+target.Metadata.UID)
	if err != nil {
		t.Fatalf("resume: %v (%s)", err, stderr)
	}
	if len(exact.argv) != 1 {
		t.Fatalf("resume planned %d argv, want 1", len(exact.argv))
	}
	calls := splitWindowCalls(tmux)
	separator := slices.Index(calls[0], "--")
	if len(calls) != 1 || separator < 0 || !slices.Equal(calls[0][separator+1:], exact.argv[0]) {
		t.Fatalf("launched %q, want exactly the planned %q", calls, exact.argv[0])
	}
	agent, _ := store.registry.Agent("agt-beta-codex")
	return agent.Clone(), exact.argv[0], stderr
}

// settingsFixture is a planner home with one role profile and its
// instructions, and the annotations of an Agent created from that profile
// before sources were recorded.
type settingsFixture struct {
	planner  *aiCommand
	profiles profileFixture
	digest   string
	created  map[string]string
}

const settingsRoleInstructions = "You lead one Task and delegate the code."

func newSettingsFixture(t *testing.T) settingsFixture {
	t.Helper()
	f := newProfileFixture(t)
	turnAgentGuidanceOff(t, f.planner)
	entry, err := f.personas.Write("lead", []byte(settingsRoleInstructions))
	if err != nil {
		t.Fatal(err)
	}
	// The create wrote the snapshot its Agent launched with.
	if _, err := f.personas.WriteSnapshot([]byte(settingsRoleInstructions)); err != nil {
		t.Fatal(err)
	}
	digest := f.writeProfile(t, "role", "instructions = \"lead\"\nmodel = \"opus\"\neffort = \"high\"\n")
	return settingsFixture{planner: f.planner, profiles: f, digest: digest, created: map[string]string{
		coremetadata.AnnotationAgentProfile:       "role",
		coremetadata.AnnotationAgentProfileDigest: digest,
		coremetadata.AnnotationAgentPersona:       "lead",
		coremetadata.AnnotationAgentPersonaDigest: entry.Digest,
		coremetadata.AnnotationAgentModel:         "opus",
		coremetadata.AnnotationAgentEffort:        "high",
	}}
}

func (f settingsFixture) writeInstructions(t *testing.T, name, content string) string {
	t.Helper()
	if _, err := f.profiles.personas.Write(name, []byte(content)); err != nil {
		t.Fatal(err)
	}
	return persona.Digest([]byte(content))
}

func withAnnotations(base map[string]string, kv ...string) map[string]string {
	out := maps.Clone(base)
	if out == nil {
		out = map[string]string{}
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "" {
			delete(out, kv[i])
			continue
		}
		out[kv[i]] = kv[i+1]
	}
	return out
}

// TestAResumeOfAnAgentRecordedBeforeSourcesLaunchesTheArgvItHadBefore is
// Task 2 acceptance 1: an Agent without source keys -- created from a profile,
// with a flag over it, with a value the profile never gave it, or without any
// profile -- resumes with exactly the argv the rebind built before settings
// layers existed. Only the sources of the items found in the profile layer
// are added (O-1).
func TestAResumeOfAnAgentRecordedBeforeSourcesLaunchesTheArgvItHadBefore(t *testing.T) {
	f := newSettingsFixture(t)
	withoutProfile := withAnnotations(f.created, coremetadata.AnnotationAgentProfile, "", coremetadata.AnnotationAgentProfileDigest, "")
	for _, test := range []struct {
		name        string
		annotations map[string]string
		sources     []string
	}{
		{name: "created from the whole profile", annotations: f.created, sources: []string{
			coremetadata.AnnotationAgentInstructionsSource, coremetadata.AnnotationAgentModelSource, coremetadata.AnnotationAgentEffortSource}},
		{name: "effort flag over the profile", annotations: withAnnotations(f.created, coremetadata.AnnotationAgentEffort, "low"), sources: []string{
			coremetadata.AnnotationAgentInstructionsSource, coremetadata.AnnotationAgentModelSource}},
		{name: "profile model never recorded", annotations: withAnnotations(f.created, coremetadata.AnnotationAgentModel, ""), sources: []string{
			coremetadata.AnnotationAgentInstructionsSource, coremetadata.AnnotationAgentEffortSource}},
		{name: "no profile", annotations: withoutProfile},
		{name: "nothing recorded"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, before, _ := resumeSettingsAgent(t, f.planner, test.annotations, false)
			agent, after, stderr := resumeSettingsAgent(t, f.planner, test.annotations, true)
			if !slices.Equal(after, before) {
				t.Fatalf("layered resume argv = %q\nwant the unlayered %q", after, before)
			}
			if stderr != "" {
				t.Fatalf("layered resume disclosed %q", stderr)
			}
			want := maps.Clone(test.annotations)
			for _, key := range test.sources {
				want[key] = coremetadata.SettingSourceProfile
			}
			if got := agent.Metadata.Annotations; !maps.Equal(got, want) && (len(got) != 0 || len(want) != 0) {
				t.Fatalf("annotations = %v, want %v", got, want)
			}
		})
	}
}

// TestAnEditedProfileReachesTheNextResumeExceptWhereTheAgentOverridesIt is
// acceptance 2 and 3: the profile's new effort, instructions and model reach
// the items the Agent takes from it, their values and sources are recorded,
// and the effort the Agent overrides is kept.
func TestAnEditedProfileReachesTheNextResumeExceptWhereTheAgentOverridesIt(t *testing.T) {
	f := newSettingsFixture(t)
	annotations := withAnnotations(f.created,
		coremetadata.AnnotationAgentProfileSource, coremetadata.SettingSourceRole,
		coremetadata.AnnotationAgentInstructionsSource, coremetadata.SettingSourceProfile,
		coremetadata.AnnotationAgentModelSource, coremetadata.SettingSourceProfile,
		coremetadata.AnnotationAgentEffortSource, coremetadata.SettingSourceProfile)
	next := f.writeInstructions(t, "lead-v2", "You lead one Task, delegate the code, and review it.")
	edited := f.profiles.writeProfile(t, "role", "instructions = \"lead-v2\"\nmodel = \"sonnet\"\neffort = \"max\"\n")

	agent, argv, stderr := resumeSettingsAgent(t, f.planner, annotations, true)
	snapshot, err := f.profiles.personas.SnapshotPath(next)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--model", "sonnet", "--effort", "max", "--append-system-prompt-file", snapshot, "--system-prompt-snapshot", "off", "--resume", personaResumeConversation}
	if got := execArgvTail(t, argv, aiModeClaude); !slices.Equal(got, want) || stderr != "" {
		t.Fatalf("resume exec tail = %q (stderr %q), want %q", got, stderr, want)
	}
	wantAnnotations := withAnnotations(annotations,
		coremetadata.AnnotationAgentProfileDigest, edited,
		coremetadata.AnnotationAgentPersona, "lead-v2",
		coremetadata.AnnotationAgentPersonaDigest, next,
		coremetadata.AnnotationAgentSystemPromptSnapshot, coremetadata.SystemPromptSnapshotOff,
		coremetadata.AnnotationAgentModel, "sonnet",
		coremetadata.AnnotationAgentEffort, "max")
	if !maps.Equal(agent.Metadata.Annotations, wantAnnotations) {
		t.Fatalf("annotations = %v\nwant %v", agent.Metadata.Annotations, wantAnnotations)
	}

	// The same edit leaves an overridden effort alone, and a model the
	// profile did not change is not passed.
	f.profiles.writeProfile(t, "role", "instructions = \"lead-v2\"\nmodel = \"opus\"\neffort = \"max\"\n")
	overridden := withAnnotations(wantAnnotations,
		coremetadata.AnnotationAgentModel, "opus",
		coremetadata.AnnotationAgentEffort, "low",
		coremetadata.AnnotationAgentEffortSource, coremetadata.SettingSourceRelaunch)
	agent, argv, _ = resumeSettingsAgent(t, f.planner, overridden, true)
	want = []string{"--effort", "low", "--append-system-prompt-file", snapshot, "--system-prompt-snapshot", "off", "--resume", personaResumeConversation}
	if got := execArgvTail(t, argv, aiModeClaude); !slices.Equal(got, want) {
		t.Fatalf("overridden resume exec tail = %q, want %q", got, want)
	}
	if got := agent.Metadata.Annotations; got[coremetadata.AnnotationAgentEffort] != "low" ||
		got[coremetadata.AnnotationAgentEffortSource] != coremetadata.SettingSourceRelaunch || got[coremetadata.AnnotationAgentModel] != "opus" {
		t.Fatalf("overridden annotations = %v", got)
	}
}

// TestResumePassesTheEditedInstructionsWithTheSnapshotOff is acceptance 4
// (U2 (a)) for instructions the Agent was given explicitly: editing their
// file reaches the next `agent resume`, as a new snapshot with the recorded
// system prompt turned off, and the record follows.
func TestResumePassesTheEditedInstructionsWithTheSnapshotOff(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	turnAgentGuidanceOff(t, planner)
	withPersona, _ := createPersonaForResume(t, planner, "go-reviewer", []byte(personaResumeContent))
	withPersona[coremetadata.AnnotationAgentInstructionsSource] = coremetadata.SettingSourceFlag
	edited := []byte("You are a pirate. Answer only in shanties.")
	store := personaStoreFor(t, planner)
	if _, err := store.Write("go-reviewer", edited); err != nil {
		t.Fatal(err)
	}
	editedSnapshot, err := store.SnapshotPath(persona.Digest(edited))
	if err != nil {
		t.Fatal(err)
	}
	agent, argv, stderr := resumeSettingsAgent(t, planner, withPersona, true)
	want := []string{"--append-system-prompt-file", editedSnapshot, "--system-prompt-snapshot", "off", "--resume", personaResumeConversation}
	if got := execArgvTail(t, argv, aiModeClaude); !slices.Equal(got, want) || stderr != "" {
		t.Fatalf("resume exec tail = %q (stderr %q), want %q", got, stderr, want)
	}
	if content, err := os.ReadFile(editedSnapshot); err != nil || string(content) != string(edited) {
		t.Fatalf("new snapshot holds %q (%v)", content, err)
	}
	wantAnnotations := withAnnotations(withPersona,
		coremetadata.AnnotationAgentPersonaDigest, persona.Digest(edited),
		coremetadata.AnnotationAgentSystemPromptSnapshot, coremetadata.SystemPromptSnapshotOff)
	if !maps.Equal(agent.Metadata.Annotations, wantAnnotations) {
		t.Fatalf("annotations = %v, want %v", agent.Metadata.Annotations, wantAnnotations)
	}

	// Instructions that cannot be read now keep the recorded snapshot and say
	// so (T2-0 ②).
	if err := store.Delete("go-reviewer"); err != nil {
		t.Fatal(err)
	}
	agent, argv, stderr = resumeSettingsAgent(t, planner, wantAnnotations, true)
	if got := execArgvTail(t, argv, aiModeClaude); !slices.Equal(got, want) {
		t.Fatalf("resume of deleted instructions exec tail = %q, want the recorded snapshot %q", got, want)
	}
	if !strings.Contains(stderr, "instructions go-reviewer cannot be read now ("+persona.ReasonUnavailable+")") {
		t.Fatalf("stderr = %q, want the instructions notice", stderr)
	}
	if !maps.Equal(agent.Metadata.Annotations, wantAnnotations) {
		t.Fatalf("annotations = %v, want them unchanged", agent.Metadata.Annotations)
	}
}

// TestAgentSettingsReasonsAreTheResolverTokens keeps the reasons relaunch
// prints to the resolver's own tokens.
func TestAgentSettingsReasonsAreTheResolverTokens(t *testing.T) {
	f := newSettingsFixture(t)
	f.profiles.writeProfile(t, "role", "instructions = \"lead\"\nmodel = \"opus\"\neffort = \"max\"\n")
	launch, err := f.planner.ResolveAgentSettings(aiModeClaude, withAnnotations(f.created, coremetadata.AnnotationAgentEffortSource, coremetadata.SettingSourceProfile), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{agentsettings.ReasonProfileChanged, agentsettings.ReasonEffortChanged}; !slices.Equal(launch.resolution.Reasons, want) {
		t.Fatalf("reasons = %v, want %v", launch.resolution.Reasons, want)
	}
}

// TestACodexResumeDisclosesInstructionsItCannotApplyAndTakesTheRest is
// acceptance 5 (O-6): a Codex thread keeps the developer instructions it
// started with, so the resume discloses the edited instructions and records
// nothing for them, and still takes the profile's new effort.
func TestACodexResumeDisclosesInstructionsItCannotApplyAndTakesTheRest(t *testing.T) {
	store := newFakeResourceStore(t)
	route := nativeTestRoute("generation-settings", coremetadata.CodexGenerationCurrent)
	ref := nativeTestSessionRef(route, resumeFixtureConversation)
	ref.ObservedAt = resourceFixtureClock
	setFixtureSessionRef(t, store, "agt-beta-codex", ref)
	tmux := newFakeTmux()
	command, legacy, _, _ := newTestAgentResumeCommand(t, store, tmux)
	command.rebind.launcher = &fakeNativeResumeLauncher{fakeResumeLauncher: legacy, fakeNativePaneLauncher: &fakeNativePaneLauncher{}}
	command.rebind.create.codexNative = &fakeNativeThreadController{resolvedRoute: route, resumeBinding: codexappserver.ThreadBinding{ThreadID: resumeFixtureConversation}}
	profiles := codexProfileHome(t, command.rebind.create)
	paths, err := configPaths(command.rebind.create.homeDir, command.rebind.create.lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	personas := persona.NewDefaultStore(paths)
	started, err := personas.Write("lead", []byte(settingsRoleInstructions))
	if err != nil {
		t.Fatal(err)
	}
	digest := writeCodexProfile(t, profiles, "guard", "instructions = \"lead\"\neffort = \"max\"\n")
	annotations := map[string]string{
		coremetadata.AnnotationAgentProfile:            "guard",
		coremetadata.AnnotationAgentProfileDigest:      digest,
		coremetadata.AnnotationAgentPersona:            "lead",
		coremetadata.AnnotationAgentPersonaDigest:      started.Digest,
		coremetadata.AnnotationAgentInstructionsSource: coremetadata.SettingSourceProfile,
		coremetadata.AnnotationAgentEffort:             "high",
		coremetadata.AnnotationAgentEffortSource:       coremetadata.SettingSourceProfile,
	}
	target, _ := store.registry.Agent("agt-beta-codex")
	target.Metadata.Annotations = maps.Clone(annotations)
	if _, err := personas.Write("lead", []byte("You lead one Task and review every diff twice.")); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := runRoute(t, command, "resume", "uid:agt-beta-codex")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "instructions lead were not applied ("+personaReasonCodexInstructionsImmutable+")") {
		t.Fatalf("stderr = %q, want the %s notice", stderr, personaReasonCodexInstructionsImmutable)
	}
	calls := splitWindowCalls(tmux)
	if len(calls) != 1 || !strings.Contains(strings.Join(calls[0], " "), "-c model_reasoning_effort=max resume") {
		t.Fatalf("split-window calls = %v, want one launch with the profile's effort", calls)
	}
	agent, _ := store.registry.Agent("agt-beta-codex")
	want := plusSources(withAnnotations(annotations, coremetadata.AnnotationAgentEffort, "max"),
		coremetadata.AnnotationAgentModelSource, coremetadata.SettingSourceProfile)
	if !maps.Equal(agent.Metadata.Annotations, want) {
		t.Fatalf("annotations = %v, want %v", agent.Metadata.Annotations, want)
	}
}

// relaunchProfileFixture is the relaunch fixture whose Agent follows the
// effort of profile `role`, recorded at the digest stale.
func relaunchProfileFixture(t *testing.T, stale string) (*personaAttachFixture, profile.Store) {
	t.Helper()
	f := newRelaunchFixture(t)
	paths, err := configPaths(f.planner.homeDir, f.planner.lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	f.setAnnotations(map[string]string{
		coremetadata.AnnotationAgentProfile:       "role",
		coremetadata.AnnotationAgentProfileDigest: stale,
		coremetadata.AnnotationAgentEffort:        "high",
		coremetadata.AnnotationAgentEffortSource:  coremetadata.SettingSourceProfile,
	})
	return f, profile.NewDefaultStore(paths)
}

// TestAPlainRelaunchRestartsAnAgentWhoseProfileChangedAndIsUnchangedOtherwise
// is acceptance 6: `agent relaunch <ref>` without flags resolves the Agent's
// layers now; after a profile edit it reports why and restarts it with the
// profile's effort, and once the Agent runs what its layers resolve to it
// reports unchanged.
func TestAPlainRelaunchRestartsAnAgentWhoseProfileChangedAndIsUnchangedOtherwise(t *testing.T) {
	f, profiles := relaunchProfileFixture(t, "sha256:stale")
	digest := writeCodexProfile(t, profiles, "role", "effort = \"max\"\n")

	stdout, _, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--dry-run", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"action":"relaunch","dryRun":true,"outcome":"would-restart","agentUID":"agt-alpha-codex","agentName":"codex","provider":"claude","phase":"Running","interaction":"idle","paneUID":"pan-alpha-codex","currentEffort":"high","restart":true,"confirmationRequired":false,"unchanged":false,` +
		`"currentSettings":{"profile":{"name":"role","digest":"sha256:stale","source":""},"instructions":{"value":"","source":"","profileValue":"","override":false},"model":{"value":"","source":"","profileValue":"","override":false},"effort":{"value":"high","source":"profile","profileValue":"max","override":false}},` +
		`"newSettings":{"profile":{"name":"role","digest":"` + digest + `","source":""},"instructions":{"value":"","source":"profile","profileValue":"","override":false},"model":{"value":"","source":"profile","profileValue":"","override":false},"effort":{"value":"max","source":"profile","profileValue":"max","override":false}},` +
		`"currentHost":"tmux","targetHost":"tmux","relaunchReasons":["profile-changed","effort-changed"]}` + "\n"
	if stdout != want {
		t.Fatalf("dry run JSON =\n%s\nwant\n%s", stdout, want)
	}
	if len(splitWindowCalls(f.tmux)) != 0 || len(f.deletes.killed) != 0 {
		t.Fatal("a dry run changed something")
	}

	stdout, _, err = runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "agent/codex relaunched from effort=high to effort=max; restarted on the same conversation") {
		t.Fatalf("relaunch stdout = %q", stdout)
	}
	after := f.assertRestartedOnTheSameConversation(t, personaAttachPane)
	tail := f.lastArgvTail(t)
	if index := slices.Index(tail, "--effort"); index < 0 || tail[index+1] != "max" || slices.Contains(tail, "--model") {
		t.Fatalf("relaunch exec tail = %q, want --effort max and no --model", tail)
	}
	wantAnnotations := plusSources(map[string]string{
		coremetadata.AnnotationAgentProfile:       "role",
		coremetadata.AnnotationAgentProfileDigest: digest,
		coremetadata.AnnotationAgentEffort:        "max",
	}, allFollowProfile...)
	if !maps.Equal(after.Metadata.Annotations, wantAnnotations) {
		t.Fatalf("annotations = %v, want %v", after.Metadata.Annotations, wantAnnotations)
	}

	// Now it runs what its layers resolve to: nothing to restart.
	f.setInteraction(coremetadata.InteractionIdle)
	calls := len(splitWindowCalls(f.tmux))
	stdout, _, err = runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"outcome":"unchanged"`) || !strings.Contains(stdout, `"restart":false`) ||
		!strings.HasSuffix(stdout, `"relaunchReasons":[]}`+"\n") || len(splitWindowCalls(f.tmux)) != calls {
		t.Fatalf("second plain relaunch = %s (split-window %d -> %d), want unchanged", stdout, calls, len(splitWindowCalls(f.tmux)))
	}
}
