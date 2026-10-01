package app

import (
	"errors"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// guardThreadPolicy is the policy of profile guard in codexRelaunchFixture.
var guardThreadPolicy = codexappserver.ThreadPolicy{Sandbox: codexappserver.SandboxWorkspaceWrite, ApprovalPolicy: codexappserver.ApprovalOnRequest}

// TestACodexRelaunchAsksTheThreadForTheSettingsItLaunches pins that the native
// resume of a Codex relaunch carries what the launch runs with -- the model
// and effort the TUI argv spells and the profile's policy -- so the thread's
// later turns run with them, a profile with another sandbox and approval
// included; and that a Running Agent's thread is probed before the stop.
func TestACodexRelaunchAsksTheThreadForTheSettingsItLaunches(t *testing.T) {
	for _, test := range []struct {
		name  string
		flags []string
		want  fakeNativeResume
	}{
		{"model and effort", []string{"--model", "gpt-7", "--effort", "xhigh"},
			fakeNativeResume{model: "gpt-7", effort: "xhigh", policy: guardThreadPolicy}},
		{"effort only", []string{"--effort", "low"},
			fakeNativeResume{effort: "low", policy: guardThreadPolicy}},
		{"profile with another sandbox and approval", []string{"--profile", "strict"},
			fakeNativeResume{model: "gpt-6", effort: "low", policy: readonlyThreadPolicy}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, profiles, controller, _ := codexRelaunchFixture(t)
			writeCodexProfile(t, profiles, "strict", "instructions = \"lead\"\nmodel = \"gpt-6\"\neffort = \"low\"\n[permissions]\nsandbox = \"read-only\"\napproval = \"never\"\n")

			stdout, stderr, err := runRoute(t, f.command, append([]string{"relaunch", "uid:" + personaAttachAgent}, test.flags...)...)
			if err != nil {
				t.Fatalf("relaunch %q: stdout=%q stderr=%q err=%v", test.flags, stdout, stderr, err)
			}
			if !slices.Equal(controller.probes, []string{resumeFixtureConversation}) {
				t.Fatalf("settings probes = %v, want the thread once before the stop", controller.probes)
			}
			if len(controller.resumes) != 1 {
				t.Fatalf("thread resumes = %+v, want one", controller.resumes)
			}
			got := controller.resumes[0]
			if got.model != test.want.model || got.effort != test.want.effort || got.policy != test.want.policy {
				t.Fatalf("thread resume = model %q effort %q policy %+v, want %+v", got.model, got.effort, got.policy, test.want)
			}
		})
	}
}

// TestARunningCodexRelaunchWhoseThreadCannotTakeSettingsIsRefusedBeforeTheStop
// pins the pre-stop refusal: an endpoint that does not take
// thread/settings/update could not apply the new settings after the stop, so
// the relaunch refuses with relaunch-codex-settings-unsupported, resumes
// nothing, and changes nothing.
func TestARunningCodexRelaunchWhoseThreadCannotTakeSettingsIsRefusedBeforeTheStop(t *testing.T) {
	f, _, controller, _ := codexRelaunchFixture(t)
	controller.probeErr = errors.New("codex app-server request failed: rpc-refused")
	beforeAnnotations := f.agent(t).Metadata.Annotations
	before := f.store.snapshot()

	stdout, _, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--model", "gpt-7", "--yes")
	if err == nil || !IsUsageError(err) || !strings.Contains(err.Error(), "("+relaunchReasonCodexSettingsUnsupported+")") ||
		!strings.HasSuffix(err.Error(), "nothing was changed") || !strings.Contains(err.Error(), "rpc-refused") {
		t.Fatalf("relaunch err = %v, want a %s refusal", err, relaunchReasonCodexSettingsUnsupported)
	}
	if stdout != "" || len(controller.resumes) != 0 {
		t.Fatalf("refused relaunch printed %q and resumed %d threads", stdout, len(controller.resumes))
	}
	assertRelaunchNothingChanged(t, f, before, beforeAnnotations)
}

// TestACodexRelaunchWhoseThreadDoesNotTakeTheSettingsRecoversWithAPlainResume
// pins the recovery after the stop: a resume the thread's settings refused
// would fail the same way when re-run with the same flags, so the recovery is
// a plain `agent resume` with the previous settings.
func TestACodexRelaunchWhoseThreadDoesNotTakeTheSettingsRecoversWithAPlainResume(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{"model kept", &codexappserver.SettingsMismatchError{Field: "model", Requested: "gpt-7"}},
		{"policy kept", &codexappserver.PolicyMismatchError{Method: "thread/settings/update", Requested: readonlyThreadPolicy, Effective: guardThreadPolicy}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, profiles, controller, _ := codexRelaunchFixture(t)
			writeCodexProfile(t, profiles, "strict", "instructions = \"lead\"\n[permissions]\nsandbox = \"read-only\"\napproval = \"never\"\n")
			controller.resumeErr = test.err

			_, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--profile", "strict", "--model", "gpt-7", "--yes")
			if err == nil || !strings.Contains(err.Error(), "is "+string(coremetadata.PhaseOffline)+" with its previous settings because its Codex thread did not take the new ones, and needs `agent resume`") {
				t.Fatalf("relaunch err = %v", err)
			}
			recovery := ""
			for line := range strings.Lines(stderr) {
				if after, ok := strings.CutPrefix(line, "projmux: recover with: "); ok {
					recovery = strings.TrimSpace(after)
				}
			}
			if !strings.HasPrefix(recovery, "projmux agent resume ") || strings.Contains(recovery, "--profile") || strings.Contains(recovery, "--model") {
				t.Fatalf("recover line = %q, want a plain agent resume (stderr %q)", recovery, stderr)
			}
		})
	}
}

// TestAPlainCodexResumeAsksTheThreadForTheRecordedEffortButNoModel pins the
// model rule (#1095 D1) on the thread: a plain resume passes the recorded
// effort again, as its argv does, and no model, so the thread keeps the model
// it runs.
func TestAPlainCodexResumeAsksTheThreadForTheRecordedEffortButNoModel(t *testing.T) {
	store := newFakeResourceStore(t)
	route := nativeTestRoute("generation-plain", coremetadata.CodexGenerationCurrent)
	ref := nativeTestSessionRef(route, resumeFixtureConversation)
	ref.ObservedAt = resourceFixtureClock
	setFixtureSessionRef(t, store, "agt-beta-codex", ref)
	agent, _ := store.registry.Agent("agt-beta-codex")
	agent.Metadata.Annotations = modelEffortAnnotations("gpt-6", "medium")
	tmux := newFakeTmux()
	command, legacy, _, _ := newTestAgentResumeCommand(t, store, tmux)
	command.rebind.launcher = &fakeNativeResumeLauncher{fakeResumeLauncher: legacy, fakeNativePaneLauncher: &fakeNativePaneLauncher{}}
	controller := &fakeNativeThreadController{resolvedRoute: route, resumeBinding: codexappserver.ThreadBinding{ThreadID: resumeFixtureConversation}}
	command.rebind.create.codexNative = controller

	if stdout, stderr, err := runRoute(t, command, "resume", "uid:agt-beta-codex"); err != nil {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if len(controller.resumes) != 1 || controller.resumes[0].model != "" || controller.resumes[0].effort != "medium" {
		t.Fatalf("thread resumes = %+v, want one with the recorded effort and no model", controller.resumes)
	}
	if len(controller.probes) != 0 {
		t.Fatalf("an Offline resume probed the thread: %v", controller.probes)
	}
}
