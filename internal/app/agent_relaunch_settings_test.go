package app

import (
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

const (
	relaunchLeadInstructions   = "You lead one Task and delegate the code."
	relaunchReviewInstructions = "You review every diff twice."
)

// relaunchLayersFixture is the relaunch fixture with a profile store and an
// instructions store in the resume seam's home: instructions lead and
// reviewer, profile role (lead, opus, high) and profile review (reviewer,
// sonnet, a Claude allow rule).
type relaunchLayersFixture struct {
	*personaAttachFixture
	profiles   profile.Store
	personas   persona.Store
	roleDigest string
	reviewDig  string
	leadDigest string
	reviewText string
}

func newRelaunchLayersFixture(t *testing.T) relaunchLayersFixture {
	t.Helper()
	f := newRelaunchFixture(t)
	turnAgentGuidanceOff(t, f.planner)
	paths, err := configPaths(f.planner.homeDir, f.planner.lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	out := relaunchLayersFixture{personaAttachFixture: f, profiles: profile.NewDefaultStore(paths), personas: persona.NewDefaultStore(paths)}
	lead, err := out.personas.Write("lead", []byte(relaunchLeadInstructions))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := out.personas.Write("reviewer", []byte(relaunchReviewInstructions)); err != nil {
		t.Fatal(err)
	}
	out.leadDigest, out.reviewText = lead.Digest, persona.Digest([]byte(relaunchReviewInstructions))
	out.roleDigest = writeCodexProfile(t, out.profiles, "role", "instructions = \"lead\"\nmodel = \"opus\"\neffort = \"high\"\n")
	out.reviewDig = writeCodexProfile(t, out.profiles, "review", "instructions = \"reviewer\"\nmodel = \"sonnet\"\n[permissions]\nallow = [\"Read\"]\n")
	return out
}

// onRoleWithOverrides makes the fixture Agent one created from profile role
// with an explicit model and a relaunched effort over it.
func (f relaunchLayersFixture) onRoleWithOverrides(t *testing.T) map[string]string {
	t.Helper()
	if _, err := f.personas.WriteSnapshot([]byte(relaunchLeadInstructions)); err != nil {
		t.Fatal(err)
	}
	annotations := map[string]string{
		coremetadata.AnnotationAgentProfile:            "role",
		coremetadata.AnnotationAgentProfileDigest:      f.roleDigest,
		coremetadata.AnnotationAgentProfileSource:      coremetadata.SettingSourceRole,
		coremetadata.AnnotationAgentPersona:            "lead",
		coremetadata.AnnotationAgentPersonaDigest:      f.leadDigest,
		coremetadata.AnnotationAgentInstructionsSource: coremetadata.SettingSourceProfile,
		coremetadata.AnnotationAgentModel:              "haiku",
		coremetadata.AnnotationAgentModelSource:        coremetadata.SettingSourceFlag,
		coremetadata.AnnotationAgentEffort:             "low",
		coremetadata.AnnotationAgentEffortSource:       coremetadata.SettingSourceRelaunch,
	}
	f.setAnnotations(maps.Clone(annotations))
	return annotations
}

// assertNothingChangedButTheSnapshot is assertRelaunchNothingChanged for a
// fixture whose Agent started with an instructions snapshot: the snapshot
// directory holds exactly that one.
func (f relaunchLayersFixture) assertNothingChangedButTheSnapshot(t *testing.T, before string, beforeAnnotations map[string]string) {
	t.Helper()
	if files := f.snapshotFiles(t); len(files) != 1 {
		t.Fatalf("an unchanged or refused relaunch wrote snapshots: %v", files)
	}
	if f.store.writes != 0 || f.store.transactions != 0 || f.store.snapshot() != before {
		t.Fatalf("an unchanged or refused relaunch changed the registry: writes=%d transactions=%d", f.store.writes, f.store.transactions)
	}
	if got := f.agent(t).Metadata.Annotations; !maps.Equal(got, beforeAnnotations) {
		t.Fatalf("annotations = %v, want %v", got, beforeAnnotations)
	}
	if f.deletes.preflights != 0 || len(f.deletes.killed) != 0 || len(splitWindowCalls(f.tmux)) != 0 {
		t.Fatalf("an unchanged or refused relaunch stopped or launched: preflights=%d killed=%+v", f.deletes.preflights, f.deletes.killed)
	}
}

// argvValue is the value after flag in tail, "" when the flag is absent.
func argvValue(tail []string, flag string) string {
	if index := slices.Index(tail, flag); index >= 0 && index+1 < len(tail) {
		return tail[index+1]
	}
	return ""
}

// TestARelaunchGivesAnAgentWithoutAProfileItsProfile is Task 3 acceptance 3,
// the portfolio case: an Agent created before profiles gets one with
// `relaunch --profile`, restarted once on the same conversation with the
// profile's instructions, model, effort and permissions, and records the
// profile as a relaunch and every item as the profile's.
func TestARelaunchGivesAnAgentWithoutAProfileItsProfile(t *testing.T) {
	f := newRelaunchLayersFixture(t)

	stdout, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--profile", "role")
	if err != nil {
		t.Fatalf("relaunch --profile: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if want := "agent/codex relaunched from effort=high profile=none instructions=none to effort=high model=opus profile=role instructions=lead; restarted on the same conversation\n"; !strings.HasSuffix(stdout, want) {
		t.Fatalf("stdout = %q, want suffix %q", stdout, want)
	}
	after := f.assertRestartedOnTheSameConversation(t, personaAttachPane)
	tail := f.lastArgvTail(t)
	snapshot := f.snapshotPath(t, relaunchLeadInstructions)
	if argvValue(tail, "--model") != "opus" || argvValue(tail, "--effort") != "high" || argvValue(tail, "--append-system-prompt-file") != snapshot ||
		argvValue(tail, "--system-prompt-snapshot") != "off" || argvValue(tail, "--resume") != personaResumeConversation {
		t.Fatalf("relaunch exec tail = %q, want the role profile's model, effort and instructions on the same conversation", tail)
	}
	want := map[string]string{
		coremetadata.AnnotationAgentProfile:              "role",
		coremetadata.AnnotationAgentProfileDigest:        f.roleDigest,
		coremetadata.AnnotationAgentProfileSource:        coremetadata.SettingSourceRelaunch,
		coremetadata.AnnotationAgentPersona:              "lead",
		coremetadata.AnnotationAgentPersonaDigest:        f.leadDigest,
		coremetadata.AnnotationAgentSystemPromptSnapshot: coremetadata.SystemPromptSnapshotOff,
		coremetadata.AnnotationAgentInstructionsSource:   coremetadata.SettingSourceProfile,
		coremetadata.AnnotationAgentModel:                "opus",
		coremetadata.AnnotationAgentModelSource:          coremetadata.SettingSourceProfile,
		coremetadata.AnnotationAgentEffort:               "high",
		coremetadata.AnnotationAgentEffortSource:         coremetadata.SettingSourceProfile,
	}
	if !maps.Equal(after.Metadata.Annotations, want) {
		t.Fatalf("annotations = %v, want %v", after.Metadata.Annotations, want)
	}

	// The next plain relaunch finds nothing to change.
	stdout, _, err = runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent)
	if err != nil || stdout != "agent/codex unchanged: already running with effort=high\n" {
		t.Fatalf("plain relaunch after the switch = %q, %v; want unchanged", stdout, err)
	}
}

// TestARelaunchSwitchesTheProfileAndOverridesInOneRestart is Task 3
// acceptance 1: `--profile review --effort max` drops the Agent's overrides,
// takes the review profile's layer (its permissions included), and overrides
// only the effort, all in one restart.
func TestARelaunchSwitchesTheProfileAndOverridesInOneRestart(t *testing.T) {
	f := newRelaunchLayersFixture(t)
	f.onRoleWithOverrides(t)

	stdout, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--profile", "review", "--effort", "max")
	if err != nil {
		t.Fatalf("relaunch: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if want := "agent/codex relaunched from effort=low profile=role instructions=lead to effort=max model=sonnet profile=review instructions=reviewer; restarted on the same conversation\n"; !strings.HasSuffix(stdout, want) {
		t.Fatalf("stdout = %q, want suffix %q", stdout, want)
	}
	after := f.assertRestartedOnTheSameConversation(t, personaAttachPane)
	tail := f.lastArgvTail(t)
	if argvValue(tail, "--model") != "sonnet" || argvValue(tail, "--effort") != "max" ||
		argvValue(tail, "--append-system-prompt-file") != f.snapshotPath(t, relaunchReviewInstructions) || argvValue(tail, "--settings") == "" {
		t.Fatalf("relaunch exec tail = %q, want review's model, instructions and permissions with effort max", tail)
	}
	want := map[string]string{
		coremetadata.AnnotationAgentProfile:              "review",
		coremetadata.AnnotationAgentProfileDigest:        f.reviewDig,
		coremetadata.AnnotationAgentProfileSource:        coremetadata.SettingSourceRelaunch,
		coremetadata.AnnotationAgentPersona:              "reviewer",
		coremetadata.AnnotationAgentPersonaDigest:        f.reviewText,
		coremetadata.AnnotationAgentSystemPromptSnapshot: coremetadata.SystemPromptSnapshotOff,
		coremetadata.AnnotationAgentInstructionsSource:   coremetadata.SettingSourceProfile,
		coremetadata.AnnotationAgentModel:                "sonnet",
		coremetadata.AnnotationAgentModelSource:          coremetadata.SettingSourceProfile,
		coremetadata.AnnotationAgentEffort:               "max",
		coremetadata.AnnotationAgentEffortSource:         coremetadata.SettingSourceRelaunch,
	}
	if !maps.Equal(after.Metadata.Annotations, want) {
		t.Fatalf("annotations = %v, want %v", after.Metadata.Annotations, want)
	}
}

// TestARelaunchToTheSameProfileKeepsItsOverridesAndAResetRemovesOne is Task 3
// acceptance 2: naming the profile the Agent has keeps its overrides and
// changes nothing, and `--reset effort` puts only the effort back in the
// profile layer.
func TestARelaunchToTheSameProfileKeepsItsOverridesAndAResetRemovesOne(t *testing.T) {
	f := newRelaunchLayersFixture(t)
	annotations := f.onRoleWithOverrides(t)
	before := f.store.snapshot()

	stdout, _, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--profile", "role", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var result agentRelaunchResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != personaOutcomeUnchanged || result.NewSettings != result.CurrentSettings || len(result.RelaunchReasons) != 0 {
		t.Fatalf("same-profile relaunch = %+v, want unchanged with its overrides", result)
	}
	f.assertNothingChangedButTheSnapshot(t, before, annotations)

	stdout, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--reset", "effort")
	if err != nil {
		t.Fatalf("relaunch --reset effort: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	after := f.assertRestartedOnTheSameConversation(t, personaAttachPane)
	tail := f.lastArgvTail(t)
	if argvValue(tail, "--effort") != "high" || slices.Contains(tail, "--model") {
		t.Fatalf("reset exec tail = %q, want the profile's effort and the model left to the conversation", tail)
	}
	want := withAnnotations(annotations, coremetadata.AnnotationAgentEffort, "high", coremetadata.AnnotationAgentEffortSource, coremetadata.SettingSourceProfile)
	if !maps.Equal(after.Metadata.Annotations, want) {
		t.Fatalf("annotations = %v, want %v", after.Metadata.Annotations, want)
	}

	// A model override whose value the profile shares is still an override
	// until it is reset: the reset records the layer, with one restart.
	f.setInteraction(coremetadata.InteractionIdle)
	f.setAnnotations(withAnnotations(after.Metadata.Annotations, coremetadata.AnnotationAgentModel, "opus"))
	calls := len(splitWindowCalls(f.tmux))
	if _, _, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--reset", "model"); err != nil {
		t.Fatal(err)
	}
	if got := len(splitWindowCalls(f.tmux)); got != calls+1 || slices.Contains(f.lastArgvTail(t), "--model") {
		t.Fatalf("reset model: %d launches (was %d), tail %q; want one launch without --model", got, calls, f.lastArgvTail(t))
	}
	if source := f.agent(t).Metadata.Annotations[coremetadata.AnnotationAgentModelSource]; source != coremetadata.SettingSourceProfile {
		t.Fatalf("model source after reset = %q, want profile", source)
	}
}

// TestARelaunchToNoProfileDropsTheProfileAndEveryOverride is `--profile
// none`: no permissions, instructions, model or effort are passed, and the
// Agent records none of them.
func TestARelaunchToNoProfileDropsTheProfileAndEveryOverride(t *testing.T) {
	f := newRelaunchLayersFixture(t)
	f.onRoleWithOverrides(t)

	if _, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--profile", "none"); err != nil {
		t.Fatalf("relaunch --profile none: %v (%s)", err, stderr)
	}
	after := f.assertRestartedOnTheSameConversation(t, personaAttachPane)
	want := []string{"--system-prompt-snapshot", "off", "--resume", personaResumeConversation}
	if got := f.lastArgvTail(t); !slices.Equal(got, want) {
		t.Fatalf("relaunch exec tail = %q, want %q", got, want)
	}
	if want := map[string]string{coremetadata.AnnotationAgentSystemPromptSnapshot: coremetadata.SystemPromptSnapshotOff}; !maps.Equal(after.Metadata.Annotations, want) {
		t.Fatalf("annotations = %v, want %v", after.Metadata.Annotations, want)
	}
}

// TestARelaunchWithInstructionsDoesWhatAnAttachDoes is Task 3 acceptance 5:
// `agent instructions attach` and `detach` are `relaunch --instructions` and
// `--instructions none`. The launch and the record are the same; only the
// source says which command gave them.
func TestARelaunchWithInstructionsDoesWhatAnAttachDoes(t *testing.T) {
	for _, test := range []struct {
		name     string
		attach   []string
		relaunch []string
	}{
		{"attach", []string{"instructions", "attach", "uid:" + personaAttachAgent, "reviewer"}, []string{"relaunch", "uid:" + personaAttachAgent, "--instructions", "reviewer"}},
		{"detach", []string{"instructions", "detach", "uid:" + personaAttachAgent}, []string{"relaunch", "uid:" + personaAttachAgent, "--instructions", "none"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := func(args []string) (coremetadata.Agent, []string) {
				f := newRelaunchLayersFixture(t)
				f.onRoleWithOverrides(t)
				if _, stderr, err := runRoute(t, f.command, args...); err != nil {
					t.Fatalf("%q: %v (%s)", args, err, stderr)
				}
				after := f.assertRestartedOnTheSameConversation(t, personaAttachPane)
				// The two runs have two homes: compare the snapshot by name.
				tail := f.lastArgvTail(t)
				if index := slices.Index(tail, "--append-system-prompt-file"); index >= 0 {
					tail[index+1] = filepath.Base(tail[index+1])
				}
				return after, tail
			}
			attached, attachTail := run(test.attach)
			relaunched, relaunchTail := run(test.relaunch)
			if !slices.Equal(attachTail, relaunchTail) {
				t.Fatalf("attach launched %q, relaunch %q; want the same launch", attachTail, relaunchTail)
			}
			if got := attached.Metadata.Annotations[coremetadata.AnnotationAgentInstructionsSource]; got != coremetadata.SettingSourceAttach {
				t.Fatalf("attach instructions source = %q", got)
			}
			if got := relaunched.Metadata.Annotations[coremetadata.AnnotationAgentInstructionsSource]; got != coremetadata.SettingSourceRelaunch {
				t.Fatalf("relaunch instructions source = %q", got)
			}
			if !maps.Equal(withAnnotations(attached.Metadata.Annotations, coremetadata.AnnotationAgentInstructionsSource, ""),
				withAnnotations(relaunched.Metadata.Annotations, coremetadata.AnnotationAgentInstructionsSource, "")) {
				t.Fatalf("attach recorded %v, relaunch %v; want the same but the source", attached.Metadata.Annotations, relaunched.Metadata.Annotations)
			}
		})
	}
}

// TestARelaunchThatChangesTheLayersPrintsTheRelaunchToRecover pins the
// recovery of a switch whose resume failed: the Agent is Offline with what it
// recorded before, and the same `agent relaunch`, which `agent resume` cannot
// stand for, finishes it.
func TestARelaunchThatChangesTheLayersPrintsTheRelaunchToRecover(t *testing.T) {
	f := newRelaunchLayersFixture(t)
	annotations := f.onRoleWithOverrides(t)
	working := f.command.rebind.launcher
	f.command.rebind.launcher = &failingRelaunchResumeLauncher{exactArgvResumeLauncher: f.launcher}

	_, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--profile", "review", "--reset", "effort")
	if err == nil || !strings.Contains(err.Error(), "with its previous settings and needs `agent relaunch`") {
		t.Fatalf("relaunch with a failing resume err = %v", err)
	}
	recovery := "projmux agent relaunch " + relaunchAgentRef + " --profile review --reset effort"
	if !strings.Contains(stderr, "projmux: recover with: "+recovery+"\n") {
		t.Fatalf("stderr = %q, want the recovery command %q", stderr, recovery)
	}
	if after := f.agent(t); after.Status.Phase != coremetadata.PhaseOffline || !maps.Equal(after.Metadata.Annotations, annotations) {
		t.Fatalf("agent after a failed resume = %s %v, want Offline with its previous annotations", after.Status.Phase, after.Metadata.Annotations)
	}
	f.command.rebind.launcher = working
	if _, stderr, err := runRoute(t, f.command, strings.Fields(strings.TrimPrefix(recovery, "projmux agent "))...); err != nil {
		t.Fatalf("recovery: %v (%s)", err, stderr)
	}
	if after := f.agent(t); after.Status.Phase != coremetadata.PhaseRunning || after.Metadata.Annotations[coremetadata.AnnotationAgentProfile] != "review" {
		t.Fatalf("agent after the recovery = %s %v", after.Status.Phase, after.Metadata.Annotations)
	}
}

// TestARelaunchProfileDryRunJSONReportsBothSides is the relaunch golden of a
// switch: the Task 2 shape, with the new profile, its items and the one
// override on the new side.
func TestARelaunchProfileDryRunJSONReportsBothSides(t *testing.T) {
	f := newRelaunchLayersFixture(t)
	annotations := f.onRoleWithOverrides(t)
	before := f.store.snapshot()

	stdout, _, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--profile", "review", "--effort", "max", "--dry-run", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"action":"relaunch","dryRun":true,"outcome":"would-restart","agentUID":"agt-alpha-codex","agentName":"codex","provider":"claude","phase":"Running","interaction":"idle","paneUID":"pan-alpha-codex","currentEffort":"low","currentModel":"haiku","newEffort":"max","restart":true,"confirmationRequired":false,"unchanged":false,` +
		`"currentSettings":{"profile":{"name":"role","digest":"` + f.roleDigest + `","source":"role"},"instructions":{"value":"lead","source":"profile","profileValue":"lead","override":false},"model":{"value":"haiku","source":"flag","profileValue":"opus","override":true},"effort":{"value":"low","source":"relaunch","profileValue":"high","override":true}},` +
		`"newSettings":{"profile":{"name":"review","digest":"` + f.reviewDig + `","source":"relaunch"},"instructions":{"value":"reviewer","source":"profile","profileValue":"reviewer","override":false},"model":{"value":"sonnet","source":"profile","profileValue":"sonnet","override":false},"effort":{"value":"max","source":"relaunch","profileValue":"","override":true}},` +
		`"currentHost":"tmux","targetHost":"tmux","relaunchReasons":["profile-changed","instructions-changed","model-changed","effort-changed"]}` + "\n"
	if stdout != want {
		t.Fatalf("dry run JSON =\n%s\nwant\n%s", stdout, want)
	}
	f.assertNothingChangedButTheSnapshot(t, before, annotations)
}

// TestARelaunchLayerChangeRefusalsLeaveNoTrace is the Task 3 refusal table:
// every change to the layers that cannot be made is refused before the stop,
// with its reason, and changes nothing.
func TestARelaunchLayerChangeRefusalsLeaveNoTrace(t *testing.T) {
	for _, test := range []struct {
		name    string
		flags   []string
		arrange func(relaunchLayersFixture)
		want    string
		usage   bool
	}{
		{name: "missing profile", flags: []string{"--profile", "absent"}, want: profile.ReasonNotFound},
		{name: "profile for another provider", flags: []string{"--profile", "coder"}, want: profileReasonProviderMismatch, arrange: func(f relaunchLayersFixture) {
			writeCodexProfile(t, f.profiles, "coder", "provider = \"codex\"\n")
		}},
		{name: "invalid profile", flags: []string{"--profile", "broken"}, want: profile.ReasonKeyUnknown, arrange: func(f relaunchLayersFixture) {
			writeCodexProfileFile(t, f.profiles, "broken", "colour = \"blue\"\n")
		}},
		{name: "missing instructions", flags: []string{"--instructions", "absent"}, want: persona.ReasonNotFound},
		{name: "reset with a value for the same item", flags: []string{"--effort", "max", "--reset", "effort"}, want: "--effort sets the effort and --reset removes its override", usage: true},
		{name: "reset all with a model", flags: []string{"--model", "opus", "--reset", "all"}, want: "--model sets the model and --reset removes its override", usage: true},
		{name: "reset of an unknown item", flags: []string{"--reset", "permissions"}, want: `--reset "permissions" is not an item`, usage: true},
		{name: "invalid profile name", flags: []string{"--profile", "../role"}, want: profile.ReasonNameInvalid, usage: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newRelaunchLayersFixture(t)
			annotations := f.onRoleWithOverrides(t)
			if test.arrange != nil {
				test.arrange(f)
			}
			before := f.store.snapshot()
			stdout, _, err := runRoute(t, f.command, append([]string{"relaunch", "uid:" + personaAttachAgent}, test.flags...)...)
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.HasSuffix(err.Error(), "nothing was changed") {
				t.Fatalf("relaunch %q err = %v, want a refusal carrying %q", test.flags, err, test.want)
			}
			if !test.usage && !IsUsageError(err) {
				t.Fatalf("refusal %v is not a usage error", err)
			}
			if stdout != "" {
				t.Fatalf("refused relaunch printed %q", stdout)
			}
			f.assertNothingChangedButTheSnapshot(t, before, annotations)
		})
	}
}

// codexRelaunchFixture is the relaunch fixture made a Codex Agent on a native
// thread, on profile guard (instructions lead, workspace-write sandbox,
// on-request approval), with profile stores in the create home a Codex rebind
// reads.
func codexRelaunchFixture(t *testing.T) (*personaAttachFixture, profile.Store, *fakeNativeThreadController, map[string]string) {
	t.Helper()
	f := newRelaunchFixture(t)
	route := nativeTestRoute("generation-relaunch", coremetadata.CodexGenerationCurrent)
	ref := nativeTestSessionRef(route, resumeFixtureConversation)
	ref.ObservedAt = resourceFixtureClock
	agent, _ := f.store.registry.Agent(personaAttachAgent)
	agent.Spec.Provider = aiModeCodex
	agent.Status.SessionRef = ref
	f.command.rebind.launcher = &fakeNativeResumeLauncher{fakeResumeLauncher: f.launcher.fakeResumeLauncher, fakeNativePaneLauncher: &fakeNativePaneLauncher{}}
	controller := &fakeNativeThreadController{resolvedRoute: route, resumeBinding: codexappserver.ThreadBinding{ThreadID: resumeFixtureConversation}}
	f.command.rebind.create.codexNative = controller
	profiles := codexProfileHome(t, f.command.rebind.create)
	paths, err := configPaths(f.command.rebind.create.homeDir, f.command.rebind.create.lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	personas := persona.NewDefaultStore(paths)
	lead, err := personas.Write("lead", []byte(relaunchLeadInstructions))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := personas.Write("reviewer", []byte(relaunchReviewInstructions)); err != nil {
		t.Fatal(err)
	}
	digest := writeCodexProfile(t, profiles, "guard", "instructions = \"lead\"\n[permissions]\nsandbox = \"workspace-write\"\napproval = \"on-request\"\n")
	annotations := map[string]string{
		coremetadata.AnnotationAgentProfile:            "guard",
		coremetadata.AnnotationAgentProfileDigest:      digest,
		coremetadata.AnnotationAgentProfileSource:      coremetadata.SettingSourceFlag,
		coremetadata.AnnotationAgentPersona:            "lead",
		coremetadata.AnnotationAgentPersonaDigest:      lead.Digest,
		coremetadata.AnnotationAgentInstructionsSource: coremetadata.SettingSourceProfile,
		coremetadata.AnnotationAgentEffort:             "high",
		coremetadata.AnnotationAgentEffortSource:       coremetadata.SettingSourceFlag,
	}
	f.setAnnotations(maps.Clone(annotations))
	return f, profiles, controller, annotations
}

// TestACodexRelaunchRefusesInstructionsAndPermissionsItCannotChange is Task 3
// acceptance 4, refusal half: a Codex thread keeps the developer message it
// started with, and thread/resume can set a sandbox or an approval but never
// remove one, so a relaunch that would need either is refused before the
// stop.
func TestACodexRelaunchRefusesInstructionsAndPermissionsItCannotChange(t *testing.T) {
	for _, test := range []struct {
		name  string
		flags []string
		want  string
	}{
		{name: "instructions", flags: []string{"--instructions", "reviewer"}, want: personaReasonCodexInstructionsImmutable},
		{name: "no instructions", flags: []string{"--instructions", "none"}, want: personaReasonCodexInstructionsImmutable},
		{name: "profile with other instructions", flags: []string{"--profile", "review"}, want: personaReasonCodexInstructionsImmutable},
		{name: "reset of instructions to none", flags: []string{"--reset", "instructions"}, want: personaReasonCodexInstructionsImmutable},
		{name: "profile without a sandbox", flags: []string{"--profile", "loose"}, want: relaunchReasonCodexPermissionsKept},
		{name: "no profile", flags: []string{"--profile", "none", "--instructions", "lead"}, want: relaunchReasonCodexPermissionsKept},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, profiles, controller, annotations := codexRelaunchFixture(t)
			writeCodexProfile(t, profiles, "review", "instructions = \"reviewer\"\n[permissions]\nsandbox = \"read-only\"\napproval = \"never\"\n")
			writeCodexProfile(t, profiles, "loose", "instructions = \"lead\"\n[permissions]\napproval = \"never\"\n")
			if test.name == "reset of instructions to none" {
				f.setAnnotations(withAnnotations(annotations, coremetadata.AnnotationAgentInstructionsSource, coremetadata.SettingSourceFlag))
				writeCodexProfile(t, profiles, "guard", "[permissions]\nsandbox = \"workspace-write\"\napproval = \"on-request\"\n")
			}
			beforeAnnotations := f.agent(t).Metadata.Annotations
			before := f.store.snapshot()
			stdout, _, err := runRoute(t, f.command, append([]string{"relaunch", "uid:" + personaAttachAgent}, test.flags...)...)
			if err == nil || !IsUsageError(err) || !strings.Contains(err.Error(), "("+test.want+")") || !strings.HasSuffix(err.Error(), "nothing was changed") {
				t.Fatalf("relaunch %q err = %v, want a %s refusal", test.flags, err, test.want)
			}
			if stdout != "" || len(controller.resumes) != 0 {
				t.Fatalf("refused relaunch printed %q and resumed %d threads", stdout, len(controller.resumes))
			}
			assertRelaunchNothingChanged(t, f, before, beforeAnnotations)
		})
	}
}

// TestACodexRelaunchSwitchesToAProfileThatKeepsItsInstructions is Task 3
// acceptance 4, allowed half: a Codex switch to a profile with the same
// instructions and a full policy resumes the thread with the new policy, the
// new effort and model, and records the switch.
func TestACodexRelaunchSwitchesToAProfileThatKeepsItsInstructions(t *testing.T) {
	f, profiles, controller, annotations := codexRelaunchFixture(t)
	digest := writeCodexProfile(t, profiles, "strict", "instructions = \"lead\"\nmodel = \"gpt-6\"\neffort = \"low\"\n[permissions]\nsandbox = \"read-only\"\napproval = \"never\"\n")

	stdout, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--profile", "strict", "-o", "json")
	if err != nil {
		t.Fatalf("relaunch: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if len(controller.resumes) != 1 || controller.resumes[0].policy != readonlyThreadPolicy {
		t.Fatalf("thread resumes = %+v, want one with the strict policy", controller.resumes)
	}
	calls := splitWindowCalls(f.tmux)
	if len(calls) != 1 || !strings.Contains(strings.Join(calls[0], " "), "-m gpt-6 -c model_reasoning_effort=low resume") {
		t.Fatalf("split-window calls = %v, want one launch with the strict model and effort", calls)
	}
	want := withAnnotations(annotations,
		coremetadata.AnnotationAgentProfile, "strict",
		coremetadata.AnnotationAgentProfileDigest, digest,
		coremetadata.AnnotationAgentProfileSource, coremetadata.SettingSourceRelaunch,
		coremetadata.AnnotationAgentModel, "gpt-6",
		coremetadata.AnnotationAgentModelSource, coremetadata.SettingSourceProfile,
		coremetadata.AnnotationAgentEffort, "low",
		coremetadata.AnnotationAgentEffortSource, coremetadata.SettingSourceProfile)
	if got := f.agent(t).Metadata.Annotations; !maps.Equal(got, want) {
		t.Fatalf("annotations = %v, want %v", got, want)
	}
	if !strings.Contains(stdout, `"outcome":"restarted"`) || !strings.Contains(stdout, `"profile":{"name":"strict","digest":"`+digest+`","source":"relaunch"}`) {
		t.Fatalf("codex relaunch JSON = %s", stdout)
	}
}

// TestRelaunchResetItemsAreTheResolverItems pins the --reset vocabulary to the
// items the resolver knows.
func TestRelaunchResetItemsAreTheResolverItems(t *testing.T) {
	items, err := parseRelaunchReset("effort,instructions,effort")
	if err != nil || !slices.Equal(items, []string{agentsettings.ItemInstructions, agentsettings.ItemEffort}) {
		t.Fatalf("parseRelaunchReset = %v, %v", items, err)
	}
	if items, err := parseRelaunchReset("all"); err != nil || !slices.Equal(items, agentsettings.Items()) {
		t.Fatalf("parseRelaunchReset(all) = %v, %v", items, err)
	}
}
