package app

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/profile"
)

// profileInstructionsCommand is `projmux <noun>` (instructions or persona)
// rooted at the HOME of homeDir.
func profileInstructionsCommand(noun string, homeDir func() (string, error), lookupEnv func(string) string) *personaCommand {
	return &personaCommand{noun: noun, homeDir: homeDir, lookupEnv: lookupEnv, stdin: strings.NewReader("")}
}

// personaStoreOf is the persona store of create's HOME.
func personaStoreOf(t *testing.T, create *createCommand) persona.Store {
	t.Helper()
	paths, err := configPaths(create.homeDir, create.lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	return persona.NewDefaultStore(paths)
}

// removeInstructionsFile deletes stored instructions behind the CLI's back,
// the way a hand `rm` would, which is the one way left to orphan a profile.
func removeInstructionsFile(t *testing.T, store persona.Store, name string) {
	t.Helper()
	path, err := store.Path(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

// requireNothingCreated holds a refused create to zero launches, zero
// Registry writes, and zero tmux calls.
func (f profileFixture) requireNothingCreated(t *testing.T, label string, launches, writes, agents, tmuxCalls int) {
	t.Helper()
	if len(f.launcher.argv) != launches || f.store.writes != writes || len(f.store.registry.Agents) != agents || len(f.tmux.calls) != tmuxCalls {
		t.Fatalf("%s acted: launches %d->%d writes %d->%d agents %d->%d tmux calls %d->%d", label,
			launches, len(f.launcher.argv), writes, f.store.writes, agents, len(f.store.registry.Agents), tmuxCalls, len(f.tmux.calls))
	}
}

const guardedRoleProfile = `instructions = "x"
roles = ["reviewer"]

[permissions]
sandbox = "read-only"
approval = "never"
deny = ["Edit", "Write"]
`

// TestCreateAgentRefusesARoleAnInvalidProfileListsInsteadOfCreatingWithoutIt
// is the role fail-closed rule: a role whose one listing profile turned
// invalid -- here its instructions were removed by hand -- refuses the create
// with exit 2, naming the profile and its reason, and creates nothing.
// Before, the role selected no profile and the Agent started with default
// permissions. --profile none still creates without one.
func TestCreateAgentRefusesARoleAnInvalidProfileListsInsteadOfCreatingWithoutIt(t *testing.T) {
	t.Parallel()
	f := newProfileFixture(t)
	if _, err := f.personas.Write("x", []byte("You review.\n")); err != nil {
		t.Fatal(err)
	}
	f.writeProfile(t, "guarded", guardedRoleProfile)

	if _, stderr, err := f.createClaude(t, "--label", "role=reviewer", "--name", "mapped"); err != nil {
		t.Fatalf("create while the profile is valid: %v (stderr=%q)", err, stderr)
	}
	if got := agentNamed(t, f.store, "win-alpha-review", "mapped").Metadata.Annotations[coremetadata.AnnotationAgentProfile]; got != "guarded" {
		t.Fatalf("mapped Agent profile = %q, want guarded", got)
	}

	removeInstructionsFile(t, f.personas, "x")
	launches, writes, agents, calls := len(f.launcher.argv), f.store.writes, len(f.store.registry.Agents), len(f.tmux.calls)
	stdout, _, err := f.createClaude(t, "--label", "role=reviewer", "--name", "unguarded")
	if err == nil || !IsUsageError(err) || exitCodeOf(err) != 2 || stdout != "" {
		t.Fatalf("role create over an invalid profile = %v (exit %d, stdout %q), want an exit-2 refusal", err, exitCodeOf(err), stdout)
	}
	for _, want := range []string{
		"--label role=reviewer", profile.ReasonRoleProfileInvalid, `profile "guarded"`, `role "reviewer"`,
		profile.ReasonInstructionsNotFound, "`projmux profile set <name>`", "--profile none", "nothing was created",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not mention %q", err, want)
		}
	}
	f.requireNothingCreated(t, "refused role create", launches, writes, agents, calls)

	if _, _, err := f.createClaude(t, "--label", "role=reviewer", "--profile", "none", "--name", "plain"); err != nil {
		t.Fatalf("--profile none over the invalid profile: %v", err)
	}
	if plain := agentNamed(t, f.store, "win-alpha-review", "plain"); plain.Metadata.Annotations != nil || plain.Metadata.Labels["role"] != "reviewer" {
		t.Fatalf("plain = %v labels %v, want no profile and the role label", plain.Metadata.Annotations, plain.Metadata.Labels)
	}
}

// TestDeletingInstructionsAProfileUsesIsRefusedAndKeepsResumeWorking: an
// Agent created from a profile naming instructions keeps resuming, because
// deleting those instructions is refused (exit 2) while the profile names
// them -- on Claude and on the native Codex resume.
func TestDeletingInstructionsAProfileUsesIsRefusedAndKeepsResumeWorking(t *testing.T) {
	t.Parallel()
	t.Run("claude", func(t *testing.T) {
		t.Parallel()
		f := newProfileFixture(t)
		if _, err := f.personas.Write("x", []byte("You review.\n")); err != nil {
			t.Fatal(err)
		}
		f.writeProfile(t, "p", "instructions = \"x\"\n[permissions]\ndeny = [\"Edit\"]\n")
		if _, stderr, err := f.createClaude(t, "--profile", "p"); err != nil {
			t.Fatalf("create: %v (stderr=%q)", err, stderr)
		}
		annotations := maps.Clone(f.createdAgent(t).Metadata.Annotations)
		if annotations[coremetadata.AnnotationAgentProfile] != "p" || annotations[coremetadata.AnnotationAgentPersona] != "x" {
			t.Fatalf("created annotations = %v", annotations)
		}

		stdout, _, err := runPersona(profileInstructionsCommand("instructions", f.planner.homeDir, f.planner.lookupEnv), "delete", "x", "--yes")
		if err == nil || exitCodeOf(err) != 2 || stdout != "" ||
			!strings.Contains(err.Error(), profile.ReasonInstructionsInUse) || !strings.Contains(err.Error(), `profile "p"`) {
			t.Fatalf("instructions delete x --yes = %q, %v (exit %d), want an exit-2 %s refusal", stdout, err, exitCodeOf(err), profile.ReasonInstructionsInUse)
		}
		if _, err := f.personas.Load("x"); err != nil {
			t.Fatalf("a refused delete removed x: %v", err)
		}

		_, argv, _, err := resumeProfileAgent(t, f.planner, annotations, nil)
		if err != nil {
			t.Fatalf("resume after the refused delete: %v", err)
		}
		if tail := execArgvTail(t, argv, aiModeClaude); !slices.Contains(tail, f.settingsPath(t, profile.Permissions{Deny: []string{"Edit"}})) {
			t.Fatalf("resume exec argv tail = %q, want the profile's settings", tail)
		}
	})
	t.Run("codex native", func(t *testing.T) {
		t.Parallel()
		command, store, _, native, profiles := codexProfileResume(t, profileAnnotations("p", "sha256:created"))
		create := command.rebind.create
		if _, err := personaStoreOf(t, create).Write("x", []byte("You review.\n")); err != nil {
			t.Fatal(err)
		}
		digest := writeCodexProfile(t, profiles, "p", "instructions = \"x\"\n[permissions]\nsandbox = \"read-only\"\napproval = \"never\"\n")

		stdout, _, err := runPersona(profileInstructionsCommand("persona", create.homeDir, create.lookupEnv), "delete", "x", "--yes")
		if err == nil || exitCodeOf(err) != 2 || stdout != "" || !strings.Contains(err.Error(), profile.ReasonInstructionsInUse) {
			t.Fatalf("persona delete x --yes = %q, %v (exit %d), want an exit-2 %s refusal", stdout, err, exitCodeOf(err), profile.ReasonInstructionsInUse)
		}

		if _, _, err := runRoute(t, command, "resume", "uid:agt-beta-codex"); err != nil {
			t.Fatalf("native resume after the refused delete: %v", err)
		}
		if len(native.resumes) != 1 || native.resumes[0].policy != readonlyThreadPolicy {
			t.Fatalf("native resumes = %+v, want one carrying %+v", native.resumes, readonlyThreadPolicy)
		}
		if agent, _ := store.registry.Agent("agt-beta-codex"); !maps.Equal(agent.Metadata.Annotations, profileAnnotations("p", digest)) {
			t.Fatalf("resumed Agent annotations = %v, want digest %s", agent.Metadata.Annotations, digest)
		}
	})
}

// TestInstructionsAndPersonaDeleteRefuseInstructionsAProfileUses is the
// reference-integrity rule on both spellings: every user profile that parses
// and names the instructions -- valid or not -- blocks the delete with exit 2,
// is named, and nothing is deleted; a file that does not parse names nothing.
// Instructions no profile names delete as before.
func TestInstructionsAndPersonaDeleteRefuseInstructionsAProfileUses(t *testing.T) {
	t.Parallel()
	for _, noun := range []string{"instructions", "persona"} {
		t.Run(noun, func(t *testing.T) {
			t.Parallel()
			profiles, dir := newProfileTestCommand(t, "instructions = \"reviewer\"\nroles = [\"review\"]\n")
			if _, _, err := runProfile(profiles, "set", "guarded"); err != nil {
				t.Fatal(err)
			}
			for name, content := range map[string]string{
				"claimant": "instructions = \"reviewer\"\nroles = [\"review\"]\n",
				"broken":   "instructions = \"reviewer\"\nbogus = \"x\"\n",
			} {
				if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			command := profileInstructionsCommand(noun, profiles.homeDir, profiles.lookupEnv)
			personaDir := filepath.Join(filepath.Dir(dir), "personas")

			stdout, _, err := runPersona(command, "delete", "reviewer", "--yes")
			if err == nil || !IsUsageError(err) || exitCodeOf(err) != 2 || stdout != "" {
				t.Fatalf("%s delete reviewer --yes = %q, %v (exit %d), want an exit-2 refusal", noun, stdout, err, exitCodeOf(err))
			}
			want := noun + " delete reviewer: " + profile.ReasonInstructionsInUse + `: profiles "claimant", "guarded" name ` + noun + ` "reviewer"; ` +
				"point each at other instructions with `projmux profile set <name>`, or remove it with `projmux profile delete <name> --yes`; nothing was deleted"
			if err.Error() != want {
				t.Fatalf("refusal = %q\nwant      %q", err, want)
			}
			if _, statErr := os.Stat(filepath.Join(personaDir, "reviewer.md")); statErr != nil {
				t.Fatalf("a refused delete removed reviewer.md: %v", statErr)
			}

			if err := os.WriteFile(filepath.Join(personaDir, "spare.md"), []byte("Spare.\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			stdout, _, err = runPersona(command, "delete", "spare", "--yes")
			if err != nil || stdout != "deleted "+noun+" spare\n" {
				t.Fatalf("%s delete spare --yes = %q, %v", noun, stdout, err)
			}
			if _, statErr := os.Stat(filepath.Join(personaDir, "spare.md")); !os.IsNotExist(statErr) {
				t.Fatalf("delete of unused instructions left spare.md: %v", statErr)
			}
			if _, _, err := runPersona(command, "delete", "gone", "--yes"); persona.ReasonOf(err) != persona.ReasonNotFound || exitCodeOf(err) != 1 {
				t.Fatalf("%s delete of missing instructions = %v (exit %d), want %s exit 1", noun, err, exitCodeOf(err), persona.ReasonNotFound)
			}
		})
	}
}

// TestProfileListShowsTheCombinationEachProfileNames: list shows provider,
// instructions, model, effort, and roles; "-" where the profile names none
// (a provider-neutral profile included) and for every item of a file that
// does not parse; an invalid profile that parses still shows its roles.
func TestProfileListShowsTheCombinationEachProfileNames(t *testing.T) {
	t.Parallel()
	cmd, dir := newProfileTestCommand(t, "provider = \"codex\"\ninstructions = \"reviewer\"\nmodel = \"gpt-5\"\neffort = \"high\"\nroles = [\"review\"]\n")
	if _, _, err := runProfile(cmd, "set", "codexer"); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"orphan": "provider = \"claude\"\ninstructions = \"gone\"\nroles = [\"qa\"]\n",
		"broken": "roles = [\"ops\"]\nbogus = \"x\"\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stdout, _, err := runProfile(cmd, "list")
	if err != nil {
		t.Fatal(err)
	}
	rows := profileListRows(t, stdout)
	for name, want := range map[string][]string{
		"codexer":  {"codexer", "user", "codex", "reviewer", "gpt-5", "high", "review"},
		"orphan":   {"orphan", "user", "claude", "gone", "-", "-", "qa"},
		"broken":   {"broken", "user", "-", "-", "-", "-", "-"},
		"readonly": {"readonly", "builtin", "-", "-", "-", "-", "-"},
	} {
		if row := rows[name]; len(row) < 9 || !slices.Equal(row[:7], want) || !strings.HasPrefix(row[7], "sha256:") {
			t.Errorf("%s row = %q, want %q then a digest", name, row, want)
		}
	}
	if got := strings.Join(rows["orphan"][8:], " "); got != "no ("+profile.ReasonInstructionsNotFound+")" {
		t.Errorf("orphan validity = %q", got)
	}
	if got := strings.Join(rows["broken"][8:], " "); got != "no ("+profile.ReasonKeyUnknown+")" {
		t.Errorf("broken validity = %q", got)
	}
}

// TestProfileSetRefusesAnUnknownProviderAndWritesNothing: `provider` takes
// only a registered provider id; anything else exits 2 naming the accepted
// ones, and no file is written.
func TestProfileSetRefusesAnUnknownProviderAndWritesNothing(t *testing.T) {
	t.Parallel()
	cmd, dir := newProfileTestCommand(t, "provider = \"nope\"\n")
	stdout, _, err := runProfile(cmd, "set", "bad")
	if err == nil || !IsUsageError(err) || exitCodeOf(err) != 2 || stdout != "" || !strings.Contains(err.Error(), profile.ReasonProviderUnknown) {
		t.Fatalf("set provider nope = %q, %v (exit %d)", stdout, err, exitCodeOf(err))
	}
	for _, provider := range profile.Providers() {
		if !strings.Contains(err.Error(), provider) {
			t.Errorf("refusal %q does not name accepted provider %s", err, provider)
		}
	}
	if _, statErr := os.Stat(filepath.Join(dir, "bad.toml")); !os.IsNotExist(statErr) {
		t.Fatalf("a refused set wrote bad.toml: %v", statErr)
	}
}

// TestCreateAgentRefusesAProfileForAnotherProviderOnEveryLane: a profile
// naming a provider refuses a create of any other provider with exit 2 and
// profile-provider-mismatch, before any launch, Registry write, or tmux call,
// on the explicit --profile, the role label, the provider shortcuts, and the
// UI create. A profile naming the create's own provider applies as a
// provider-neutral one does.
func TestCreateAgentRefusesAProfileForAnotherProviderOnEveryLane(t *testing.T) {
	t.Parallel()
	f := newProfileFixture(t)
	f.writeProfile(t, "claudeonly", "provider = \"claude\"\nroles = [\"reviewer\"]\n[permissions]\ndeny = [\"Edit\"]\n")
	f.writeProfile(t, "codexonly", "provider = \"codex\"\n[permissions]\nsandbox = \"read-only\"\n")
	target := []string{"--project", "alpha", "--window", "review", "--", "review this"}

	for _, test := range []struct {
		lane, want string
		args       []string
	}{
		{"explicit", "profile \"claudeonly\" is for provider claude, not codex", append([]string{"agent", "--provider", "codex", "--profile", "claudeonly"}, target...)},
		{"role label", "profile \"claudeonly\" is for provider claude, not codex", append([]string{"agent", "--provider", "codex", "--label", "role=reviewer"}, target...)},
		{"shortcut codex", "profile \"claudeonly\" is for provider claude, not codex", append([]string{"codex", "--profile", "claudeonly"}, target...)},
		{"shortcut claude", "profile \"codexonly\" is for provider codex, not claude", append([]string{"claude", "--profile", "codexonly"}, target...)},
	} {
		launches, writes, agents, calls := len(f.launcher.argv), f.store.writes, len(f.store.registry.Agents), len(f.tmux.calls)
		stdout, _, err := runRoute(t, f.create, test.args...)
		if err == nil || !IsUsageError(err) || exitCodeOf(err) != 2 || stdout != "" {
			t.Fatalf("%s = %v (exit %d, stdout %q), want an exit-2 refusal", test.lane, err, exitCodeOf(err), stdout)
		}
		for _, want := range []string{profileReasonProviderMismatch, test.want, "--profile none", "nothing was created"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s refusal %q does not mention %q", test.lane, err, want)
			}
		}
		f.requireNothingCreated(t, test.lane, launches, writes, agents, calls)
	}

	launches, writes, agents, calls := len(f.launcher.argv), f.store.writes, len(f.store.registry.Agents), len(f.tmux.calls)
	if _, err := f.create.prepareIntentAgent(aiModeCodex, resourceCreateFlags{profile: "claudeonly"}); err == nil || !IsUsageError(err) ||
		!strings.Contains(err.Error(), profileReasonProviderMismatch) || !strings.Contains(err.Error(), "for provider claude, not codex") {
		t.Fatalf("UI create = %v, want %s", err, profileReasonProviderMismatch)
	}
	f.requireNothingCreated(t, "UI create", launches, writes, agents, calls)

	// The matching provider applies the profile, on the shortcut and the role label.
	if _, stderr, err := runRoute(t, f.create, append([]string{"claude", "--profile", "claudeonly", "--name", "direct"}, target...)...); err != nil {
		t.Fatalf("create claude --profile claudeonly: %v (stderr=%q)", err, stderr)
	}
	if _, stderr, err := f.createClaude(t, "--label", "role=reviewer", "--name", "mapped"); err != nil {
		t.Fatalf("role create on claude: %v (stderr=%q)", err, stderr)
	}
	for _, name := range []string{"direct", "mapped"} {
		if got := agentNamed(t, f.store, "win-alpha-review", name).Metadata.Annotations[coremetadata.AnnotationAgentProfile]; got != "claudeonly" {
			t.Fatalf("%s Agent profile = %q, want claudeonly", name, got)
		}
	}
	if plan, err := f.create.prepareIntentAgent(aiModeClaude, resourceCreateFlags{profile: "claudeonly"}); err != nil || plan.flags.profileLaunch.name != "claudeonly" {
		t.Fatalf("UI create on claude = %+v, %v", plan.flags.profileLaunch, err)
	}
}
