package app

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// codexProfileHome gives create an isolated HOME and returns the profile
// store of that HOME, the store every profile read of create resolves in.
func codexProfileHome(t *testing.T, create *createCommand) profile.Store {
	t.Helper()
	personaTestHome(t, create)
	paths, err := configPaths(create.homeDir, create.lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	return profile.NewDefaultStore(paths)
}

func writeCodexProfile(t *testing.T, store profile.Store, name, content string) string {
	t.Helper()
	entry, err := store.Write(name, []byte(content))
	if err != nil {
		t.Fatalf("write profile %s: %v", name, err)
	}
	return entry.Digest
}

// writeCodexProfileFile writes a profile file past the store's checks, the
// way a hand edit would.
func writeCodexProfileFile(t *testing.T, store profile.Store, name, content string) {
	t.Helper()
	path, err := store.Path(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

var readonlyThreadPolicy = codexappserver.ThreadPolicy{Sandbox: codexappserver.SandboxReadOnly, ApprovalPolicy: codexappserver.ApprovalNever}

// codexNativeCreateArgs is one prompted `create agent --provider codex` into
// alpha/main -- the native fresh lane -- with flags before the prompt.
func codexNativeCreateArgs(flags ...string) []string {
	args := append([]string{"agent", "--provider", "codex"}, flags...)
	return append(args, "--project", "alpha", "--window", "main", "--", "review this")
}

// TestCodexThreadPolicyMapsTheProfileVocabularyOntoTheWire pins the one
// spelling difference between a profile and Codex -- `full-access` is
// `danger-full-access` -- and that an absent value stays absent.
func TestCodexThreadPolicyMapsTheProfileVocabularyOntoTheWire(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		perms profile.Permissions
		want  codexappserver.ThreadPolicy
	}{
		{profile.Permissions{}, codexappserver.ThreadPolicy{}},
		{profile.Permissions{Allow: []string{"Read"}, Deny: []string{"Edit"}}, codexappserver.ThreadPolicy{}},
		{profile.Permissions{Sandbox: "read-only", Approval: "never"}, readonlyThreadPolicy},
		{profile.Permissions{Sandbox: "workspace-write"}, codexappserver.ThreadPolicy{Sandbox: codexappserver.SandboxWorkspaceWrite}},
		{profile.Permissions{Sandbox: "full-access", Approval: "on-request"},
			codexappserver.ThreadPolicy{Sandbox: codexappserver.SandboxDangerFullAccess, ApprovalPolicy: codexappserver.ApprovalOnRequest}},
		{profile.Permissions{Approval: "untrusted"}, codexappserver.ThreadPolicy{ApprovalPolicy: codexappserver.ApprovalUntrusted}},
	} {
		got, err := codexThreadPolicy(test.perms)
		if err != nil || got != test.want {
			t.Fatalf("codexThreadPolicy(%+v) = %+v, %v; want %+v", test.perms, got, err, test.want)
		}
	}
	for _, perms := range []profile.Permissions{{Sandbox: "danger-full-access"}, {Approval: "on-failure"}} {
		if _, err := codexThreadPolicy(perms); err == nil {
			t.Fatalf("codexThreadPolicy(%+v) accepted a value outside the profile vocabulary", perms)
		}
	}
}

// TestCodexNativeCreateSendsTheProfileSandboxAndApprovalOnThreadStart is C-2
// acceptance 2: on the native fresh lane a profile's sandbox and approval
// reach thread/start in the wire vocabulary, the Agent records the profile
// pair, and the UI create resolves the same policy.
func TestCodexNativeCreateSendsTheProfileSandboxAndApprovalOnThreadStart(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, content string
		want          codexappserver.ThreadPolicy
	}{
		{"readonly", "", readonlyThreadPolicy},
		{"wide", "[permissions]\nsandbox = \"full-access\"\napproval = \"on-request\"\n",
			codexappserver.ThreadPolicy{Sandbox: codexappserver.SandboxDangerFullAccess, ApprovalPolicy: codexappserver.ApprovalOnRequest}},
		{"careful", "[permissions]\napproval = \"untrusted\"\n", codexappserver.ThreadPolicy{ApprovalPolicy: codexappserver.ApprovalUntrusted}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			create, store, _, native, _ := newCodexPersonaCreate(t)
			profiles := codexProfileHome(t, create)
			if test.content != "" {
				writeCodexProfile(t, profiles, test.name, test.content)
			}
			loaded, err := profiles.Load(test.name)
			if err != nil {
				t.Fatal(err)
			}
			if _, stderr, err := runRoute(t, create, codexNativeCreateArgs("--profile", test.name)...); err != nil {
				t.Fatalf("create: %v (stderr=%q)", err, stderr)
			}
			if len(native.creates) != 1 || native.creates[0].policy != test.want || native.creates[0].prompt != "review this" {
				t.Fatalf("native creates = %+v, want one carrying policy %+v", native.creates, test.want)
			}
			agent := agentNamed(t, store, "win-alpha-main", "agent-test-1")
			if !maps.Equal(agent.Metadata.Annotations, profileAnnotations(test.name, loaded.Digest)) {
				t.Fatalf("Agent annotations = %v, want the profile pair", agent.Metadata.Annotations)
			}

			plan, err := create.prepareIntentAgent(aiModeCodex, resourceCreateFlags{profile: test.name, payload: []string{"review this"}})
			if err != nil || plan.flags.profileLaunch.codexPolicy != test.want {
				t.Fatalf("UI plan policy = %+v, %v; want %+v", plan.flags.profileLaunch.codexPolicy, err, test.want)
			}
		})
	}
}

// TestCodexNativeCreateWithoutAProfileSendsAZeroPolicy is C-2 acceptance 1 on
// the create seam: with no profile, `--profile none`, or a profile that sets
// no sandbox or approval, thread/start carries the zero policy -- the request
// it sent before policies existed -- and the output is what it was.
func TestCodexNativeCreateWithoutAProfileSendsAZeroPolicy(t *testing.T) {
	t.Parallel()
	var outputs []string
	for _, flags := range [][]string{nil, {"--profile", "none"}, {"--profile", "rules"}} {
		create, _, _, native, _ := newCodexPersonaCreate(t)
		profiles := codexProfileHome(t, create)
		writeCodexProfile(t, profiles, "rules", "[permissions]\ndeny = [\"Edit\"]\n")
		stdout, _, err := runRoute(t, create, codexNativeCreateArgs(append(flags, "-o", "receipt")...)...)
		if err != nil {
			t.Fatal(err)
		}
		if len(native.creates) != 1 || !native.creates[0].policy.IsZero() {
			t.Fatalf("%v: native creates = %+v, want one with a zero policy", flags, native.creates)
		}
		outputs = append(outputs, stdout)
	}
	if outputs[0] != outputs[1] || strings.Contains(outputs[0], "profile") {
		t.Fatalf("no profile = %q, --profile none = %q; want identical and profile-free", outputs[0], outputs[1])
	}
}

// TestCodexProfilePermissionsAreRefusedOffTheNativeFreshLane is C-2's lane
// rule: every Codex create that does not start its own native thread -- no
// prompt, or --interactive-only -- and every provider but Claude and Codex
// refuse a profile with permissions, with the stable token, before any thread,
// Registry write, or Pane exists.
func TestCodexProfilePermissionsAreRefusedOffTheNativeFreshLane(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		args []string
		lane string
	}{
		{"codex without a prompt", []string{"agent", "--provider", "codex", "--profile", "readonly", "--project", "alpha", "--window", "main"}, "only on a create with a prompt"},
		{"codex interactive-only", codexNativeCreateArgs("--profile", "readonly", "--interactive-only"), "only on a create with a prompt"},
		{"antigravity with a prompt", []string{"agent", "--provider", "antigravity", "--profile", "readonly", "--project", "alpha", "--window", "main", "--", "review this"}, "cannot apply"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			create, store, tmux, native, _ := newCodexPersonaCreate(t)
			codexProfileHome(t, create)
			before, panes := store.snapshot(), tmux.paneCount()
			stdout, _, err := runRoute(t, create, test.args...)
			if err == nil || !IsUsageError(err) || !strings.Contains(err.Error(), profileReasonPermissionsUnsupported) ||
				!strings.Contains(err.Error(), test.lane) || stdout != "" {
				t.Fatalf("create = %v (stdout %q), want a %s refusal naming %q", err, stdout, profileReasonPermissionsUnsupported, test.lane)
			}
			if len(native.creates) != 0 || store.snapshot() != before || store.writes != 0 || tmux.paneCount() != panes {
				t.Fatalf("refused create mutated state: creates=%+v writes=%d", native.creates, store.writes)
			}
		})
	}
}

// TestCodexCreateResultsDiscloseAllowDenyModelAndEffortButNotSandboxOrApproval
// is C-2 acceptance 4: on Codex the items not applied -- allow and deny, model
// and effort -- are disclosed with their reasons, in the human result and the
// receipt, and the applied sandbox and approval are not.
func TestCodexCreateResultsDiscloseAllowDenyModelAndEffortButNotSandboxOrApproval(t *testing.T) {
	t.Parallel()
	create, _, _, native, _ := newCodexPersonaCreate(t)
	profiles := codexProfileHome(t, create)
	digest := writeCodexProfile(t, profiles, "guard", "model = \"opus\"\neffort = \"high\"\n[permissions]\nsandbox = \"workspace-write\"\napproval = \"on-request\"\nallow = [\"Read\"]\ndeny = [\"Edit\"]\n")

	stdout, _, err := runRoute(t, create, codexNativeCreateArgs("--profile", "guard")...)
	if err != nil {
		t.Fatal(err)
	}
	want := []cli.ReceiptProfileItem{
		{Item: profileItemAllow, Provider: aiModeCodex, Reason: profileReasonCodexCommandRulesUnsupported},
		{Item: profileItemDeny, Provider: aiModeCodex, Reason: profileReasonCodexCommandRulesUnsupported},
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	var disclosed []string
	for _, line := range lines {
		if strings.HasPrefix(line, "profile") {
			disclosed = append(disclosed, line)
		}
	}
	wantLines := []string{"profile name=guard digest=" + digest}
	for _, item := range want {
		wantLines = append(wantLines, "profile-not-applied item="+item.Item+" provider=codex reason="+item.Reason)
	}
	slices.Sort(disclosed[1:])
	slices.Sort(wantLines[1:])
	if !slices.Equal(disclosed, wantLines) {
		t.Fatalf("profile lines = %q, want %q", disclosed, wantLines)
	}
	for _, applied := range []string{profileItemSandbox, profileItemApproval} {
		if strings.Contains(stdout, applied) {
			t.Fatalf("stdout discloses applied item %s: %q", applied, stdout)
		}
	}
	if native.creates[0].policy != (codexappserver.ThreadPolicy{Sandbox: codexappserver.SandboxWorkspaceWrite, ApprovalPolicy: codexappserver.ApprovalOnRequest}) {
		t.Fatalf("thread/start policy = %+v", native.creates[0].policy)
	}
	if native.creates[0].model != "opus" || native.creates[0].effort != "high" {
		t.Fatalf("native model/effort = %q/%q", native.creates[0].model, native.creates[0].effort)
	}

	stdout, _, err = runRoute(t, create, codexNativeCreateArgs("--profile", "guard", "--name", "json", "-o", "receipt")...)
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Profile *cli.ReceiptProfile `json:"profile"`
	}
	if err := json.Unmarshal([]byte(stdout), &receipt); err != nil || receipt.Profile == nil {
		t.Fatalf("receipt = %q (%v), want a profile field", stdout, err)
	}
	got := slices.Clone(receipt.Profile.NotApplied)
	byItem := func(a, b cli.ReceiptProfileItem) int { return strings.Compare(a.Item, b.Item) }
	slices.SortFunc(got, byItem)
	slices.SortFunc(want, byItem)
	if receipt.Profile.Name != "guard" || receipt.Profile.Digest != digest || !slices.Equal(got, want) {
		t.Fatalf("receipt profile = %+v, want not-applied %+v", *receipt.Profile, want)
	}
}

// TestCodexNativeCreateRefusesAThreadWhosePolicyDiffers is C-2's answer
// check: a thread/start whose answer is not the requested policy fails the
// create -- no Agent, no Pane -- and offers no lane without the policy.
func TestCodexNativeCreateRefusesAThreadWhosePolicyDiffers(t *testing.T) {
	t.Parallel()
	create, store, tmux, native, _ := newCodexPersonaCreate(t)
	codexProfileHome(t, create)
	native.createErr = &codexappserver.PolicyMismatchError{
		Method: "thread/start", Requested: readonlyThreadPolicy,
		Effective: codexappserver.ThreadPolicy{Sandbox: "workspaceWrite", ApprovalPolicy: codexappserver.ApprovalNever},
	}
	agents := len(store.registry.Agents)
	_, _, err := runRoute(t, create, codexNativeCreateArgs("--profile", "readonly")...)
	if err == nil || !strings.Contains(err.Error(), codexappserver.ReasonPolicyMismatch) || strings.Contains(err.Error(), interactiveOnlyFlag) {
		t.Fatalf("create = %v, want a %s refusal with no second lane", err, codexappserver.ReasonPolicyMismatch)
	}
	if len(store.registry.Agents) != agents || len(splitWindowCalls(tmux)) != 0 || len(native.creates) != 1 {
		t.Fatalf("mismatched create left agents %d -> %d, splits %v", agents, len(store.registry.Agents), splitWindowCalls(tmux))
	}
}

// TestCodexRefusesAProfileWithAnyPermissionOffTheNativeFreshLane replaces
// Task 2's TestCodexRefusesAProfileWithAnyPermission: off the native fresh
// lane (here --interactive-only) Codex, like every provider but Claude,
// refuses a profile carrying any permission, with its stable token and
// nothing created; a permission-free profile applies model and effort, and its instructions follow the persona lane
// rule --instructions follows.
func TestCodexRefusesAProfileWithAnyPermissionOffTheNativeFreshLane(t *testing.T) {
	t.Parallel()
	f := newProfileFixture(t)
	f.writeProfile(t, "sandboxed", "[permissions]\nsandbox = \"read-only\"\n")
	f.writeProfile(t, "approving", "[permissions]\napproval = \"never\"\n")
	f.writeProfile(t, "allowing", "[permissions]\nallow = [\"Read\"]\n")
	for _, name := range []string{"readonly", "sandboxed", "approving", "allowing"} {
		for _, provider := range []string{aiModeCodex, aiModeAntigravity} {
			args := []string{"agent", "--provider", provider, "--profile", name, "--project", "alpha", "--window", "review"}
			if provider == aiModeCodex {
				args = append(args, "--interactive-only")
			}
			stdout, _, err := runRoute(t, f.create, args...)
			if err == nil || !IsUsageError(err) || !strings.Contains(err.Error(), profileReasonPermissionsUnsupported) || stdout != "" {
				t.Fatalf("%s --profile %s = %v (stdout %q), want a %s refusal", provider, name, err, stdout, profileReasonPermissionsUnsupported)
			}
		}
	}
	if len(f.launcher.argv) != 0 || len(f.store.registry.Agents) != 2 {
		t.Fatalf("refused creates launched %d and left %d Agents", len(f.launcher.argv), len(f.store.registry.Agents))
	}

	digest := f.writeProfile(t, "tuned", "model = \"opus\"\neffort = \"high\"\n")
	stdout, _, err := runRoute(t, f.create, "agent", "--provider", "codex", "--profile", "tuned", "--interactive-only",
		"--project", "alpha", "--window", "review")
	if err != nil {
		t.Fatal(err)
	}
	if tail, want := f.onlyArgvTail(t, aiModeCodex), []string{"-m", "opus", "-c", "model_reasoning_effort=high", "-C", "/srv/alpha"}; !slices.Equal(tail, want) {
		t.Fatalf("codex exec argv tail = %q, want %q", tail, want)
	}
	if strings.Contains(stdout, profileReasonProviderOptionUnsupported) {
		t.Fatalf("Codex profile model or effort was skipped: %q", stdout)
	}
	agent := f.createdAgent(t)
	if agent.Metadata.Annotations[coremetadata.AnnotationAgentProfileDigest] != digest || agent.Metadata.Annotations[coremetadata.AnnotationAgentEffort] != "high" {
		t.Fatalf("codex Agent annotations = %v, want the profile pair and high effort", agent.Metadata.Annotations)
	}

	if _, err := f.personas.Write("go-reviewer", []byte("x\n")); err != nil {
		t.Fatal(err)
	}
	f.writeProfile(t, "instructed", "instructions = \"go-reviewer\"\n")
	_, _, err = runRoute(t, f.create, "agent", "--provider", "codex", "--profile", "instructed", "--interactive-only",
		"--project", "alpha", "--window", "review", "--name", "instructed")
	if err == nil || !strings.Contains(err.Error(), "persona-provider-unsupported") {
		t.Fatalf("codex plain lane with profile instructions = %v, want the persona-provider-unsupported refusal --instructions gets", err)
	}
}

// codexProfileResume wires one `agent resume` of the native Codex fixture
// Agent carrying annotations, with an isolated profile HOME.
func codexProfileResume(t *testing.T, annotations map[string]string) (*agentCommand, *fakeResourceStore, *fakeTmux, *fakeNativeThreadController, profile.Store) {
	t.Helper()
	store := newFakeResourceStore(t)
	setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
	target, _ := store.registry.Agent("agt-beta-codex")
	target.Metadata.Annotations = maps.Clone(annotations)
	tmux := newFakeTmux()
	command, launcher, _, _ := newTestAgentResumeCommand(t, store, tmux)
	native, _ := enablePinnedNativeResumeFixture(t, command, store, "agt-beta-codex", launcher)
	return command, store, tmux, native, codexProfileHome(t, command.rebind.create)
}

// TestCodexAgentResumeResendsTheCurrentProfilePolicyAndRecordsItsDigest is
// C-3 on `agent resume`: the profile is re-read by name, the sandbox and
// approval it holds now -- not at creation -- ride thread/resume, and the
// Agent records the digest of that content. An Agent without a profile sends
// the zero policy.
func TestCodexAgentResumeResendsTheCurrentProfilePolicyAndRecordsItsDigest(t *testing.T) {
	t.Parallel()
	command, store, _, native, profiles := codexProfileResume(t, profileAnnotations("guard", "sha256:created"))
	writeCodexProfile(t, profiles, "guard", "[permissions]\nsandbox = \"read-only\"\napproval = \"never\"\n")
	edited := writeCodexProfile(t, profiles, "guard", "[permissions]\nsandbox = \"workspace-write\"\napproval = \"untrusted\"\n")

	if _, _, err := runRoute(t, command, "resume", "uid:agt-beta-codex"); err != nil {
		t.Fatal(err)
	}
	want := codexappserver.ThreadPolicy{Sandbox: codexappserver.SandboxWorkspaceWrite, ApprovalPolicy: codexappserver.ApprovalUntrusted}
	if len(native.resumes) != 1 || native.resumes[0].policy != want {
		t.Fatalf("native resumes = %+v, want one carrying %+v", native.resumes, want)
	}
	agent, _ := store.registry.Agent("agt-beta-codex")
	if !maps.Equal(agent.Metadata.Annotations, profileAnnotations("guard", edited)) || agent.Status.PaneRef == "" {
		t.Fatalf("resumed Agent annotations = %v paneRef=%q, want the current digest %s", agent.Metadata.Annotations, agent.Status.PaneRef, edited)
	}

	command, store, _, native, _ = codexProfileResume(t, nil)
	if _, _, err := runRoute(t, command, "resume", "uid:agt-beta-codex"); err != nil {
		t.Fatal(err)
	}
	if len(native.resumes) != 1 || !native.resumes[0].policy.IsZero() {
		t.Fatalf("unprofiled native resumes = %+v, want one with a zero policy", native.resumes)
	}
	if agent, _ := store.registry.Agent("agt-beta-codex"); agent.Metadata.Annotations != nil {
		t.Fatalf("unprofiled resume annotated the Agent: %v", agent.Metadata.Annotations)
	}
}

// TestCodexAgentResumeRefusesAGoneOrInvalidProfileWithZeroWrites is C-3's
// preflight: a profile that is gone, invalid, or now role-claimed refuses the
// resume with profile-resume-unavailable before any provider call, Registry
// write, or Pane.
func TestCodexAgentResumeRefusesAGoneOrInvalidProfileWithZeroWrites(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, reason string }{
		{"gone", profile.ReasonNotFound},
		{"broken", profile.ReasonValueInvalid},
		{"claimed", profile.ReasonRoleClaimed},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			command, store, tmux, native, profiles := codexProfileResume(t, profileAnnotations(test.name, "sha256:old"))
			writeCodexProfileFile(t, profiles, "broken", "[permissions]\nsandbox = \"everything\"\n")
			writeCodexProfileFile(t, profiles, "claimed", "roles = [\"reviewer\"]\n[permissions]\nsandbox = \"read-only\"\n")
			writeCodexProfileFile(t, profiles, "rival", "roles = [\"reviewer\"]\n")
			before := store.snapshot()
			_, _, err := runRoute(t, command, "resume", "uid:agt-beta-codex")
			if err == nil || !strings.Contains(err.Error(), profileReasonResumeUnavailable) || !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("resume = %v, want %s (%s)", err, profileReasonResumeUnavailable, test.reason)
			}
			if len(native.resumes) != 0 || store.writes != 0 || store.snapshot() != before || len(splitWindowCalls(tmux)) != 0 {
				t.Fatalf("refused resume acted: resumes=%+v writes=%d splits=%v", native.resumes, store.writes, splitWindowCalls(tmux))
			}
		})
	}
}

// TestCodexAgentResumeRefusesAThreadWhosePolicyDiffers is C-3 acceptance 3: a
// thread/resume whose answer is not the profile's policy -- a thread already
// loaded with another one -- refuses the resume and rolls the transaction
// back: no Pane, and the recorded digest is unchanged.
func TestCodexAgentResumeRefusesAThreadWhosePolicyDiffers(t *testing.T) {
	t.Parallel()
	command, store, tmux, native, _ := codexProfileResume(t, profileAnnotations("readonly", "sha256:old"))
	native.resumeErr = &codexappserver.PolicyMismatchError{
		Method: "thread/resume", Requested: readonlyThreadPolicy,
		Effective: codexappserver.ThreadPolicy{Sandbox: "dangerFullAccess", ApprovalPolicy: codexappserver.ApprovalNever},
	}
	_, _, err := runRoute(t, command, "resume", "uid:agt-beta-codex")
	if err == nil || !strings.Contains(err.Error(), codexappserver.ReasonPolicyMismatch) {
		t.Fatalf("resume = %v, want a %s refusal", err, codexappserver.ReasonPolicyMismatch)
	}
	agent, _ := store.registry.Agent("agt-beta-codex")
	if len(native.resumes) != 1 || native.resumes[0].policy != readonlyThreadPolicy || agent.Status.PaneRef != "" ||
		agent.Metadata.Annotations[coremetadata.AnnotationAgentProfileDigest] != "sha256:old" || len(splitWindowCalls(tmux)) != 0 {
		t.Fatalf("mismatched resume committed: resumes=%+v annotations=%v paneRef=%q", native.resumes, agent.Metadata.Annotations, agent.Status.PaneRef)
	}
}

// codexPickerNativeResume runs one resume-picker selection of a native
// app-server Codex thread that agt-beta-codex already records with
// annotations.
func codexPickerNativeResume(t *testing.T, annotations map[string]string, prepare func(profile.Store)) (canonicalRootFixture, *fakeNativeThreadController, error) {
	t.Helper()
	fx := canonicalFixture(t, false)
	const id = "019f0000-0000-7000-8000-0000000000c3"
	route := nativeTestRoute("generation-picker", coremetadata.CodexGenerationCurrent)
	native := &fakeNativeThreadController{resolvedRoute: route, resumeBinding: codexappserver.ThreadBinding{ThreadID: id}}
	fx.create.codexNative = native
	fx.create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: &fakeNativePaneLauncher{}}
	profiles := codexProfileHome(t, fx.create)
	if prepare != nil {
		prepare(profiles)
	}
	pickerLaunchValuesFixture{canonicalRootFixture: fx}.hold(t, "agt-beta-codex", aiModeCodex, id, annotations, nil)
	_, err := fx.create.createFromIntent(agentPaneIntent{
		producer: canonicalProducerResumePicker, provider: aiModeCodex, placement: "right",
		conversationID: id, resumeSource: aisessions.SourceCodexAppServer, anchorPaneID: fx.originID,
		resumeEndpoint: route.Endpoint, resumeGenerationState: coremetadata.CodexGenerationCurrent,
	}, ioDiscard{}, ioDiscard{})
	return fx, native, err
}

// TestResumePickerNativeCodexResumeResendsTheInheritedProfilePolicy is C-3 on
// the resume picker's native lane: the new Agent inherits the holders'
// profile, its current policy rides thread/resume, and the new Agent records
// the current digest; a profile that is gone refuses with no Agent created.
func TestResumePickerNativeCodexResumeResendsTheInheritedProfilePolicy(t *testing.T) {
	t.Parallel()
	var digest string
	fx, native, err := codexPickerNativeResume(t, profileAnnotations("guard", "sha256:stale"), func(profiles profile.Store) {
		digest = writeCodexProfile(t, profiles, "guard", "[permissions]\nsandbox = \"read-only\"\napproval = \"never\"\n")
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(native.resumes) != 1 || native.resumes[0].policy != readonlyThreadPolicy {
		t.Fatalf("native resumes = %+v, want one carrying %+v", native.resumes, readonlyThreadPolicy)
	}
	agents := fx.store.registry.AgentsOf(fx.windowUID)
	created := agents[len(agents)-1]
	if created.Metadata.UID == "agt-beta-codex" || !maps.Equal(created.Metadata.Annotations, profileAnnotations("guard", digest)) {
		t.Fatalf("picker Agent %s annotations = %v, want the current digest %s", created.Metadata.UID, created.Metadata.Annotations, digest)
	}

	fx, native, err = codexPickerNativeResume(t, nil, nil)
	if err != nil || len(native.resumes) != 1 || !native.resumes[0].policy.IsZero() {
		t.Fatalf("unprofiled picker resume = %v, resumes %+v; want one with a zero policy", err, native.resumes)
	}

	fx, native, err = codexPickerNativeResume(t, profileAnnotations("gone", "sha256:old"), nil)
	if err == nil || !strings.Contains(err.Error(), profileReasonResumeUnavailable) || !strings.Contains(err.Error(), profile.ReasonNotFound) ||
		len(native.resumes) != 0 || len(splitWindowCalls(fx.tmux)) != 0 {
		t.Fatalf("gone profile picker resume = %v (resumes %+v), want a %s refusal", err, native.resumes, profileReasonResumeUnavailable)
	}
	for _, agent := range fx.store.registry.AgentsOf(fx.windowUID) {
		if agent.Metadata.UID != "agt-beta-codex" && agent.Metadata.Annotations[coremetadata.AnnotationAgentProfile] == "gone" {
			t.Fatalf("a refused picker resume left Agent %s", agent.Metadata.UID)
		}
	}
}

// TestCodexCLIResumeLanesRefuseAProfileWithPermissions is C-3's fail-closed
// rule for the lanes that resume Codex through `codex resume <id>`: topology
// replay and a rollout row of the resume picker. They cannot carry a sandbox
// or an approval, so a profiled Agent whose profile now sets any permission is
// not resumed; a profile without permissions resumes as it did.
func TestCodexCLIResumeLanesRefuseAProfileWithPermissions(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, content string
		refused       bool
	}{
		{"permissions", "[permissions]\nsandbox = \"read-only\"\n", true},
		{"rules only", "[permissions]\ndeny = [\"Edit\"]\n", true},
		{"no permissions", "model = \"opus\"\n", false},
	} {
		t.Run("topology replay "+test.name, func(t *testing.T) {
			t.Parallel()
			command, store, _, _, root, _ := newTopologyMaterializeFixture(t)
			planner := agentLaunchArgvTestCommand(t)
			paths, err := configPaths(planner.homeDir, planner.lookupEnv)
			if err != nil {
				t.Fatal(err)
			}
			writeCodexProfile(t, profile.NewDefaultStore(paths), "guard", test.content)
			launcher := &profileTopologyLauncher{fakeTopologyAgentLauncher: command.agents.(*fakeTopologyAgentLauncher), planner: planner}
			command.agents = launcher
			agent := addTopologyFixtureAgent(t, store, topologyFixtureAgent{name: "codex", provider: "codex", cwd: root, ref: codexConversationRef("thread-profiled")})
			stored, _ := store.registry.Agent(agent.Metadata.UID)
			stored.Metadata.Annotations = profileAnnotations("guard", "sha256:stale")
			markTopologyAgentInterrupted(t, store, agent.Metadata.UID, "")

			_, stderr, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
			if err != nil {
				t.Fatalf("materialize: %v (%s)", err, stderr)
			}
			after, _ := store.registry.Agent(agent.Metadata.UID)
			if test.refused {
				if len(launcher.argv) != 0 || after.Status.Phase == coremetadata.PhaseRunning ||
					!strings.Contains(stderr, profileReasonResumeUnavailable) || !strings.Contains(stderr, profileReasonLaneUnsupported) {
					t.Fatalf("argv=%v phase=%s stderr=%q, want a %s refusal", launcher.argv, after.Status.Phase, stderr, profileReasonLaneUnsupported)
				}
				return
			}
			if len(launcher.argv) != 1 || after.Status.Phase != coremetadata.PhaseRunning ||
				after.Metadata.Annotations[coremetadata.AnnotationAgentProfileDigest] != "sha256:stale" {
				t.Fatalf("argv=%v phase=%s annotations=%v, want the resume it had before", launcher.argv, after.Status.Phase, after.Metadata.Annotations)
			}
		})
		t.Run("rollout picker "+test.name, func(t *testing.T) {
			t.Parallel()
			f := newPickerLaunchValuesFixture(t, nil)
			paths, err := configPaths(f.planner.homeDir, f.planner.lookupEnv)
			if err != nil {
				t.Fatal(err)
			}
			writeCodexProfile(t, profile.NewDefaultStore(paths), "guard", test.content)
			const id = "019f0000-0000-7000-8000-0000000000c4"
			f.hold(t, "agt-beta-codex", aiModeCodex, id, profileAnnotations("guard", "sha256:stale"), nil)
			if !test.refused {
				agent, argv, _ := f.pick(t, false, aiModeCodex, id, aisessions.SourceCodexRollout)
				if got := execArgvTail(t, argv, aiModeCodex); !slices.Contains(got, id) || agent.Metadata.Annotations[coremetadata.AnnotationAgentProfile] != "guard" {
					t.Fatalf("rollout resume argv tail = %q annotations = %v", got, agent.Metadata.Annotations)
				}
				return
			}
			agents := len(f.store.registry.Agents)
			_, err = f.create.createFromIntent(agentPaneIntent{
				producer: canonicalProducerResumePicker, provider: aiModeCodex, placement: "right",
				conversationID: id, resumeSource: aisessions.SourceCodexRollout, anchorPaneID: f.originID,
			}, ioDiscard{}, ioDiscard{})
			if err == nil || !strings.Contains(err.Error(), profileReasonLaneUnsupported) || len(f.store.registry.Agents) != agents || len(f.launcher.argv) != 0 {
				t.Fatalf("rollout picker = %v (agents %d -> %d, argv %v), want a %s refusal", err, agents, len(f.store.registry.Agents), f.launcher.argv, profileReasonLaneUnsupported)
			}
		})
	}
}

// TestCodexNativeResumeCallersAreExactlyTheTwoResumePaths closes the caller
// set C-3 covers: every non-test reference to the native controller's Resume
// in internal/app sits in `agent resume` or the resume picker's native
// catalog resume, the two paths that re-send the profile's policy.
func TestCodexNativeResumeCallersAreExactlyTheTwoResumePaths(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var got []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			where := "<package level>"
			if fn, ok := decl.(*ast.FuncDecl); ok {
				where = funcDeclName(fn)
			}
			ast.Inspect(decl, func(node ast.Node) bool {
				sel, ok := node.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Resume" {
					return true
				}
				if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "codexNative" {
					got = append(got, name+":"+where)
				}
				return true
			})
		}
	}
	sort.Strings(got)
	want := []string{
		"agent_resume.go:(*agentRebinder).rebind",
		"create_intent.go:(*createCommand).openIntentAgent",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("codexNative.Resume references = %q, want exactly %q", got, want)
	}
}
