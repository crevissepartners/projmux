package app

import (
	"bytes"
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
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// exactArgvAgentLauncher is the create-seam fake whose launch planning is the
// real Claude/Codex planner, so a create test asserts the exact provider argv.
// Everything else -- binding, activation -- stays the fake's.
type exactArgvAgentLauncher struct {
	*fakeAgentLauncher
	planner *aiCommand
	argv    [][]string
}

func (l *exactArgvAgentLauncher) record(title string, argv []string, err error) (string, []string, error) {
	if err == nil {
		l.argv = append(l.argv, slices.Clone(argv))
	}
	return title, argv, err
}

func (l *exactArgvAgentLauncher) PlanAgentLaunch(provider string, workspace coremetadata.AgentWorkspace, payload []string) (string, []string, error) {
	return l.record(l.planner.PlanAgentLaunch(provider, workspace, payload))
}

func (l *exactArgvAgentLauncher) PlanAgentLaunchWithOptions(provider string, workspace coremetadata.AgentWorkspace, payload []string, model, effort, personaFile string) (string, []string, error) {
	return l.record(l.planner.PlanAgentLaunchWithOptions(provider, workspace, payload, model, effort, personaFile))
}

func (l *exactArgvAgentLauncher) PlanAgentLaunchWithSettings(provider string, workspace coremetadata.AgentWorkspace, payload []string, model, effort, personaFile, settingsFile string) (string, []string, error) {
	return l.record(l.planner.PlanAgentLaunchWithSettings(provider, workspace, payload, model, effort, personaFile, settingsFile))
}

func (l *exactArgvAgentLauncher) PlanCodexAgentLaunchWithPolicy(workspace coremetadata.AgentWorkspace, payload []string, model, effort string, policy codexappserver.ThreadPolicy) (string, []string, error) {
	return l.record(l.planner.PlanCodexAgentLaunchWithPolicy(workspace, payload, model, effort, policy))
}

// profileFixture is one isolated HOME with its profile and persona stores,
// shared by a create command and the real launch planner.
type profileFixture struct {
	store    *fakeResourceStore
	tmux     *fakeTmux
	create   *createCommand
	launcher *exactArgvAgentLauncher
	planner  *aiCommand
	profiles profile.Store
	personas persona.Store
	stateDir string
}

func newProfileFixture(t *testing.T) profileFixture {
	t.Helper()
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, fake := newTestAgentCreateCommand(t, store, tmux)
	planner := agentLaunchArgvTestCommand(t)
	create.homeDir, create.lookupEnv = planner.homeDir, planner.lookupEnv
	launcher := &exactArgvAgentLauncher{fakeAgentLauncher: fake, planner: planner}
	create.agents = launcher
	paths, err := configPaths(planner.homeDir, planner.lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	return profileFixture{
		store: store, tmux: tmux, create: create, launcher: launcher, planner: planner,
		profiles: profile.NewDefaultStore(paths), personas: persona.NewDefaultStore(paths), stateDir: paths.StateDir,
	}
}

// writeProfile stores one valid profile through the store's own validation.
func (f profileFixture) writeProfile(t *testing.T, name, content string) string {
	t.Helper()
	entry, err := f.profiles.Write(name, []byte(content))
	if err != nil {
		t.Fatalf("write profile %s: %v", name, err)
	}
	return entry.Digest
}

// writeProfileFile writes a profile file directly, bypassing the store's
// checks, the way a hand edit would.
func (f profileFixture) writeProfileFile(t *testing.T, name, content string) {
	t.Helper()
	path, err := f.profiles.Path(name)
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

// createClaude runs one `create agent --provider claude` with flags into
// alpha/review with a prompt.
func (f profileFixture) createClaude(t *testing.T, flags ...string) (string, string, error) {
	t.Helper()
	args := append([]string{"agent", "--provider", "claude"}, flags...)
	args = append(args, "--project", "alpha", "--window", "review", "--", "review this")
	return runRoute(t, f.create, args...)
}

func (f profileFixture) createdAgent(t *testing.T) coremetadata.Agent {
	t.Helper()
	return agentNamed(t, f.store, "win-alpha-review", "agent-test-1")
}

func (f profileFixture) onlyArgvTail(t *testing.T, provider string) []string {
	t.Helper()
	if len(f.launcher.argv) != 1 {
		t.Fatalf("planned %d launches, want 1", len(f.launcher.argv))
	}
	return execArgvTail(t, f.launcher.argv[0], provider)
}

func (f profileFixture) settingsPath(t *testing.T, perms profile.Permissions) string {
	t.Helper()
	content, err := profile.SettingsContent(perms)
	if err != nil || content == nil {
		t.Fatalf("settings content = %q, %v", content, err)
	}
	return profile.SettingsSnapshotPath(f.stateDir, content)
}

const guardProfile = `instructions = "go-reviewer"
model = "opus"
effort = "high"

[permissions]
sandbox = "workspace-write"
approval = "on-request"
allow = ["Read", "Bash(git log *)"]
deny = ["Edit", "Write"]
`

var guardPermissions = profile.Permissions{Allow: []string{"Read", "Bash(git log *)"}, Deny: []string{"Edit", "Write"}}

// TestCreateClaudeAgentWithoutAProfileKeepsTheExactArgv is acceptance 1: a
// create that applies no profile -- none spelled, `--profile none`, or a
// `role` label no profile lists -- launches the exact argv, annotations and
// output it launched before profiles existed.
func TestCreateClaudeAgentWithoutAProfileKeepsTheExactArgv(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		flags []string
	}{
		{"no profile", nil},
		{"profile none", []string{"--profile", "none"}},
		{"role no profile lists", []string{"--label", "role=nobody"}},
		{"profile none over a mapped role", []string{"--profile", "none", "--label", "role=reviewer"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f := newProfileFixture(t)
			f.writeProfile(t, "mapped", "roles = [\"reviewer\"]\n[permissions]\ndeny = [\"Edit\"]\n")
			stdout, stderr, err := f.createClaude(t, test.flags...)
			if err != nil {
				t.Fatalf("create: %v (stderr=%q)", err, stderr)
			}
			if got, want := f.onlyArgvTail(t, aiModeClaude), []string{"--", "review this"}; !slices.Equal(got, want) {
				t.Fatalf("exec argv tail = %q, want exactly %q", got, want)
			}
			agent := f.createdAgent(t)
			if _, ok := agent.Metadata.Annotations[coremetadata.AnnotationAgentProfile]; ok {
				t.Fatalf("Agent annotations = %v, want no profile", agent.Metadata.Annotations)
			}
			if test.name == "no profile" && agent.Metadata.Annotations != nil {
				t.Fatalf("Agent annotations = %#v, want nil", agent.Metadata.Annotations)
			}
			if strings.Contains(stdout, "profile") || strings.Contains(stderr, "profile") {
				t.Fatalf("output mentions a profile: stdout=%q stderr=%q", stdout, stderr)
			}
			if _, err := os.Stat(filepath.Join(f.stateDir, profile.SettingsDirName)); !os.IsNotExist(err) {
				t.Fatalf("a create without a profile wrote a settings snapshot: %v", err)
			}
		})
	}
}

// TestCreateClaudeAgentWithAProfileLaunchesItsSettingsModelEffortAndInstructions
// is acceptance 2: the profile's permissions reach Claude as --settings
// <snapshot>, its model and effort as --model/--effort, and its instructions
// through the persona path, with the persona, effort and profile annotations.
func TestCreateClaudeAgentWithAProfileLaunchesItsSettingsModelEffortAndInstructions(t *testing.T) {
	t.Parallel()
	f := newProfileFixture(t)
	instructions := []byte("You review Go code.\n")
	if _, err := f.personas.Write("go-reviewer", instructions); err != nil {
		t.Fatal(err)
	}
	digest := f.writeProfile(t, "guard", guardProfile)

	if _, stderr, err := f.createClaude(t, "--profile", "guard"); err != nil {
		t.Fatalf("create: %v (stderr=%q)", err, stderr)
	}
	snapshot, err := f.personas.SnapshotPath(persona.Digest(instructions))
	if err != nil {
		t.Fatal(err)
	}
	settings := f.settingsPath(t, guardPermissions)
	want := []string{"--model", "opus", "--effort", "high", "--append-system-prompt-file", snapshot,
		"--settings", settings, "--", "review this"}
	if got := f.onlyArgvTail(t, aiModeClaude); !slices.Equal(got, want) {
		t.Fatalf("exec argv tail = %q, want %q", got, want)
	}
	content, err := os.ReadFile(settings)
	if err != nil || string(content) != `{"permissions":{"allow":["Read","Bash(git log *)"],"deny":["Edit","Write"]}}` {
		t.Fatalf("settings snapshot = %q, %v", content, err)
	}
	wantAnnotations := map[string]string{
		coremetadata.AnnotationAgentPersona:       "go-reviewer",
		coremetadata.AnnotationAgentPersonaDigest: persona.Digest(instructions),
		coremetadata.AnnotationAgentEffort:        "high",
		coremetadata.AnnotationAgentProfile:       "guard",
		coremetadata.AnnotationAgentProfileDigest: digest,
	}
	if got := f.createdAgent(t).Metadata.Annotations; !maps.Equal(got, wantAnnotations) {
		t.Fatalf("Agent annotations = %v, want %v", got, wantAnnotations)
	}
}

// TestExplicitCreateFlagsWinOverTheProfileItemsTheyOverlap is acceptance 2's
// precedence half: --instructions, --model and --effort replace the profile's
// items, which are then disclosed as overridden-by-flag, while the
// permissions still come from the profile.
func TestExplicitCreateFlagsWinOverTheProfileItemsTheyOverlap(t *testing.T) {
	t.Parallel()
	f := newProfileFixture(t)
	for name, body := range map[string]string{"go-reviewer": "profile one\n", "mine": "the flag's own\n"} {
		if _, err := f.personas.Write(name, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	f.writeProfile(t, "guard", guardProfile)
	stdout, stderr, err := f.createClaude(t, "--profile", "guard", "--instructions", "mine", "--model", "sonnet", "--effort", "low")
	if err != nil {
		t.Fatalf("create: %v (stderr=%q)", err, stderr)
	}
	snapshot, _ := f.personas.SnapshotPath(persona.Digest([]byte("the flag's own\n")))
	want := []string{"--model", "sonnet", "--effort", "low", "--append-system-prompt-file", snapshot,
		"--settings", f.settingsPath(t, guardPermissions), "--", "review this"}
	if got := f.onlyArgvTail(t, aiModeClaude); !slices.Equal(got, want) {
		t.Fatalf("exec argv tail = %q, want %q", got, want)
	}
	agent := f.createdAgent(t)
	if agent.Metadata.Annotations[coremetadata.AnnotationAgentPersona] != "mine" || agent.Metadata.Annotations[coremetadata.AnnotationAgentEffort] != "low" {
		t.Fatalf("Agent annotations = %v, want the flag's persona and effort", agent.Metadata.Annotations)
	}
	for _, item := range []string{"instructions", "model", "effort"} {
		line := "profile-not-applied item=" + item + " provider=claude reason=" + profileReasonOverriddenByFlag
		if !strings.Contains(stdout, line+"\n") {
			t.Fatalf("stdout = %q, want %q", stdout, line)
		}
	}
}

// TestCreateAgentRoleLabelAppliesTheOneProfileListingItAtCreationOnly is
// acceptance 3: a `role` label selects the one valid profile listing that
// role, and the label is never read again -- relabelling the Agent changes
// neither its annotations nor its resume.
func TestCreateAgentRoleLabelAppliesTheOneProfileListingItAtCreationOnly(t *testing.T) {
	t.Parallel()
	f := newProfileFixture(t)
	digest := f.writeProfile(t, "mapped", "roles = [\"reviewer\"]\nmodel = \"opus\"\n[permissions]\ndeny = [\"Edit\"]\n")
	if _, stderr, err := f.createClaude(t, "--label", "role=reviewer"); err != nil {
		t.Fatalf("create: %v (stderr=%q)", err, stderr)
	}
	settings := f.settingsPath(t, profile.Permissions{Deny: []string{"Edit"}})
	if got, want := f.onlyArgvTail(t, aiModeClaude), []string{"--model", "opus", "--settings", settings, "--", "review this"}; !slices.Equal(got, want) {
		t.Fatalf("exec argv tail = %q, want %q", got, want)
	}
	agent := f.createdAgent(t)
	if agent.Metadata.Annotations[coremetadata.AnnotationAgentProfile] != "mapped" ||
		agent.Metadata.Annotations[coremetadata.AnnotationAgentProfileDigest] != digest ||
		agent.Metadata.Labels["role"] != "reviewer" {
		t.Fatalf("Agent = %v labels %v, want profile mapped and the role label", agent.Metadata.Annotations, agent.Metadata.Labels)
	}

	// Relabel: the resume reads the annotation, never the label.
	annotations := maps.Clone(agent.Metadata.Annotations)
	_, argv, _, err := resumeProfileAgent(t, f.planner, annotations, map[string]string{"role": "someone-else"})
	if err != nil {
		t.Fatal(err)
	}
	if tail := execArgvTail(t, argv, aiModeClaude); !slices.Contains(tail, settings) {
		t.Fatalf("resume after relabel exec argv tail = %q, want the mapped profile's settings", tail)
	}
	_, argv, _, err = resumeProfileAgent(t, f.planner, nil, map[string]string{"role": "reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	if tail := execArgvTail(t, argv, aiModeClaude); slices.Contains(tail, "--settings") {
		t.Fatalf("resume of an unprofiled Agent labelled role=reviewer got a profile: %q", tail)
	}
}

// TestCreateAgentRefusesARoleSeveralProfilesClaim is acceptance 3's
// fail-closed half: a role two profiles list refuses with
// profile-role-claimed and names the two ways out, and nothing is created.
// Naming one of the claimants outright is refused too, because `profile list`
// marks both invalid; only --profile none creates.
func TestCreateAgentRefusesARoleSeveralProfilesClaim(t *testing.T) {
	t.Parallel()
	f := newProfileFixture(t)
	f.writeProfileFile(t, "a", "roles = [\"reviewer\"]\n[permissions]\ndeny = [\"Edit\"]\n")
	f.writeProfileFile(t, "b", "roles = [\"reviewer\"]\n")

	stdout, _, err := f.createClaude(t, "--label", "role=reviewer")
	if err == nil || !IsUsageError(err) {
		t.Fatalf("create = %v, want a usage refusal", err)
	}
	for _, want := range []string{profile.ReasonRoleClaimed, "--label role=reviewer", "remove the role from all but one of the listed profiles", "--profile none", "(a, b)", "nothing was created"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not mention %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "--profile <name>") {
		t.Fatalf("refusal %q still offers --profile <name> as a way out", err)
	}
	if stdout != "" || len(f.launcher.argv) != 0 || len(f.store.registry.Agents) != 2 {
		t.Fatalf("a refused create left stdout=%q launches=%d agents=%d", stdout, len(f.launcher.argv), len(f.store.registry.Agents))
	}

	for _, args := range [][]string{
		{"--label", "role=reviewer", "--profile", "a", "--name", "named"},
		{"--profile", "b", "--name", "named"},
	} {
		stdout, _, err := f.createClaude(t, args...)
		if err == nil || !IsUsageError(err) || !strings.Contains(err.Error(), profile.ReasonRoleClaimed) || stdout != "" {
			t.Fatalf("%v over the conflict = %v (stdout %q), want an exit-2 %s refusal", args, err, stdout, profile.ReasonRoleClaimed)
		}
	}
	if len(f.launcher.argv) != 0 || len(f.store.registry.Agents) != 2 {
		t.Fatalf("refused creates launched %d and left %d Agents", len(f.launcher.argv), len(f.store.registry.Agents))
	}
	if _, _, err := f.createClaude(t, "--label", "role=reviewer", "--profile", "none", "--name", "plain"); err != nil {
		t.Fatalf("--profile none over the conflict: %v", err)
	}
	if plain := agentNamed(t, f.store, "win-alpha-review", "plain"); plain.Metadata.Annotations != nil {
		t.Fatalf("plain = %v, want no profile", plain.Metadata.Annotations)
	}
}

// TestCreateAgentRefusesAMissingOrInvalidExplicitProfile pins that an
// explicit --profile that cannot be applied is a usage refusal (exit 2)
// carrying the profile reason token, with nothing created.
func TestCreateAgentRefusesAMissingOrInvalidExplicitProfile(t *testing.T) {
	t.Parallel()
	f := newProfileFixture(t)
	f.writeProfileFile(t, "broken", "model = \"opus\"\nmodel = \"sonnet\"\n")
	f.writeProfileFile(t, "orphan", "instructions = \"gone\"\n")
	for _, test := range []struct{ name, reason string }{
		{"missing", profile.ReasonNotFound},
		{"broken", profile.ReasonSyntax},
		{"orphan", profile.ReasonInstructionsNotFound},
		{".hidden", profile.ReasonNameInvalid},
	} {
		stdout, _, err := f.createClaude(t, "--profile", test.name)
		if err == nil || !IsUsageError(err) || !strings.Contains(err.Error(), test.reason) || !strings.Contains(err.Error(), "nothing was created") {
			t.Fatalf("--profile %s = %v, want a usage refusal carrying %s", test.name, err, test.reason)
		}
		if stdout != "" {
			t.Fatalf("--profile %s wrote stdout %q", test.name, stdout)
		}
	}
	if _, _, err := f.createClaude(t, "--profile", ""); err == nil || !IsUsageError(err) {
		t.Fatalf("--profile \"\" = %v, want a usage refusal", err)
	}
	if len(f.launcher.argv) != 0 || len(f.store.registry.Agents) != 2 {
		t.Fatalf("refused creates launched %d and left %d Agents", len(f.launcher.argv), len(f.store.registry.Agents))
	}
}

// TestEveryRoleTheProfileValidatorAcceptsRoundTripsThroughTheCreateLabel is
// Task 1's role-value risk: every role a profile may list is spelled back
// unchanged by the `--label role=<value>` parse create uses, so a role
// mapping can always be selected.
func TestEveryRoleTheProfileValidatorAcceptsRoundTripsThroughTheCreateLabel(t *testing.T) {
	t.Parallel()
	for _, role := range []string{"reviewer", "a=b", "two words", "ünïcode", "x:y/z", "trailing=", "role=role", `quote\"d`} {
		spec, err := profile.Parse([]byte("roles = [\"" + role + "\"]\n"))
		if err != nil {
			t.Fatalf("role %q: the validator refused it: %v", role, err)
		}
		accepted := spec.Roles[0]
		labels, err := labelMap([]string{profileRoleLabel + "=" + accepted})
		if err != nil || labels[profileRoleLabel] != accepted {
			t.Fatalf("role %q: --label parse = %v, %v; want it unchanged", accepted, labels, err)
		}
	}
	for _, role := range []string{" padded", "padded ", ""} {
		if _, err := profile.Parse([]byte("roles = [\"" + role + "\"]\n")); err == nil {
			t.Fatalf("role %q: the validator accepted a value the label parse would change", role)
		}
	}
	// And end to end, through a create.
	f := newProfileFixture(t)
	f.writeProfile(t, "odd", "roles = [\"a=b c\"]\nmodel = \"opus\"\n")
	if _, _, err := f.createClaude(t, "--label", "role=a=b c"); err != nil {
		t.Fatal(err)
	}
	if got := f.createdAgent(t).Metadata.Annotations[coremetadata.AnnotationAgentProfile]; got != "odd" {
		t.Fatalf("role a=b c selected profile %q, want odd", got)
	}
}

// TestCreateResultsDiscloseOnlyTheProfileItemsNotApplied is acceptance 4:
// the human result and the receipt JSON name the profile and list every item
// not applied -- sandbox and approval on Claude -- and never an applied one;
// a projection with no room for it discloses on stderr.
func TestCreateResultsDiscloseOnlyTheProfileItemsNotApplied(t *testing.T) {
	t.Parallel()
	f := newProfileFixture(t)
	if _, err := f.personas.Write("go-reviewer", []byte("x\n")); err != nil {
		t.Fatal(err)
	}
	digest := f.writeProfile(t, "guard", guardProfile)

	stdout, _, err := f.createClaude(t, "--profile", "guard")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	want := []string{
		"profile name=guard digest=" + digest,
		"profile-not-applied item=permissions.sandbox provider=claude reason=" + profileReasonClaudeSandboxBashOnly,
		"profile-not-applied item=permissions.approval provider=claude reason=" + profileReasonClaudeNoPermissionMode,
	}
	if len(lines) != 2+len(want) || !slices.Equal(lines[2:], want) || lines[0] != "agent/agent-test-1 created" || !strings.HasPrefix(lines[1], "receipt ") {
		t.Fatalf("stdout lines = %q, want the created line, the receipt line, then %q", lines, want)
	}
	for _, applied := range []string{"item=model", "item=effort", "item=instructions", "allow", "deny"} {
		if strings.Contains(stdout, applied) {
			t.Fatalf("stdout lists applied item %q: %q", applied, stdout)
		}
	}

	stdout, _, err = f.createClaude(t, "--profile", "guard", "--name", "json", "-o", "receipt")
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Profile *cli.ReceiptProfile `json:"profile"`
	}
	if err := json.Unmarshal([]byte(stdout), &receipt); err != nil || receipt.Profile == nil {
		t.Fatalf("receipt = %q (%v), want a profile field", stdout, err)
	}
	wantProfile := cli.ReceiptProfile{Name: "guard", Digest: digest, NotApplied: []cli.ReceiptProfileItem{
		{Item: "permissions.sandbox", Provider: "claude", Reason: profileReasonClaudeSandboxBashOnly},
		{Item: "permissions.approval", Provider: "claude", Reason: profileReasonClaudeNoPermissionMode},
	}}
	if got := *receipt.Profile; got.Name != wantProfile.Name || got.Digest != wantProfile.Digest || !slices.Equal(got.NotApplied, wantProfile.NotApplied) {
		t.Fatalf("receipt profile = %+v, want %+v", got, wantProfile)
	}

	stdout, stderr, err := f.createClaude(t, "--profile", "guard", "--name", "paneid", "-o", "pane-id")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "profile") || !strings.Contains(stderr, "projmux: "+want[1]+"\n") {
		t.Fatalf("pane-id: stdout=%q stderr=%q, want the disclosure on stderr only", stdout, stderr)
	}
}

// TestCreateResultsWithoutAProfileAreByteIdentical is acceptance 4's other
// half: with no profile applied, the human output and the receipt JSON are
// what they were before profiles existed -- no profile line and no profile
// key.
func TestCreateResultsWithoutAProfileAreByteIdentical(t *testing.T) {
	t.Parallel()
	for _, output := range [][]string{nil, {"-o", "receipt"}} {
		a, _, err := newProfileFixture(t).createClaude(t, output...)
		if err != nil {
			t.Fatal(err)
		}
		b, _, err := newProfileFixture(t).createClaude(t, append([]string{"--profile", "none"}, output...)...)
		if err != nil {
			t.Fatal(err)
		}
		if a != b || strings.Contains(a, "profile") {
			t.Fatalf("output %v: no profile = %q, --profile none = %q; want identical and profile-free", output, a, b)
		}
	}
	var buf bytes.Buffer
	receipt := cli.NewReceipt(cli.OperationCreateAgent, cli.ReceiptTarget{Kind: "Agent"}, cli.ReceiptEffects{})
	if err := receipt.WriteJSON(&buf); err != nil || strings.Contains(buf.String(), "profile") {
		t.Fatalf("a receipt with no profile renders %q (%v)", buf.String(), err)
	}
}

// TestUICreateResolvesTheProfileLikeTheTypedCreate is requirement 9: the UI
// Agent answer resolves, merges, and snapshots a profile exactly like
// `create agent`, refuses the same conflicts, and a resume-picker answer does
// no role mapping of its own.
func TestUICreateResolvesTheProfileLikeTheTypedCreate(t *testing.T) {
	t.Parallel()
	f := newProfileFixture(t)
	f.writeProfile(t, "guard", "model = \"opus\"\n[permissions]\nsandbox = \"read-only\"\ndeny = [\"Edit\"]\n")
	plan, err := f.create.prepareIntentAgent(aiModeClaude, resourceCreateFlags{profile: "guard", effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	got := plan.flags.profileLaunch
	if got.name != "guard" || plan.flags.model != "opus" || plan.flags.effort != "low" ||
		got.settings != f.settingsPath(t, profile.Permissions{Deny: []string{"Edit"}}) {
		t.Fatalf("UI plan = %+v model=%q effort=%q", got, plan.flags.model, plan.flags.effort)
	}
	if notices := got.notices(); !slices.Equal(notices, []string{"profile guard: permissions.sandbox not applied on claude (" + profileReasonClaudeSandboxBashOnly + ")"}) {
		t.Fatalf("UI notices = %q", notices)
	}

	f.writeProfileFile(t, "a", "roles = [\"r\"]\n")
	f.writeProfileFile(t, "b", "roles = [\"r\"]\n")
	if _, err := f.create.prepareIntentAgent(aiModeClaude, resourceCreateFlags{labels: repeatedFlag{"role=r"}}); err == nil || !strings.Contains(err.Error(), profile.ReasonRoleClaimed) {
		t.Fatalf("UI role conflict = %v, want %s", err, profile.ReasonRoleClaimed)
	}
	if plan, err := f.create.prepareIntentAgent(aiModeClaude, resourceCreateFlags{labels: repeatedFlag{"role=r"}, resumeConversation: personaResumeConversation}); err != nil || plan.flags.profileLaunch.active() {
		t.Fatalf("UI resume-picker answer resolved a profile: %+v, %v", plan.flags.profileLaunch, err)
	}
}

// resumeProfileAgent runs `agent resume` on a Claude Agent carrying
// annotations and labels through the real resume seam of planner, and
// returns the store, the planned argv (nil on refusal), stderr and the error.
func resumeProfileAgent(t *testing.T, planner *aiCommand, annotations, labels map[string]string) (*fakeResourceStore, []string, string, error) {
	t.Helper()
	store := newFakeResourceStore(t)
	target, _ := store.registry.Agent("agt-beta-codex")
	target.Spec.Provider = aiModeClaude
	ref, ok := coremetadata.NewAgentSessionRef(coremetadata.AgentSessionObservation{Provider: aiModeClaude, SessionID: personaResumeConversation}, resourceFixtureClock)
	if !ok {
		t.Fatal("claude session fixture was rejected")
	}
	target.Status.SessionRef = ref
	target.Metadata.Annotations = maps.Clone(annotations)
	target.Metadata.Labels = maps.Clone(labels)

	tmux := newFakeTmux()
	command, recorder, _, _ := newTestAgentResumeCommand(t, store, tmux)
	launcher := &exactArgvResumeLauncher{fakeResumeLauncher: recorder, planner: planner}
	command.rebind.launcher = launcher
	_, stderr, err := runRoute(t, command, "resume", "uid:"+target.Metadata.UID)
	calls := splitWindowCalls(tmux)
	if err != nil {
		if len(calls) != 0 || len(launcher.argv) != 0 {
			t.Fatalf("a refused resume launched: calls=%v argv=%v", calls, launcher.argv)
		}
		return store, nil, stderr, err
	}
	if len(launcher.argv) != 1 || len(calls) != 1 {
		t.Fatalf("resume planned %d argv and split %d times, want 1 and 1", len(launcher.argv), len(calls))
	}
	separator := slices.Index(calls[0], "--")
	if separator < 0 || !slices.Equal(calls[0][separator+1:], launcher.argv[0]) {
		t.Fatalf("launched child argv = %q, want exact %q", calls[0], launcher.argv[0])
	}
	return store, launcher.argv[0], stderr, nil
}

func profileAnnotations(name, digest string) map[string]string {
	return map[string]string{coremetadata.AnnotationAgentProfile: name, coremetadata.AnnotationAgentProfileDigest: digest}
}

// TestAgentResumeReappliesTheCurrentProfileAndRecordsItsDigest is acceptance
// 5 on `agent resume`: the profile is re-read by name, its current rules are
// passed as --settings, and the Agent records the digest it applied; the model
// is not re-sent; a profile that is gone or invalid -- a role-claimed one
// included -- refuses the resume with nothing launched.
func TestAgentResumeReappliesTheCurrentProfileAndRecordsItsDigest(t *testing.T) {
	t.Parallel()
	f := newProfileFixture(t)
	f.writeProfile(t, "guard", "model = \"opus\"\n[permissions]\ndeny = [\"Edit\"]\n")
	edited := f.writeProfile(t, "guard", "model = \"opus\"\n[permissions]\ndeny = [\"Edit\", \"Write\"]\n")

	store, argv, _, err := resumeProfileAgent(t, f.planner, profileAnnotations("guard", "sha256:stale"), nil)
	if err != nil {
		t.Fatal(err)
	}
	settings := f.settingsPath(t, profile.Permissions{Deny: []string{"Edit", "Write"}})
	if got, want := execArgvTail(t, argv, aiModeClaude), []string{"--settings", settings, "--resume", personaResumeConversation}; !slices.Equal(got, want) {
		t.Fatalf("resume exec argv tail = %q, want %q", got, want)
	}
	agent, _ := store.registry.Agent("agt-beta-codex")
	if got := agent.Metadata.Annotations; !maps.Equal(got, profileAnnotations("guard", edited)) {
		t.Fatalf("resumed Agent annotations = %v, want the current digest %s", got, edited)
	}

	// No annotation: byte-identical to the resume argv before profiles.
	_, plain, _, err := resumeProfileAgent(t, f.planner, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := execArgvTail(t, plain, aiModeClaude); !slices.Equal(got, []string{"--resume", personaResumeConversation}) {
		t.Fatalf("unprofiled resume exec argv tail = %q", got)
	}

	f.writeProfileFile(t, "broken", "effort = \"extreme\"\n")
	f.writeProfileFile(t, "claimed", "roles = [\"reviewer\"]\n[permissions]\ndeny = [\"Edit\"]\n")
	f.writeProfileFile(t, "rival", "roles = [\"reviewer\"]\n")
	for _, test := range []struct{ name, reason string }{
		{"gone", profile.ReasonNotFound},
		{"broken", profile.ReasonValueInvalid},
		{"claimed", profile.ReasonRoleClaimed},
	} {
		store, _, _, err := resumeProfileAgent(t, f.planner, profileAnnotations(test.name, "sha256:old"), nil)
		if err == nil || !strings.Contains(err.Error(), profileReasonResumeUnavailable) || !strings.Contains(err.Error(), test.reason) {
			t.Fatalf("resume with profile %s = %v, want %s (%s)", test.name, err, profileReasonResumeUnavailable, test.reason)
		}
		agent, _ := store.registry.Agent("agt-beta-codex")
		if agent.Metadata.Annotations[coremetadata.AnnotationAgentProfileDigest] != "sha256:old" || agent.Status.PaneRef != "" {
			t.Fatalf("a refused resume changed the Agent: %v paneRef=%q", agent.Metadata.Annotations, agent.Status.PaneRef)
		}
	}
}

// profileTopologyLauncher is the topology fake whose resume planning is the
// real planner.
type profileTopologyLauncher struct {
	*fakeTopologyAgentLauncher
	planner *aiCommand
	argv    [][]string
}

func (l *profileTopologyLauncher) PlanAgentResume(provider string, workspace coremetadata.AgentWorkspace, conversationID string, annotations map[string]string) (agentResumeLaunch, error) {
	launch, err := l.planner.PlanAgentResume(provider, workspace, conversationID, annotations)
	if err == nil {
		l.argv = append(l.argv, slices.Clone(launch.argv))
	}
	return launch, err
}

// TestTopologyReplayReappliesTheCurrentProfileAndRecordsItsDigest is
// acceptance 5 on Continue/topology replay: the replayed Agent launches with
// the current profile's --settings and records its digest; a profile that is
// gone, or now role-claimed, leaves the Agent unrestored with
// profile-resume-unavailable.
func TestTopologyReplayReappliesTheCurrentProfileAndRecordsItsDigest(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"current", "gone", "claimed"} {
		gone := state != "current"
		command, store, _, _, root, _ := newTopologyMaterializeFixture(t)
		planner := agentLaunchArgvTestCommand(t)
		paths, err := configPaths(planner.homeDir, planner.lookupEnv)
		if err != nil {
			t.Fatal(err)
		}
		digest := ""
		if !gone {
			entry, err := profile.NewDefaultStore(paths).Write("guard", []byte("[permissions]\nallow = [\"Read\"]\n"))
			if err != nil {
				t.Fatal(err)
			}
			digest = entry.Digest
		}
		if state == "claimed" {
			for _, name := range []string{"guard", "rival"} {
				path := filepath.Join(paths.ConfigDir, profile.DirName, name+profile.FileExt)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("roles = [\"reviewer\"]\n[permissions]\nallow = [\"Read\"]\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
		launcher := &profileTopologyLauncher{fakeTopologyAgentLauncher: command.agents.(*fakeTopologyAgentLauncher), planner: planner}
		command.agents = launcher
		agent := addTopologyFixtureAgent(t, store, topologyFixtureAgent{name: "claude", provider: "claude", cwd: root, ref: claudeConversationRef("conv-claude-1")})
		stored, _ := store.registry.Agent(agent.Metadata.UID)
		stored.Metadata.Annotations = profileAnnotations("guard", "sha256:stale")
		markTopologyAgentInterrupted(t, store, agent.Metadata.UID, "")

		_, stderr, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "beta", "-o", "json")
		if err != nil {
			t.Fatalf("materialize: %v (%s)", err, stderr)
		}
		after, _ := store.registry.Agent(agent.Metadata.UID)
		if gone {
			reason := profile.ReasonNotFound
			if state == "claimed" {
				reason = profile.ReasonRoleClaimed
			}
			if len(launcher.argv) != 0 || after.Status.Phase == coremetadata.PhaseRunning || !strings.Contains(stderr, profileReasonResumeUnavailable) ||
				!strings.Contains(stderr, reason) || after.Metadata.Annotations[coremetadata.AnnotationAgentProfileDigest] != "sha256:stale" {
				t.Fatalf("%s profile: argv=%v phase=%s annotations=%v stderr=%q", state, launcher.argv, after.Status.Phase, after.Metadata.Annotations, stderr)
			}
			continue
		}
		content, _ := profile.SettingsContent(profile.Permissions{Allow: []string{"Read"}})
		want := []string{"--settings", profile.SettingsSnapshotPath(paths.StateDir, content), "--resume", "conv-claude-1"}
		if len(launcher.argv) != 1 || !slices.Equal(execArgvTail(t, launcher.argv[0], aiModeClaude), want) {
			t.Fatalf("replay argv = %q, want exec tail %q", launcher.argv, want)
		}
		if after.Status.Phase != coremetadata.PhaseRunning || !maps.Equal(after.Metadata.Annotations, profileAnnotations("guard", digest)) {
			t.Fatalf("replayed Agent = %s %v, want Running with digest %s", after.Status.Phase, after.Metadata.Annotations, digest)
		}
	}
}

// TestResumePickerInheritsTheProfileAndAppliesItsCurrentContent is acceptance
// 5 on the resume picker: the new Agent inherits the holders' profile,
// launches with its current --settings, and records the current digest;
// holders that disagree about the profile, or a profile that is now
// role-claimed, refuse the create.
func TestResumePickerInheritsTheProfileAndAppliesItsCurrentContent(t *testing.T) {
	t.Parallel()
	f := newPickerLaunchValuesFixture(t, nil)
	paths, err := configPaths(f.planner.homeDir, f.planner.lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := profile.NewDefaultStore(paths).Write("guard", []byte("[permissions]\ndeny = [\"Edit\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	f.hold(t, "agt-beta-codex", aiModeClaude, personaResumeConversation, profileAnnotations("guard", "sha256:stale"), nil)
	agent, argv, _ := f.pick(t, false, aiModeClaude, personaResumeConversation, "")
	content, _ := profile.SettingsContent(profile.Permissions{Deny: []string{"Edit"}})
	want := []string{"--settings", profile.SettingsSnapshotPath(paths.StateDir, content), "--resume", personaResumeConversation}
	if got := execArgvTail(t, argv, aiModeClaude); !slices.Equal(got, want) {
		t.Fatalf("picker exec argv tail = %q, want %q", got, want)
	}
	if !maps.Equal(agent.Metadata.Annotations, profileAnnotations("guard", entry.Digest)) {
		t.Fatalf("picker Agent annotations = %v, want the current digest", agent.Metadata.Annotations)
	}

	d := newPickerLaunchValuesFixture(t, f.planner)
	d.hold(t, "agt-alpha-codex", aiModeClaude, personaResumeConversation, profileAnnotations("guard", entry.Digest), nil)
	d.hold(t, "agt-beta-codex", aiModeClaude, personaResumeConversation, nil, nil)
	agents := len(d.store.registry.Agents)
	var stdout, stderr bytes.Buffer
	_, err = d.create.createFromIntent(agentPaneIntent{
		producer: canonicalProducerResumePicker, provider: aiModeClaude, placement: "right",
		conversationID: personaResumeConversation, anchorPaneID: d.originID,
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), profileReasonResumeUnavailable) || len(d.store.registry.Agents) != agents || len(d.launcher.argv) != 0 {
		t.Fatalf("disagreeing holders = %v (agents %d -> %d, argv %v), want a %s refusal", err, agents, len(d.store.registry.Agents), d.launcher.argv, profileReasonResumeUnavailable)
	}
	// A profile that became role-claimed refuses the picker too.
	if _, err := profile.NewDefaultStore(paths).Write("rival", []byte("[permissions]\ndeny = [\"Edit\"]\n")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"guard", "rival"} {
		path := filepath.Join(paths.ConfigDir, profile.DirName, name+profile.FileExt)
		if err := os.WriteFile(path, []byte("roles = [\"reviewer\"]\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := newPickerLaunchValuesFixture(t, f.planner)
	c.hold(t, "agt-beta-codex", aiModeClaude, personaResumeConversation, profileAnnotations("guard", entry.Digest), nil)
	agents = len(c.store.registry.Agents)
	_, err = c.create.createFromIntent(agentPaneIntent{
		producer: canonicalProducerResumePicker, provider: aiModeClaude, placement: "right",
		conversationID: personaResumeConversation, anchorPaneID: c.originID,
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), profileReasonResumeUnavailable) || !strings.Contains(err.Error(), profile.ReasonRoleClaimed) ||
		len(c.store.registry.Agents) != agents || len(c.launcher.argv) != 0 {
		t.Fatalf("role-claimed profile = %v (agents %d -> %d, argv %v), want a %s refusal naming %s", err, agents, len(c.store.registry.Agents), c.launcher.argv, profileReasonResumeUnavailable, profile.ReasonRoleClaimed)
	}
}

// TestPlanAgentResumeCallersAreExactlyTheThreeResumePaths closes the caller
// set acceptance 5 covers: every non-test reference to PlanAgentResume in
// internal/app -- a call or a method value -- sits in one of the three resume
// paths that record the profile digest.
func TestPlanAgentResumeCallersAreExactlyTheThreeResumePaths(t *testing.T) {
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
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				if sel, ok := node.(*ast.SelectorExpr); ok && sel.Sel.Name == "PlanAgentResume" {
					got = append(got, name+":"+funcDeclName(fn))
				}
				return true
			})
		}
		// A reference outside any function body (a package-level method
		// value) would escape the walk above, so count those too.
		ast.Inspect(file, func(node ast.Node) bool {
			if fn, ok := node.(*ast.FuncDecl); ok && fn.Body != nil {
				return false
			}
			if sel, ok := node.(*ast.SelectorExpr); ok && sel.Sel.Name == "PlanAgentResume" {
				got = append(got, name+":<package level>")
			}
			return true
		})
	}
	sort.Strings(got)
	want := []string{
		"agent_resume.go:(*agentRebinder).rebind",
		"create_agent.go:(*createCommand).planAgentPaneLaunchWithResume",
		"registry_topology_agents.go:planTopologyAgentReplay",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("PlanAgentResume references = %q, want exactly %q", got, want)
	}
}

func funcDeclName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	receiver := fn.Recv.List[0].Type
	if star, ok := receiver.(*ast.StarExpr); ok {
		if ident, ok := star.X.(*ast.Ident); ok {
			return "(*" + ident.Name + ")." + fn.Name.Name
		}
	}
	if ident, ok := receiver.(*ast.Ident); ok {
		return ident.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}

// TestCreateClaudeAgentFromProfileKeepsAModelOutsideTheSuggestedList holds
// `agent models` to a suggestion: a Profile `model` outside the core list is
// still parsed and passed to Claude unchanged.
func TestCreateClaudeAgentFromProfileKeepsAModelOutsideTheSuggestedList(t *testing.T) {
	t.Parallel()
	const unlisted = "claude-opus-5"
	if slices.Contains(profile.ClaudeModels(), unlisted) {
		t.Fatalf("%q is listed; pick a name outside the list", unlisted)
	}
	f := newProfileFixture(t)
	f.writeProfile(t, "unlisted", "model = \""+unlisted+"\"\n")
	if _, stderr, err := f.createClaude(t, "--profile", "unlisted"); err != nil {
		t.Fatalf("create: %v (stderr=%q)", err, stderr)
	}
	if got, want := f.onlyArgvTail(t, aiModeClaude), []string{"--model", unlisted, "--", "review this"}; !slices.Equal(got, want) {
		t.Fatalf("exec argv tail = %q, want %q", got, want)
	}
}
