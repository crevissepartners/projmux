package app

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/profile"
)

// profileProviderTarget is the alpha/review scope every create below runs in.
var profileProviderTarget = []string{"--project", "alpha", "--window", "review"}

// createAgentArgs is `create agent` with flags, into alpha/review.
func createAgentArgs(flags ...string) []string {
	return append(append([]string{"agent"}, flags...), profileProviderTarget...)
}

// profileCreateRun is what one create printed and planned.
type profileCreateRun struct {
	stdout, stderr string
	err            error
	argv           [][]string
	agents         int
}

func runProfileCreate(t *testing.T, f profileFixture, args ...string) profileCreateRun {
	t.Helper()
	stdout, stderr, err := runRoute(t, f.create, args...)
	return profileCreateRun{stdout: stdout, stderr: stderr, err: err, argv: f.launcher.argv, agents: len(f.store.registry.Agents)}
}

// requireSameRun holds an omitted --provider to exactly what the explicit
// --provider spelling does: the same stdout, stderr, refusal text, exit code,
// and planned launches.
func requireSameRun(t *testing.T, label string, got, want profileCreateRun) {
	t.Helper()
	if got.stdout != want.stdout || got.stderr != want.stderr {
		t.Fatalf("%s printed stdout %q stderr %q, want stdout %q stderr %q", label, got.stdout, got.stderr, want.stdout, want.stderr)
	}
	if fmt.Sprint(got.err) != fmt.Sprint(want.err) || exitCodeOf(got.err) != exitCodeOf(want.err) || IsUsageError(got.err) != IsUsageError(want.err) {
		t.Fatalf("%s err = %v (exit %d), want %v (exit %d)", label, got.err, exitCodeOf(got.err), want.err, exitCodeOf(want.err))
	}
	if len(got.argv) != len(want.argv) || got.agents != want.agents {
		t.Fatalf("%s planned %d launches and left %d Agents, want %d and %d", label, len(got.argv), got.agents, len(want.argv), want.agents)
	}
	for i := range got.argv {
		if !slices.Equal(got.argv[i], want.argv[i]) {
			t.Fatalf("%s launch %d = %q, want %q", label, i, got.argv[i], want.argv[i])
		}
	}
}

const codexProviderProfile = "provider = \"codex\"\nmodel = \"gpt-5\"\nroles = [\"reviewer\"]\n[permissions]\nsandbox = \"read-only\"\napproval = \"never\"\n"

// TestCreateAgentWithoutProviderTakesTheProviderOfTheProfileItSelects: a
// profile naming codex, selected by --profile or by the role label it lists,
// creates a Codex Agent when --provider is omitted -- the same Registry
// Agent, annotations, launch argv, and output as spelling `--provider codex`.
func TestCreateAgentWithoutProviderTakesTheProviderOfTheProfileItSelects(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		lane      string
		selecting []string
	}{
		{"explicit profile", []string{"--profile", "codexer"}},
		{"role label", []string{"--label", "role=reviewer"}},
	} {
		t.Run(test.lane, func(t *testing.T) {
			t.Parallel()
			f := newProfileFixture(t)
			digest := f.writeProfile(t, "codexer", codexProviderProfile)
			flags := append(slices.Clone(test.selecting), "--interactive-only")

			stdout, stderr, err := runRoute(t, f.create, createAgentArgs(append([]string{"--name", "omitted"}, flags...)...)...)
			if err != nil {
				t.Fatalf("create agent %q: %v (stderr=%q)", flags, err, stderr)
			}
			explicitStdout, explicitStderr, err := runRoute(t, f.create, createAgentArgs(append([]string{"--provider", "codex", "--name", "explicit"}, flags...)...)...)
			if err != nil {
				t.Fatalf("create agent --provider codex %q: %v (stderr=%q)", flags, err, explicitStderr)
			}
			if strings.ReplaceAll(stdout, "omitted", "explicit") != explicitStdout || stderr != explicitStderr {
				t.Fatalf("printed stdout %q stderr %q, want the --provider codex output %q %q", stdout, stderr, explicitStdout, explicitStderr)
			}
			if len(f.launcher.argv) != 2 || !slices.Equal(f.launcher.argv[0], f.launcher.argv[1]) {
				t.Fatalf("launches = %q, want two identical codex launches", f.launcher.argv)
			}
			if tail, want := execArgvTail(t, f.launcher.argv[0], aiModeCodex), []string{"-m", "gpt-5", "-s", "read-only", "-a", "never", "-C", "/srv/alpha"}; !slices.Equal(tail, want) {
				t.Fatalf("codex exec argv tail = %q, want %q", tail, want)
			}

			agent, explicit := agentNamed(t, f.store, "win-alpha-review", "omitted"), agentNamed(t, f.store, "win-alpha-review", "explicit")
			if agent.Spec.Provider != aiModeCodex || explicit.Spec.Provider != aiModeCodex {
				t.Fatalf("Agent providers = %q and %q, want %s", agent.Spec.Provider, explicit.Spec.Provider, aiModeCodex)
			}
			if !maps.Equal(agent.Metadata.Annotations, explicit.Metadata.Annotations) {
				t.Fatalf("Agent annotations = %v, want the --provider codex ones %v", agent.Metadata.Annotations, explicit.Metadata.Annotations)
			}
			if agent.Metadata.Annotations[coremetadata.AnnotationAgentProfile] != "codexer" || agent.Metadata.Annotations[coremetadata.AnnotationAgentProfileDigest] != digest {
				t.Fatalf("Agent annotations = %v, want profile codexer at %s", agent.Metadata.Annotations, digest)
			}
		})
	}
}

// TestCreateAgentWithoutProviderOrAProviderProfileKeepsTheRequiresProviderRefusal:
// no profile, a provider-neutral profile, `--profile none` (even over a role
// label that would select a codex profile), and a role no profile lists all
// leave the provider undecided, and are refused exactly as a bare
// `create agent` is, with no launch, Registry write, or tmux call.
func TestCreateAgentWithoutProviderOrAProviderProfileKeepsTheRequiresProviderRefusal(t *testing.T) {
	t.Parallel()
	f := newProfileFixture(t)
	f.writeProfile(t, "neutral", "model = \"opus\"\n")
	f.writeProfile(t, "codexer", codexProviderProfile)
	wantText := "create agent requires --provider (" + strings.Join(cli.AgentProviders(), ", ") + "); the saved split mode is not a canonical default"

	bare := runProfileCreate(t, f, createAgentArgs()...)
	if bare.err == nil || bare.err.Error() != wantText || !IsUsageError(bare.err) || exitCodeOf(bare.err) != 2 || bare.stdout != "" {
		t.Fatalf("bare create agent = %v (exit %d, stdout %q), want %q", bare.err, exitCodeOf(bare.err), bare.stdout, wantText)
	}
	for _, test := range []struct {
		lane  string
		flags []string
	}{
		{"provider-neutral profile", []string{"--profile", "neutral"}},
		{"profile none", []string{"--profile", "none"}},
		{"profile none over a role", []string{"--profile", "none", "--label", "role=reviewer"}},
		{"unlisted role", []string{"--label", "role=nobody"}},
	} {
		launches, writes, agents, calls := len(f.launcher.argv), f.store.writes, len(f.store.registry.Agents), len(f.tmux.calls)
		requireSameRun(t, test.lane, runProfileCreate(t, f, createAgentArgs(test.flags...)...), bare)
		f.requireNothingCreated(t, test.lane, launches, writes, agents, calls)
	}
	if f.store.writes != 0 || len(f.tmux.calls) != 0 {
		t.Fatalf("refused creates wrote %d times and made %d tmux calls", f.store.writes, len(f.tmux.calls))
	}
}

// TestCreateAgentWithoutProviderHoldsTheProfileProviderToEveryArgvCheck: the
// provider a profile names is the provider every provider-specific argv
// refusal judges, so each is refused exactly as the explicit `--provider`
// spelling of it is, and nothing is created.
func TestCreateAgentWithoutProviderHoldsTheProfileProviderToEveryArgvCheck(t *testing.T) {
	t.Parallel()
	f := newProfileFixture(t)
	f.writeProfile(t, "claudeonly", "provider = \"claude\"\n")
	f.writeProfile(t, "codexer", codexProviderProfile)
	f.writeProfile(t, "antigravityonly", "provider = \"antigravity\"\n")

	for _, test := range []struct {
		lane, provider, want string
		flags                []string
	}{
		{"interactive-only on claude", aiModeClaude, "--interactive-only applies only to --provider codex",
			[]string{"--profile", "claudeonly", "--interactive-only"}},
		{"reply-only on codex", aiModeCodex, "--dialogue-reply-only requires a Claude Agent/provider",
			[]string{"--label", "role=reviewer", "--dialogue-reply-only"}},
		{"instructions on the codex plain lane", aiModeCodex, "applies to --provider codex only on a create with a prompt",
			[]string{"--profile", "codexer", "--interactive-only", "--instructions", "go-reviewer"}},
		{"model on antigravity", aiModeAntigravity, "--model and --effort apply only to --provider claude or codex",
			[]string{"--profile", "antigravityonly", "--model", "opus"}},
		{"instructions on antigravity", aiModeAntigravity, "applies only to --provider claude and --provider codex",
			[]string{"--profile", "antigravityonly", "--instructions", "go-reviewer"}},
	} {
		launches, writes, agents, calls := len(f.launcher.argv), f.store.writes, len(f.store.registry.Agents), len(f.tmux.calls)
		want := runProfileCreate(t, f, createAgentArgs(append([]string{"--provider", test.provider}, test.flags...)...)...)
		if want.err == nil || !IsUsageError(want.err) || !strings.Contains(want.err.Error(), test.want) {
			t.Fatalf("%s with --provider %s = %v, want a refusal naming %q", test.lane, test.provider, want.err, test.want)
		}
		requireSameRun(t, test.lane, runProfileCreate(t, f, createAgentArgs(test.flags...)...), want)
		f.requireNothingCreated(t, test.lane, launches, writes, agents, calls)
	}
}

// TestCreateAgentWithoutProviderSurfacesTheRefusalOfAProfileItCannotResolve:
// a profile that does not exist, one that is invalid, and a role several
// profiles claim are refused with the refusal the explicit `--provider`
// spelling gives for that profile, not the generic requires-provider one, and
// nothing is created.
func TestCreateAgentWithoutProviderSurfacesTheRefusalOfAProfileItCannotResolve(t *testing.T) {
	t.Parallel()
	f := newProfileFixture(t)
	f.writeProfileFile(t, "broken", "provider = \"codex\"\nbogus = \"x\"\n")
	f.writeProfileFile(t, "first", "provider = \"codex\"\nroles = [\"shared\"]\n")
	f.writeProfileFile(t, "second", "provider = \"codex\"\nroles = [\"shared\"]\n")

	for _, test := range []struct {
		lane, want string
		flags      []string
	}{
		{"missing profile", "--profile ghost", []string{"--profile", "ghost"}},
		{"invalid profile", profile.ReasonKeyUnknown, []string{"--profile", "broken"}},
		{"claimed role", profile.ReasonRoleClaimed, []string{"--label", "role=shared"}},
	} {
		launches, writes, agents, calls := len(f.launcher.argv), f.store.writes, len(f.store.registry.Agents), len(f.tmux.calls)
		got := runProfileCreate(t, f, createAgentArgs(test.flags...)...)
		if got.err == nil || strings.Contains(got.err.Error(), "requires --provider") ||
			!strings.Contains(got.err.Error(), test.want) || !strings.Contains(got.err.Error(), "nothing was created") {
			t.Fatalf("%s = %v, want the profile refusal naming %q", test.lane, got.err, test.want)
		}
		requireSameRun(t, test.lane, got, runProfileCreate(t, f, createAgentArgs(append([]string{"--provider", aiModeCodex}, test.flags...)...)...))
		f.requireNothingCreated(t, test.lane, launches, writes, agents, calls)
	}
}

// TestCreateAgentWithProviderStillRefusesArgvBeforeReadingTheProfile: with
// --provider spelled, an argv-only refusal still wins over a profile that
// cannot be resolved, because the profile is read only after every argv
// check. Without --provider the same argv reaches the profile first, since
// the profile is what names the provider.
func TestCreateAgentWithProviderStillRefusesArgvBeforeReadingTheProfile(t *testing.T) {
	t.Parallel()
	f := newProfileFixture(t)
	f.writeProfileFile(t, "broken", "provider = \"claude\"\nbogus = \"x\"\n")
	flags := []string{"--profile", "broken", "--interactive-only"}
	launches, writes, agents, calls := len(f.launcher.argv), f.store.writes, len(f.store.registry.Agents), len(f.tmux.calls)

	explicit := runProfileCreate(t, f, createAgentArgs(append([]string{"--provider", aiModeClaude}, flags...)...)...)
	if explicit.err == nil || !strings.Contains(explicit.err.Error(), "--interactive-only applies only to --provider codex") ||
		strings.Contains(explicit.err.Error(), profile.ReasonKeyUnknown) {
		t.Fatalf("--provider claude over a broken profile = %v, want the argv-only --interactive-only refusal", explicit.err)
	}
	omitted := runProfileCreate(t, f, createAgentArgs(flags...)...)
	if omitted.err == nil || !strings.Contains(omitted.err.Error(), profile.ReasonKeyUnknown) {
		t.Fatalf("omitted --provider over a broken profile = %v, want the %s profile refusal", omitted.err, profile.ReasonKeyUnknown)
	}
	f.requireNothingCreated(t, "refused creates", launches, writes, agents, calls)
}
