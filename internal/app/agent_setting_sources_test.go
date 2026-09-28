package app

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
)

// plusSources is want with the setting source annotations keyValues names
// added: the annotation set of an Agent that records those values and where
// they came from. It never writes into want.
func plusSources(want map[string]string, keyValues ...string) map[string]string {
	out := maps.Clone(want)
	if out == nil {
		out = make(map[string]string, len(keyValues)/2)
	}
	for i := 0; i+1 < len(keyValues); i += 2 {
		out[keyValues[i]] = keyValues[i+1]
	}
	return out
}

var (
	profileFlag         = []string{coremetadata.AnnotationAgentProfileSource, coremetadata.SettingSourceFlag}
	profileInherited    = []string{coremetadata.AnnotationAgentProfileSource, coremetadata.SettingSourceInherited}
	instructionsFlag    = []string{coremetadata.AnnotationAgentInstructionsSource, coremetadata.SettingSourceFlag}
	instructionsInh     = []string{coremetadata.AnnotationAgentInstructionsSource, coremetadata.SettingSourceInherited}
	modelFlag           = []string{coremetadata.AnnotationAgentModelSource, coremetadata.SettingSourceFlag}
	effortFlag          = []string{coremetadata.AnnotationAgentEffortSource, coremetadata.SettingSourceFlag}
	effortInherited     = []string{coremetadata.AnnotationAgentEffortSource, coremetadata.SettingSourceInherited}
	modelEffortResume   = []string{coremetadata.AnnotationAgentModelSource, coremetadata.SettingSourceResume, coremetadata.AnnotationAgentEffortSource, coremetadata.SettingSourceResume}
	modelEffortRelaunch = []string{coremetadata.AnnotationAgentModelSource, coremetadata.SettingSourceRelaunch, coremetadata.AnnotationAgentEffortSource, coremetadata.SettingSourceRelaunch}
)

func sources(groups ...[]string) []string {
	var out []string
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}

// TestCreateRecordsWhereEachSettingCameFrom is Task acceptance 1 on the typed
// create: a profile named by --profile is flag, one a role label selected is
// role, an item the profile filled in is profile, and an item a flag gave is
// flag. The launch argv is the one the same create had before sources were
// recorded.
func TestCreateRecordsWhereEachSettingCameFrom(t *testing.T) {
	t.Parallel()
	t.Run("profile and effort flag", func(t *testing.T) {
		t.Parallel()
		f := newProfileFixture(t)
		instructions := []byte("You review Go code.\n")
		if _, err := f.personas.Write("go-reviewer", instructions); err != nil {
			t.Fatal(err)
		}
		digest := f.writeProfile(t, "guard", guardProfile)
		if _, stderr, err := f.createClaude(t, "--profile", "guard", "--effort", "low"); err != nil {
			t.Fatalf("create: %v (stderr=%q)", err, stderr)
		}
		snapshot, _ := f.personas.SnapshotPath(persona.Digest(instructions))
		wantArgv := []string{"--model", "opus", "--effort", "low", "--append-system-prompt-file", snapshot,
			"--settings", f.settingsPath(t, guardPermissions), "--", "review this"}
		if got := f.onlyArgvTail(t, aiModeClaude); !slices.Equal(got, wantArgv) {
			t.Fatalf("exec argv tail = %q, want %q", got, wantArgv)
		}
		want := map[string]string{
			coremetadata.AnnotationAgentPersona:            "go-reviewer",
			coremetadata.AnnotationAgentPersonaDigest:      persona.Digest(instructions),
			coremetadata.AnnotationAgentEffort:             "low",
			coremetadata.AnnotationAgentModel:              "opus",
			coremetadata.AnnotationAgentProfile:            "guard",
			coremetadata.AnnotationAgentProfileDigest:      digest,
			coremetadata.AnnotationAgentProfileSource:      coremetadata.SettingSourceFlag,
			coremetadata.AnnotationAgentInstructionsSource: coremetadata.SettingSourceProfile,
			coremetadata.AnnotationAgentModelSource:        coremetadata.SettingSourceProfile,
			coremetadata.AnnotationAgentEffortSource:       coremetadata.SettingSourceFlag,
		}
		if got := f.createdAgent(t).Metadata.Annotations; !maps.Equal(got, want) {
			t.Fatalf("Agent annotations = %v, want %v", got, want)
		}
	})
	t.Run("role label", func(t *testing.T) {
		t.Parallel()
		f := newProfileFixture(t)
		digest := f.writeProfile(t, "mapped", "roles = [\"reviewer\"]\nmodel = \"opus\"\n")
		if _, stderr, err := f.createClaude(t, "--label", "role=reviewer"); err != nil {
			t.Fatalf("create: %v (stderr=%q)", err, stderr)
		}
		want := map[string]string{
			coremetadata.AnnotationAgentModel:         "opus",
			coremetadata.AnnotationAgentProfile:       "mapped",
			coremetadata.AnnotationAgentProfileDigest: digest,
			coremetadata.AnnotationAgentProfileSource: coremetadata.SettingSourceRole,
			coremetadata.AnnotationAgentModelSource:   coremetadata.SettingSourceProfile,
		}
		if got := f.createdAgent(t).Metadata.Annotations; !maps.Equal(got, want) {
			t.Fatalf("Agent annotations = %v, want %v", got, want)
		}
	})
	t.Run("model without a profile", func(t *testing.T) {
		t.Parallel()
		f := newProfileFixture(t)
		if _, stderr, err := f.createClaude(t, "--model", "haiku"); err != nil {
			t.Fatalf("create: %v (stderr=%q)", err, stderr)
		}
		want := map[string]string{
			coremetadata.AnnotationAgentModel:       "haiku",
			coremetadata.AnnotationAgentModelSource: coremetadata.SettingSourceFlag,
		}
		if got := f.createdAgent(t).Metadata.Annotations; !maps.Equal(got, want) {
			t.Fatalf("Agent annotations = %v, want only the model and its source %v", got, want)
		}
	})
}

// TestCreateSettingSourcesFollowTheValues pins the helper both create
// surfaces call: a create that records none of the values returns the base
// map itself -- nil included -- and never writes into it.
func TestCreateSettingSourcesFollowTheValues(t *testing.T) {
	t.Parallel()
	if got := withCreateSettingSources(resourceCreateFlags{}, nil); got != nil {
		t.Fatalf("no values = %v, want nil", got)
	}
	base := map[string]string{coremetadata.AnnotationAgentTopic: "t"}
	if got := withCreateSettingSources(resourceCreateFlags{}, base); len(got) != 1 {
		t.Fatalf("no values = %v, want the base", got)
	}
	got := withCreateSettingSources(resourceCreateFlags{effort: "low"}, base)
	if len(base) != 1 || !maps.Equal(got, plusSources(base, effortFlag...)) {
		t.Fatalf("effort flag = %v (base %v)", got, base)
	}
	if got := withInheritedSettingSources(nil, nil); got != nil {
		t.Fatalf("nothing inherited = %v, want nil", got)
	}
}

// TestAgentRelaunchRecordsRelaunchOverAProfileSource is acceptance 2 on
// relaunch: a relaunch replaces the source of every value it writes -- even a
// value equal to the one the profile gave -- and leaves the source of a value
// it does not write, while an `unchanged` relaunch writes nothing at all.
func TestAgentRelaunchRecordsRelaunchOverAProfileSource(t *testing.T) {
	f := newRelaunchFixture(t)
	recorded := map[string]string{
		coremetadata.AnnotationAgentModel:        "opus",
		coremetadata.AnnotationAgentModelSource:  coremetadata.SettingSourceProfile,
		coremetadata.AnnotationAgentEffort:       "high",
		coremetadata.AnnotationAgentEffortSource: coremetadata.SettingSourceProfile,
	}
	f.setAnnotations(maps.Clone(recorded))
	if _, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--effort", "high"); err != nil {
		t.Fatalf("unchanged relaunch: stderr=%q err=%v", stderr, err)
	}
	if got := f.agent(t).Metadata.Annotations; !maps.Equal(got, recorded) {
		t.Fatalf("an unchanged relaunch wrote %v, want %v", got, recorded)
	}

	if _, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--model", "haiku"); err != nil {
		t.Fatalf("relaunch: stderr=%q err=%v", stderr, err)
	}
	after := f.assertRestartedOnTheSameConversation(t, personaAttachPane)
	want := plusSources(recorded, coremetadata.AnnotationAgentModel, "haiku", coremetadata.AnnotationAgentModelSource, coremetadata.SettingSourceRelaunch)
	if !maps.Equal(after.Metadata.Annotations, want) {
		t.Fatalf("annotations = %v, want %v", after.Metadata.Annotations, want)
	}
	if got := f.lastArgvTail(t); !slices.Equal(got, []string{"--model", "haiku", "--effort", "high", "--resume", personaResumeConversation}) {
		t.Fatalf("relaunch exec argv tail = %q", got)
	}
}

// TestAFailedOverrideResumeKeepsTheRecordedSources is acceptance 4 on the
// rebind: a resume --model/--effort whose launch fails rolls the sources back
// with the values, so the Agent keeps the sources it recorded before.
func TestAFailedOverrideResumeKeepsTheRecordedSources(t *testing.T) {
	store := newFakeResourceStore(t)
	target, _ := store.registry.Agent("agt-beta-codex")
	target.Spec.Provider = aiModeClaude
	target.Status.SessionRef = claudeConversationRef(personaResumeConversation)
	recorded := plusSources(modelEffortAnnotations("haiku", "low"), coremetadata.AnnotationAgentModelSource, coremetadata.SettingSourceFlag,
		coremetadata.AnnotationAgentEffortSource, coremetadata.SettingSourceProfile)
	target.Metadata.Annotations = maps.Clone(recorded)
	tmux := newFakeTmux()
	tmux.fail = []string{"split-window"}
	tmux.failMessage = "no space for new pane"
	command, recorder, _, _ := newTestAgentResumeCommand(t, store, tmux)
	command.rebind.launcher = &exactArgvResumeLauncher{fakeResumeLauncher: recorder, planner: agentLaunchArgvTestCommand(t)}
	before := store.snapshot()

	if _, _, err := runRoute(t, command, "resume", "uid:agt-beta-codex", "--model", "sonnet", "--effort", "max"); err == nil {
		t.Fatal("resume succeeded despite a failing split")
	}
	if store.writes != 0 || store.snapshot() != before {
		t.Fatalf("a rolled-back override resume committed %d writes", store.writes)
	}
	if agent, _ := store.registry.Agent("agt-beta-codex"); !maps.Equal(agent.Metadata.Annotations, recorded) {
		t.Fatalf("Agent annotations = %v, want %v", agent.Metadata.Annotations, recorded)
	}
}

// TestAgentRelaunchResumeFailureKeepsTheRecordedSources is acceptance 4 on
// relaunch: the resume after the stop fails, and the Agent keeps its previous
// values and sources.
func TestAgentRelaunchResumeFailureKeepsTheRecordedSources(t *testing.T) {
	f := newRelaunchFixture(t)
	recorded := plusSources(effortAnnotations("high"), coremetadata.AnnotationAgentEffortSource, coremetadata.SettingSourceProfile)
	f.setAnnotations(maps.Clone(recorded))
	f.command.rebind.launcher = &failingRelaunchResumeLauncher{exactArgvResumeLauncher: f.launcher}
	if _, _, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--model", "opus", "--effort", "max"); err == nil {
		t.Fatal("relaunch with a failing resume succeeded")
	}
	if got := f.agent(t).Metadata.Annotations; !maps.Equal(got, recorded) {
		t.Fatalf("annotations after a failed resume = %v, want %v", got, recorded)
	}
}

// TestAgentPersonaStopFailureRestoresTheInstructionsSource is acceptance 4 on
// attach and detach: a stop that fails with the old session alive restores the
// instructions source together with the persona, and an attach of the
// persona and digest already running is `unchanged` whatever the source.
func TestAgentPersonaStopFailureRestoresTheInstructionsSource(t *testing.T) {
	for _, action := range []string{"attach", "detach"} {
		t.Run(action, func(t *testing.T) {
			f := newPersonaAttachFixture(t)
			f.writePersona(t, "go-reviewer", personaResumeContent)
			f.writePersona(t, "other", "another persona\n")
			recorded := map[string]string{
				coremetadata.AnnotationAgentPersona:            "other",
				coremetadata.AnnotationAgentPersonaDigest:      persona.Digest([]byte("another persona\n")),
				coremetadata.AnnotationAgentInstructionsSource: coremetadata.SettingSourceProfile,
			}
			f.setAnnotations(maps.Clone(recorded))
			f.deletes.killErr = errors.New("tmux kill-pane failed")
			args := []string{"persona", action, "uid:" + personaAttachAgent}
			if action == "attach" {
				args = append(args, "go-reviewer")
			}
			if _, _, err := runRoute(t, f.command, args...); err == nil || !strings.Contains(err.Error(), "persona annotations were restored") {
				t.Fatalf("%s with a failing stop err = %v", action, err)
			}
			if got := f.agent(t).Metadata.Annotations; !maps.Equal(got, recorded) {
				t.Fatalf("annotations after a failed stop = %v, want %v", got, recorded)
			}
		})
	}

	f := newPersonaAttachFixture(t)
	f.writePersona(t, "go-reviewer", personaResumeContent)
	running := map[string]string{
		coremetadata.AnnotationAgentPersona:              "go-reviewer",
		coremetadata.AnnotationAgentPersonaDigest:        persona.Digest([]byte(personaResumeContent)),
		coremetadata.AnnotationAgentSystemPromptSnapshot: coremetadata.SystemPromptSnapshotOff,
		coremetadata.AnnotationAgentInstructionsSource:   coremetadata.SettingSourceProfile,
	}
	f.setAnnotations(maps.Clone(running))
	stdout, _, err := runRoute(t, f.command, "persona", "attach", "uid:"+personaAttachAgent, "go-reviewer")
	if err != nil || !strings.Contains(stdout, "unchanged") {
		t.Fatalf("attach of the running persona: stdout=%q err=%v, want unchanged", stdout, err)
	}
	if got := f.agent(t).Metadata.Annotations; !maps.Equal(got, running) {
		t.Fatalf("an unchanged attach wrote %v, want %v", got, running)
	}
}

// TestResumePickerHoldersThatDifferOnlyInSourcesStillAgree is acceptance 3's
// agreement half: the source keys are not part of the inherited bundle, so
// holders whose values agree but whose sources differ still give the new
// Agent their values -- recorded as inherited -- with no ambiguity notice;
// and the profile pair is inherited on the same terms.
func TestResumePickerHoldersThatDifferOnlyInSourcesStillAgree(t *testing.T) {
	t.Parallel()
	planner := agentLaunchArgvTestCommand(t)
	personaAnnotations, snapshot := createPersonaForResume(t, planner, "go-reviewer", []byte(personaResumeContent))
	bundle := claudeLaunchBundle(personaAnnotations, "low")
	fromFlag := plusSources(bundle, sources(instructionsFlag, effortFlag)...)
	fromAttach := plusSources(bundle, coremetadata.AnnotationAgentInstructionsSource, coremetadata.SettingSourceAttach,
		coremetadata.AnnotationAgentEffortSource, coremetadata.SettingSourceRelaunch)

	registry := coremetadata.NewRegistry()
	for i, annotations := range []map[string]string{fromFlag, fromAttach} {
		uid := []string{"agt-a", "agt-b"}[i]
		ref, _ := coremetadata.NewAgentSessionRef(pickerResumeSessionObservation(aiModeClaude, personaResumeConversation), resourceFixtureClock)
		registry.Agents = append(registry.Agents, coremetadata.Agent{
			Metadata: coremetadata.ObjectMeta{UID: uid, Name: uid, Annotations: plusSources(annotations,
				coremetadata.AnnotationAgentProfile, "guard", coremetadata.AnnotationAgentProfileDigest, "sha256:0",
				coremetadata.AnnotationAgentProfileSource, []string{coremetadata.SettingSourceFlag, coremetadata.SettingSourceRole}[i]),
				OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: "win-a"}},
			Status: coremetadata.AgentStatus{SessionRef: ref},
		})
	}
	got, notice := inheritedResumeLaunchValues(&registry, aiModeClaude, personaResumeConversation)
	if !maps.Equal(got, bundle) || notice != "" {
		t.Fatalf("inherited %v notice %q, want %v and no notice", got, notice, bundle)
	}
	profilePair, err := inheritedResumeProfile(&registry, aiModeClaude, personaResumeConversation)
	if err != nil || !maps.Equal(profilePair, profileAnnotations("guard", "sha256:0")) {
		t.Fatalf("inherited profile %v, %v", profilePair, err)
	}

	f := newPickerLaunchValuesFixture(t, planner)
	f.hold(t, "agt-alpha-codex", aiModeClaude, personaResumeConversation, fromFlag, nil)
	f.hold(t, "agt-beta-codex", aiModeClaude, personaResumeConversation, fromAttach, nil)
	agent, argv, stderr := f.pick(t, false, aiModeClaude, personaResumeConversation, "")
	wantArgv := []string{"--effort", "low", "--append-system-prompt-file", snapshot,
		"--system-prompt-snapshot", "off", "--resume", personaResumeConversation}
	if got := execArgvTail(t, argv, aiModeClaude); !slices.Equal(got, wantArgv) {
		t.Fatalf("exec argv tail = %q, want %q", got, wantArgv)
	}
	if want := plusSources(bundle, sources(instructionsInh, effortInherited)...); !maps.Equal(agent.Metadata.Annotations, want) || stderr != "" {
		t.Fatalf("new Agent annotations = %v stderr = %q, want %v and nothing", agent.Metadata.Annotations, stderr, want)
	}
}
