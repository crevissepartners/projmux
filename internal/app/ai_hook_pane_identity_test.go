package app

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// hookIdentityRegistry builds the exact Agent->Pane binding a hook has to
// resolve against: one managed Pane holding a runtime handle, owned by an Agent
// that points back at it.
func hookIdentityRegistry(paneUID, runtimeID string) coremetadata.Registry {
	return coremetadata.Registry{
		Agents: []coremetadata.Agent{{
			Metadata: coremetadata.ObjectMeta{UID: "agent-hook-identity", Name: "hook-identity-agent"},
			Status:   coremetadata.AgentStatus{PaneRef: paneUID},
		}},
		Panes: []coremetadata.Pane{{
			Metadata: coremetadata.ObjectMeta{UID: paneUID, Name: "hook-identity-pane"},
			Status: coremetadata.PaneStatus{Activation: coremetadata.PaneActivation{
				RuntimeID: runtimeID,
				AgentUID:  "agent-hook-identity",
			}},
		}},
	}
}

// paneBlindCommand is what a hook command looks like before it is handed an
// identity. Every assertion below is about the difference between the two.
func paneBlindCommand(command string) string {
	return strings.ReplaceAll(command, aiHookPaneArgument, "")
}

func TestProviderHookCommandsCarryTheirOwnPaneIdentity(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		command string
	}{
		{name: "codex", command: codexHookCommand},
		{name: "claude", command: claudeHookCommand},
		{name: "antigravity", command: antigravityManagedCommand("/opt/projmux/bin/projmux", "Stop")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(tc.command, aiHookPaneArgument) {
				t.Fatalf("%s hook command does not hand over a Pane: %s", tc.name, tc.command)
			}
			if paneBlindCommand(tc.command) == tc.command {
				t.Fatalf("%s hook command is unchanged by removing the pane argument", tc.name)
			}
		})
	}
}

// The pane argument is one shared spelling. A provider-specific transport would
// leave the other providers on the inherited-environment path that measurably
// fails for both of them.
func TestProviderHookPaneArgumentIsProviderNeutral(t *testing.T) {
	t.Parallel()

	if !strings.Contains(aiHookPaneArgument, internalActivationPaneUIDEnv) {
		t.Fatalf("pane argument does not read the activation envelope: %q", aiHookPaneArgument)
	}
	for _, provider := range []string{"codex", "claude", "antigravity", "Codex", "Claude", "Antigravity"} {
		if strings.Contains(aiHookPaneArgument, provider) {
			t.Fatalf("pane argument names provider %q: %q", provider, aiHookPaneArgument)
		}
	}
	// An app-server shared by several Panes carries no envelope. The argument
	// has to survive that expansion as an empty value rather than eating the
	// next word or failing the hook outright.
	if !strings.Contains(aiHookPaneArgument, ":-}") || !strings.Contains(aiHookPaneArgument, "--pane=") {
		t.Fatalf("pane argument does not tolerate an absent envelope: %q", aiHookPaneArgument)
	}
}

// A previously installed config carries the pane-blind spelling. It must stay
// projmux-owned or `agent integrate codex` reads it as hand-edited wiring and
// refuses the file.
func TestCodexIntegrationStillOwnsThePaneBlindHookCommand(t *testing.T) {
	t.Parallel()

	if !codexProjmuxHookCommand(priorCodexHookCommand) {
		t.Fatal("the pane-blind Codex hook command is no longer recognized as projmux-authored")
	}
	if !codexProjmuxHookCommand(codexHookCommand) {
		t.Fatal("the current Codex hook command is not recognized as projmux-authored")
	}
	if codexProjmuxHookCommand("codex hooks run something-else") {
		t.Fatal("an unrelated command is claimed as projmux-authored")
	}
}

func TestCodexIntegrationConvergesThePaneBlindHookCommandForward(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := testAICommand(home)
	config := codexHooksMarkerBegin + "\n" +
		"[[hooks.Stop]]\n" +
		"matcher = \"*\"\n" +
		"[[hooks.Stop.hooks]]\n" +
		"type = \"command\"\n" +
		"command = \"" + priorCodexHookCommand + "\"\n\n" +
		codexHooksMarkerEnd + "\n"
	path := filepath.Join(home, codexConfigRelativePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd.readFile = os.ReadFile

	plan, err := cmd.planCodexIntegration(false)
	if err != nil {
		t.Fatalf("planCodexIntegration() error = %v", err)
	}
	if plan.conflict != "" {
		t.Fatalf("planCodexIntegration() refused an owned config: %s", plan.conflict)
	}
	if !strings.Contains(plan.next, codexHookCommand) {
		t.Fatalf("plan did not converge to the pane-carrying command:\n%s", plan.next)
	}
	if strings.Contains(plan.next, "command = \""+priorCodexHookCommand+"\"") {
		t.Fatalf("plan kept the pane-blind command:\n%s", plan.next)
	}
}

// Acceptance 1: an app-server that inherited no tmux environment at all still
// attributes its hook to the Pane the command was handed.
func TestMatchAIPaneResolvesTheHandedPaneWithoutTmuxEnvironment(t *testing.T) {
	t.Parallel()

	cmd := testAICommand(t.TempDir())
	cmd.lookupEnv = func(string) string { return "" }
	cmd.readCommand = func(context.Context, string, ...string) ([]byte, error) { return nil, os.ErrNotExist }
	cmd.loadRegistry = func() (coremetadata.Registry, error) {
		return hookIdentityRegistry("pane-handed", "%77"), nil
	}

	for _, ref := range []string{"pane-handed", "%77"} {
		paneID, reason := cmd.matchAIPane(aiPaneMatchInput{ExplicitPane: ref})
		if paneID != "%77" || reason != "" {
			t.Fatalf("matchAIPane(%q) = %q, reason %q, want %%77", ref, paneID, reason)
		}
	}
}

// The handed Pane is the answer, not a first guess: falling through to the cwd
// ladder is exactly how an event lands on somebody else's Pane.
func TestMatchAIPanePrefersTheHandedPaneOverInheritedEvidence(t *testing.T) {
	t.Parallel()

	cmd := testAICommand(t.TempDir())
	cmd.lookupEnv = func(name string) string {
		if name == "TMUX_PANE" {
			return "%inherited"
		}
		return ""
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && len(args) > 0 && args[0] == "list-panes" {
			return []byte("%cwd\x1f/repo/projmux\x1fthread-1\x1fsession-1\n"), nil
		}
		return nil, os.ErrNotExist
	}
	cmd.loadRegistry = func() (coremetadata.Registry, error) {
		return hookIdentityRegistry("pane-handed", "%77"), nil
	}

	paneID, reason := cmd.matchAIPane(aiPaneMatchInput{
		ExplicitPane: "pane-handed",
		CWD:          "/repo/projmux",
		ThreadID:     "thread-1",
		SessionID:    "session-1",
	})
	if paneID != "%77" || reason != "" {
		t.Fatalf("matchAIPane() = %q, reason %q, want %%77", paneID, reason)
	}
}

// Acceptance 2: with nothing handed over, the established ladder is untouched.
func TestMatchAIPaneKeepsTheEstablishedFallbackWhenNothingWasHandedOver(t *testing.T) {
	t.Parallel()

	cmd := testAICommand(t.TempDir())
	cmd.lookupEnv = func(name string) string {
		if name == "TMUX_PANE" {
			return "%inherited"
		}
		return ""
	}
	cmd.loadRegistry = func() (coremetadata.Registry, error) {
		t.Fatal("fallback matching must not consult the registry")
		return coremetadata.Registry{}, nil
	}
	if paneID, reason := cmd.matchAIPane(aiPaneMatchInput{CWD: "/repo/projmux"}); paneID != "%inherited" || reason != "" {
		t.Fatalf("env step = %q, reason %q, want %%inherited", paneID, reason)
	}

	cmd.lookupEnv = func(string) string { return "" }
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && len(args) > 0 && args[0] == "list-panes" {
			return []byte("%cwd\x1f/repo/projmux\x1fthread-1\x1fsession-1\n%thread\x1f/repo/other\x1fthread-2\x1fsession-2\n"), nil
		}
		return nil, os.ErrNotExist
	}
	if paneID, reason := cmd.matchAIPane(aiPaneMatchInput{CWD: "/repo/projmux"}); paneID != "%cwd" || reason != "" {
		t.Fatalf("cwd step = %q, reason %q, want %%cwd", paneID, reason)
	}
	if paneID, reason := cmd.matchAIPane(aiPaneMatchInput{ThreadID: "thread-2"}); paneID != "%thread" || reason != "" {
		t.Fatalf("thread step = %q, reason %q, want %%thread", paneID, reason)
	}
	if paneID, reason := cmd.matchAIPane(aiPaneMatchInput{SessionID: "session-2"}); paneID != "%thread" || reason != "" {
		t.Fatalf("session step = %q, reason %q, want %%thread", paneID, reason)
	}
}

// Acceptance 4: a Pane that is gone is a failure, never a redirect onto a live
// Pane that merely shares a working directory.
func TestMatchAIPaneRefusesAHandedPaneThatIsGone(t *testing.T) {
	t.Parallel()

	live := coremetadata.Registry{Panes: []coremetadata.Pane{{
		Metadata: coremetadata.ObjectMeta{UID: "pane-other", Name: "other-pane"},
		Status: coremetadata.PaneStatus{Activation: coremetadata.PaneActivation{
			RuntimeID: "%9", AgentUID: "agent-other",
		}},
	}}}

	for _, tc := range []struct {
		name     string
		ref      string
		registry coremetadata.Registry
		want     string
	}{
		{name: "deleted pane", ref: "pane-handed", registry: live, want: aiPaneMatchReasonExplicitUnknown},
		{name: "recycled runtime handle", ref: "%77", registry: live, want: aiPaneMatchReasonExplicitUnknown},
		{
			name: "pane without a live runtime",
			ref:  "pane-handed",
			registry: coremetadata.Registry{Panes: []coremetadata.Pane{{
				Metadata: coremetadata.ObjectMeta{UID: "pane-handed", Name: "hook-identity-pane"},
			}}},
			want: aiPaneMatchReasonExplicitNoRuntime,
		},
		{
			name: "agent no longer points back",
			ref:  "pane-handed",
			registry: coremetadata.Registry{
				Agents: []coremetadata.Agent{{
					Metadata: coremetadata.ObjectMeta{UID: "agent-hook-identity", Name: "hook-identity-agent"},
					Status:   coremetadata.AgentStatus{PaneRef: "pane-other"},
				}},
				Panes: []coremetadata.Pane{{
					Metadata: coremetadata.ObjectMeta{UID: "pane-handed", Name: "hook-identity-pane"},
					Status: coremetadata.PaneStatus{Activation: coremetadata.PaneActivation{
						RuntimeID: "%77", AgentUID: "agent-hook-identity",
					}},
				}},
			},
			want: aiPaneMatchReasonExplicitStale,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := testAICommand(t.TempDir())
			cmd.lookupEnv = func(name string) string {
				if name == "TMUX_PANE" {
					return "%inherited"
				}
				return ""
			}
			cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == "tmux" && len(args) > 0 && args[0] == "list-panes" {
					return []byte("%9\x1f/repo/projmux\x1fthread-1\x1fsession-1\n"), nil
				}
				return nil, os.ErrNotExist
			}
			cmd.loadRegistry = func() (coremetadata.Registry, error) { return tc.registry, nil }

			paneID, reason := cmd.matchAIPane(aiPaneMatchInput{
				ExplicitPane: tc.ref,
				CWD:          "/repo/projmux",
				ThreadID:     "thread-1",
				SessionID:    "session-1",
			})
			if paneID != "" {
				t.Fatalf("matchAIPane() attributed the event to %q instead of failing", paneID)
			}
			if reason != tc.want {
				t.Fatalf("reason = %q, want %q", reason, tc.want)
			}
		})
	}
}

// Acceptance 3: every way attribution can fail names itself, so `no matching
// pane` stops meaning "something went wrong somewhere".
func TestAttributionFailureReasonsAreDistinctAndClosed(t *testing.T) {
	t.Parallel()

	seen := map[string]string{}
	record := func(t *testing.T, label, reason string) {
		t.Helper()
		if reason == "" {
			t.Fatalf("%s produced no reason", label)
		}
		if other, ok := seen[reason]; ok {
			t.Fatalf("%s reuses the reason of %s: %q", label, other, reason)
		}
		seen[reason] = label
	}

	// Nothing handed over and no Pane inventory to consult at all.
	noInventory := testAICommand(t.TempDir())
	noInventory.lookupEnv = func(string) string { return "" }
	noInventory.readCommand = func(context.Context, string, ...string) ([]byte, error) { return nil, os.ErrNotExist }
	_, reason := noInventory.matchAIPane(aiPaneMatchInput{CWD: "/repo/projmux"})
	record(t, "unreachable inventory", reason)

	// Nothing handed over, inventory readable, genuinely nothing matches.
	noMatch := testAICommand(t.TempDir())
	noMatch.lookupEnv = func(string) string { return "" }
	noMatch.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && len(args) > 0 && args[0] == "list-panes" {
			return []byte("%other\x1f/repo/other\x1fthread-9\x1fsession-9\n"), nil
		}
		return nil, os.ErrNotExist
	}
	_, reason = noMatch.matchAIPane(aiPaneMatchInput{CWD: "/repo/projmux"})
	record(t, "genuine no match", reason)
	if reason != aiPaneMatchReasonNoMatch {
		t.Fatalf("the established no-match reason changed to %q", reason)
	}

	// Handed a Pane with no registry behind it.
	noRegistry := testAICommand(t.TempDir())
	noRegistry.lookupEnv = func(string) string { return "" }
	_, reason = noRegistry.matchAIPane(aiPaneMatchInput{ExplicitPane: "pane-handed"})
	record(t, "unreachable registry", reason)

	gone := testAICommand(t.TempDir())
	gone.lookupEnv = func(string) string { return "" }
	gone.loadRegistry = func() (coremetadata.Registry, error) { return coremetadata.Registry{}, nil }
	_, reason = gone.matchAIPane(aiPaneMatchInput{ExplicitPane: "pane-handed"})
	record(t, "unregistered pane", reason)

	// Nothing handed over, no inventory, and a conversation the Registry does
	// not place. This is the shared provider host, and it is a different
	// failure from every one above it.
	unclaimed := sharedHostCommand(t, coremetadata.Registry{})
	_, reason = unclaimed.matchAIPane(aiPaneMatchInput{ThreadID: "thread-nobody-holds"})
	record(t, "unclaimed conversation", reason)

	twoPanes := conversationRegistry("pane-one", "%1", &coremetadata.CodexActivationBinding{ThreadID: "thread-shared"}, nil)
	alsoClaimed := conversationRegistry("pane-two", "%2", &coremetadata.CodexActivationBinding{ThreadID: "thread-shared"}, nil)
	twoPanes.Agents = append(twoPanes.Agents, alsoClaimed.Agents...)
	twoPanes.Panes = append(twoPanes.Panes, alsoClaimed.Panes...)
	_, reason = sharedHostCommand(t, twoPanes).matchAIPane(aiPaneMatchInput{ThreadID: "thread-shared"})
	record(t, "conversation held by two panes", reason)

	// Handed a live Pane that belongs to another provider: the decision itself,
	// and the composite the whole matcher reports once the fall-through behind
	// it has also failed. The second is its own token on purpose -- collapsing
	// it into the downstream step's reason would hide that the hook was handed
	// somebody else's Pane.
	foreign := inheritedIdentityCommand(t, providerPaneRegistry("pane-claude", "%53", aiModeClaude), "%53")
	_, reason = foreign.resolveExplicitAIPane(aiModeCodex, "pane-claude")
	record(t, "explicit pane of another provider", reason)
	_, reason = foreign.matchAIPane(aiPaneMatchInput{ExplicitPane: "pane-claude", Provider: aiModeCodex})
	record(t, "another provider's pane and no fallback", reason)

	// Every token stays a bounded phrase: no path, payload, or provider text.
	for token := range seen {
		if strings.ContainsAny(token, "/\\\"") || len(token) > 64 {
			t.Fatalf("reason token is not a bounded vocabulary entry: %q", token)
		}
		for _, provider := range []string{"codex", "claude", "antigravity"} {
			if strings.Contains(strings.ToLower(token), provider) {
				t.Fatalf("reason token names provider %q: %q", provider, token)
			}
		}
	}
}

// The route contract: the argument is accepted, optional, and never turns into a
// positional payload.
func TestHookIngestRoutesAcceptTheExplicitPaneArgument(t *testing.T) {
	t.Parallel()

	for _, route := range []string{"codex-hook", "claude-hook"} {
		t.Run(route, func(t *testing.T) {
			t.Parallel()
			var stderr strings.Builder
			pane, err := parseAIHookPaneArgument(route, []string{"--pane=pane-handed"}, &stderr)
			if err != nil || pane != "pane-handed" {
				t.Fatalf("parse(--pane=pane-handed) = %q, %v", pane, err)
			}
			pane, err = parseAIHookPaneArgument(route, []string{"--pane="}, &stderr)
			if err != nil || pane != "" {
				t.Fatalf("parse(--pane=) = %q, %v", pane, err)
			}
			pane, err = parseAIHookPaneArgument(route, nil, &stderr)
			if err != nil || pane != "" {
				t.Fatalf("parse() = %q, %v", pane, err)
			}
			if _, err := parseAIHookPaneArgument(route, []string{"payload.json"}, &stderr); err == nil {
				t.Fatal("a positional payload argument was accepted")
			}
		})
	}
}

// providerPaneResource builds one intact Agent->Pane binding that also records
// which provider the Agent runs. That record is the only thing separating an
// explicit identity a hook owns from one it inherited, so every fixture below
// states it explicitly -- including the empty spelling for a Pane the Registry
// records no provider for.
func providerPaneResource(paneUID, runtimeID, provider string, codex *coremetadata.CodexActivationBinding) (coremetadata.Agent, coremetadata.Pane) {
	agentUID := "agent-for-" + paneUID
	return coremetadata.Agent{
			Metadata: coremetadata.ObjectMeta{UID: agentUID, Name: "provider-agent-" + paneUID},
			Spec:     coremetadata.AgentSpec{Provider: provider},
			Status:   coremetadata.AgentStatus{PaneRef: paneUID},
		}, coremetadata.Pane{
			Metadata: coremetadata.ObjectMeta{UID: paneUID, Name: "provider-pane-" + paneUID},
			Status: coremetadata.PaneStatus{Activation: coremetadata.PaneActivation{
				RuntimeID: runtimeID,
				AgentUID:  agentUID,
				Codex:     codex,
			}},
		}
}

func providerPaneRegistry(paneUID, runtimeID, provider string) coremetadata.Registry {
	agent, pane := providerPaneResource(paneUID, runtimeID, provider, nil)
	return coremetadata.Registry{
		Agents: []coremetadata.Agent{agent},
		Panes:  []coremetadata.Pane{pane},
	}
}

// inheritedIdentityCommand is the app-server fixture: a hook process that
// inherited the whole activation envelope of the Pane that launched its host,
// including the tmux environment, and carries no Pane inventory of its own.
func inheritedIdentityCommand(t *testing.T, registry coremetadata.Registry, inheritedPane string) *aiCommand {
	t.Helper()
	cmd := testAICommand(t.TempDir())
	cmd.lookupEnv = func(name string) string {
		if name == "TMUX_PANE" {
			return inheritedPane
		}
		return ""
	}
	cmd.readCommand = func(context.Context, string, ...string) ([]byte, error) { return nil, os.ErrNotExist }
	cmd.loadRegistry = func() (coremetadata.Registry, error) { return registry, nil }
	cmd.updateRegistry = func(func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		t.Fatal("refusing an inherited identity wrote to the registry; this stage is a lookup only")
		return coremetadata.Registry{}, nil
	}
	return cmd
}

// Acceptance 1: a provider host is shared by several Panes and inherits the
// activation envelope of whichever Pane launched it. The explicit value it hands
// over is therefore live and resolvable and still not its own, and resolving it
// verbatim is exactly how a Codex hook lands on a Claude Pane.
func TestResolveExplicitAIPaneRefusesAnInheritedForeignIdentity(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		hook     string
		pane     string
		provider string
	}{
		{name: "codex hook handed a claude pane", hook: aiModeCodex, pane: "pane-claude", provider: aiModeClaude},
		{name: "claude hook handed a codex pane", hook: aiModeClaude, pane: "pane-codex", provider: aiModeCodex},
		{name: "antigravity hook handed a codex pane", hook: aiModeAntigravity, pane: "pane-codex", provider: aiModeCodex},
		{name: "codex hook handed an antigravity pane", hook: aiModeCodex, pane: "pane-antigravity", provider: aiModeAntigravity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			registry := providerPaneRegistry(tc.pane, "%53", tc.provider)
			cmd := inheritedIdentityCommand(t, registry, "%53")
			for _, ref := range []string{tc.pane, "%53"} {
				paneID, reason := cmd.resolveExplicitAIPane(tc.hook, ref)
				if paneID != "" {
					t.Fatalf("resolveExplicitAIPane(%q, %q) attributed the event to %q", tc.hook, ref, paneID)
				}
				if reason != aiPaneMatchReasonExplicitForeign {
					t.Fatalf("reason = %q, want %q", reason, aiPaneMatchReasonExplicitForeign)
				}
			}
		})
	}
}

// The same fixture through the whole matcher. Nothing else can answer here, so
// the refusal has to end as a failure rather than as the foreign Pane or as the
// `TMUX_PANE` the same envelope carried.
func TestMatchAIPaneNeverAttributesAnInheritedForeignIdentity(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		hook     string
		pane     string
		provider string
	}{
		{name: "codex hook handed a claude pane", hook: aiModeCodex, pane: "pane-claude", provider: aiModeClaude},
		{name: "claude hook handed a codex pane", hook: aiModeClaude, pane: "pane-codex", provider: aiModeCodex},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := inheritedIdentityCommand(t, providerPaneRegistry(tc.pane, "%53", tc.provider), "%53")
			paneID, reason := cmd.matchAIPane(aiPaneMatchInput{
				ExplicitPane: tc.pane,
				Provider:     tc.hook,
				CWD:          "/repo/projmux",
			})
			if paneID != "" {
				t.Fatalf("matchAIPane() attributed the event to %q", paneID)
			}
			if reason != aiPaneMatchReasonExplicitForeignOnly {
				t.Fatalf("reason = %q, want %q", reason, aiPaneMatchReasonExplicitForeignOnly)
			}
		})
	}
}

// Acceptance 3 regression: an unbound Window shell where somebody just ran a
// provider by hand records no provider at all. That is silence, not a
// contradiction, and Phase 2 behavior has to survive it untouched. An
// unrecognized spelling on either side is the same silence.
func TestMatchAIPaneKeepsAnExplicitPaneWithNoRecordedProvider(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		hook string
		pane string
	}{
		{name: "no recorded provider", hook: aiModeCodex, pane: ""},
		{name: "no recorded provider, claude hook", hook: aiModeClaude, pane: ""},
		{name: "matching provider", hook: aiModeCodex, pane: aiModeCodex},
		{name: "matching provider, mixed case record", hook: aiModeClaude, pane: "Claude"},
		{name: "unrecognized recorded provider", hook: aiModeCodex, pane: "some-future-provider"},
		{name: "hook names no provider", hook: "", pane: aiModeClaude},
		{name: "hook names an unrecognized provider", hook: "some-future-provider", pane: aiModeClaude},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := inheritedIdentityCommand(t, providerPaneRegistry("pane-handed", "%77", tc.pane), "")
			for _, ref := range []string{"pane-handed", "%77"} {
				paneID, reason := cmd.matchAIPane(aiPaneMatchInput{ExplicitPane: ref, Provider: tc.hook})
				if paneID != "%77" || reason != "" {
					t.Fatalf("matchAIPane(%q) = %q, reason %q, want %%77", ref, paneID, reason)
				}
			}
		})
	}
}

// Acceptance 2: a refused explicit value is not the end of the hook. It flows to
// the steps that answer from what the hook itself carries -- the Registry
// conversation record first, the established inventory ladder behind it -- and
// only a step that also fails ends as a named failure.
func TestMatchAIPaneContinuesPastARefusedInheritedIdentity(t *testing.T) {
	t.Parallel()

	claudeAgent, claudePane := providerPaneResource("pane-claude", "%53", aiModeClaude, nil)
	codexAgent, codexPane := providerPaneResource("pane-codex", "%78", aiModeCodex,
		&coremetadata.CodexActivationBinding{ThreadID: "thread-codex"})
	registry := coremetadata.Registry{
		Agents: []coremetadata.Agent{claudeAgent, codexAgent},
		Panes:  []coremetadata.Pane{claudePane, codexPane},
	}

	t.Run("registry conversation answers", func(t *testing.T) {
		t.Parallel()
		cmd := inheritedIdentityCommand(t, registry, "%53")
		paneID, reason := cmd.matchAIPane(aiPaneMatchInput{
			ExplicitPane: "pane-claude",
			Provider:     aiModeCodex,
			ThreadID:     "thread-codex",
		})
		if paneID != "%78" || reason != "" {
			t.Fatalf("matchAIPane() = %q, reason %q, want %%78", paneID, reason)
		}
	})

	t.Run("inventory answers when the registry cannot", func(t *testing.T) {
		t.Parallel()
		cmd := inheritedIdentityCommand(t, registry, "%53")
		cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "tmux" && len(args) > 0 && args[0] == "list-panes" {
				return []byte("%inventory\x1f/repo/projmux\x1fthread-inventory\x1fsession-inventory\n"), nil
			}
			return nil, os.ErrNotExist
		}
		paneID, reason := cmd.matchAIPane(aiPaneMatchInput{
			ExplicitPane: "pane-claude",
			Provider:     aiModeCodex,
			CWD:          "/repo/projmux",
			ThreadID:     "thread-no-pane-holds",
		})
		if paneID != "%inventory" || reason != "" {
			t.Fatalf("matchAIPane() = %q, reason %q, want %%inventory", paneID, reason)
		}
	})

	t.Run("nothing answers", func(t *testing.T) {
		t.Parallel()
		cmd := inheritedIdentityCommand(t, registry, "%53")
		paneID, reason := cmd.matchAIPane(aiPaneMatchInput{
			ExplicitPane: "pane-claude",
			Provider:     aiModeCodex,
			CWD:          "/repo/projmux",
			ThreadID:     "thread-no-pane-holds",
		})
		if paneID != "" {
			t.Fatalf("matchAIPane() attributed the event to %q", paneID)
		}
		if reason != aiPaneMatchReasonExplicitForeignOnly {
			t.Fatalf("reason = %q, want %q", reason, aiPaneMatchReasonExplicitForeignOnly)
		}
	})
}

// The inherited `TMUX_PANE` arrives in the very envelope whose Pane identity was
// just refused. Honouring it on the fall-through would reintroduce the same
// misattribution through a second door, so the fall-through skips that step
// entirely -- while the no-explicit path keeps it exactly as before.
func TestForeignExplicitFallThroughIgnoresTheInheritedEnvironment(t *testing.T) {
	t.Parallel()

	registry := providerPaneRegistry("pane-claude", "%53", aiModeClaude)
	inventory := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && len(args) > 0 && args[0] == "list-panes" {
			return []byte("%inventory\x1f/repo/projmux\x1fthread-1\x1fsession-1\n"), nil
		}
		return nil, os.ErrNotExist
	}

	refused := inheritedIdentityCommand(t, registry, "%53")
	refused.readCommand = inventory
	paneID, reason := refused.matchAIPane(aiPaneMatchInput{
		ExplicitPane: "pane-claude",
		Provider:     aiModeCodex,
		CWD:          "/repo/projmux",
	})
	if paneID == "%53" {
		t.Fatal("the fall-through honoured the inherited TMUX_PANE of the refused envelope")
	}
	if paneID != "%inventory" || reason != "" {
		t.Fatalf("matchAIPane() = %q, reason %q, want %%inventory", paneID, reason)
	}

	// The path that was handed nothing is untouched: inherited TMUX_PANE still
	// wins there, ahead of the inventory.
	blind := inheritedIdentityCommand(t, registry, "%53")
	blind.readCommand = inventory
	if paneID, reason := blind.matchAIPane(aiPaneMatchInput{Provider: aiModeCodex, CWD: "/repo/projmux"}); paneID != "%53" || reason != "" {
		t.Fatalf("no-explicit path = %q, reason %q, want %%53", paneID, reason)
	}
}

// Negative audit: the provider-coherence stage is a decision, not a binding. It
// writes nothing, and it leaves the Phase 6 Registry conversation index exactly
// as it found it -- that index reads the resource model and never a provider
// name, so recording a provider on an Agent cannot change what it returns.
func TestForeignExplicitRefusalLeavesTheRegistryPathUnchanged(t *testing.T) {
	t.Parallel()

	withProvider := coremetadata.Registry{}
	withoutProvider := coremetadata.Registry{}
	for _, spec := range []struct {
		paneUID   string
		runtimeID string
		provider  string
		thread    string
	}{
		{paneUID: "pane-codex", runtimeID: "%78", provider: aiModeCodex, thread: "thread-codex"},
		{paneUID: "pane-claude", runtimeID: "%53", provider: aiModeClaude, thread: ""},
	} {
		var codex *coremetadata.CodexActivationBinding
		if spec.thread != "" {
			codex = &coremetadata.CodexActivationBinding{ThreadID: spec.thread}
		}
		agent, pane := providerPaneResource(spec.paneUID, spec.runtimeID, spec.provider, codex)
		withProvider.Agents = append(withProvider.Agents, agent)
		withProvider.Panes = append(withProvider.Panes, pane)

		agent.Spec.Provider = ""
		withoutProvider.Agents = append(withoutProvider.Agents, agent)
		withoutProvider.Panes = append(withoutProvider.Panes, pane)
	}

	indexed := registeredConversationPanes(withProvider)
	if !maps.Equal(indexed, registeredConversationPanes(withoutProvider)) {
		t.Fatalf("the registry conversation index now depends on the recorded provider: %v", indexed)
	}
	if indexed["thread-codex"] != "%78" {
		t.Fatalf("registeredConversationPanes()[thread-codex] = %q, want %%78", indexed["thread-codex"])
	}

	// The refusal itself neither writes nor rebinds: inheritedIdentityCommand
	// fails the test on any registry write.
	cmd := inheritedIdentityCommand(t, withProvider, "%53")
	if paneID, reason := cmd.matchAIPane(aiPaneMatchInput{
		ExplicitPane: "pane-claude",
		Provider:     aiModeCodex,
		ThreadID:     "thread-codex",
	}); paneID != "%78" || reason != "" {
		t.Fatalf("matchAIPane() = %q, reason %q, want %%78", paneID, reason)
	}
}

// The second door. The fall-through's inventory step matches on working
// directory alone, and on a machine where every provider Pane sits in the same
// repository that step hands the event straight back to a Pane of the very
// provider just refused. Phase 8 is what routes a foreign-refused hook into that
// ladder at all, so the ladder's answer is checked for coherence too.
func TestForeignExplicitFallThroughDoesNotLandOnAnotherProvidersPane(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		hook       string
		handed     string
		foreign    string
		inventory  string
		foreignRow string
	}{
		{
			name: "codex hook, claude pane shares the cwd", hook: aiModeCodex,
			handed: "pane-claude", foreign: aiModeClaude,
			inventory: "pane-claude-sibling", foreignRow: "%54",
		},
		{
			name: "claude hook, codex pane shares the cwd", hook: aiModeClaude,
			handed: "pane-codex", foreign: aiModeCodex,
			inventory: "pane-codex-sibling", foreignRow: "%79",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			handedAgent, handedPane := providerPaneResource(tc.handed, "%53", tc.foreign, nil)
			rowAgent, rowPane := providerPaneResource(tc.inventory, tc.foreignRow, tc.foreign, nil)
			registry := coremetadata.Registry{
				Agents: []coremetadata.Agent{handedAgent, rowAgent},
				Panes:  []coremetadata.Pane{handedPane, rowPane},
			}
			cmd := inheritedIdentityCommand(t, registry, "%53")
			cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == "tmux" && len(args) > 0 && args[0] == "list-panes" {
					return []byte(tc.foreignRow + "\x1f/repo/projmux\x1f\x1f\n"), nil
				}
				return nil, os.ErrNotExist
			}

			paneID, reason := cmd.matchAIPane(aiPaneMatchInput{
				ExplicitPane: tc.handed,
				Provider:     tc.hook,
				CWD:          "/repo/projmux",
			})
			if paneID == tc.foreignRow {
				t.Fatalf("the cwd step handed the event to another provider's Pane %q", paneID)
			}
			if paneID != "" {
				t.Fatalf("matchAIPane() attributed the event to %q", paneID)
			}
			if reason != aiPaneMatchReasonExplicitForeignOnly {
				t.Fatalf("reason = %q, want %q", reason, aiPaneMatchReasonExplicitForeignOnly)
			}
		})
	}
}

// Fail-open is what keeps the Phase 2 contract intact: only a recorded and
// different provider refuses. A cwd-sharing Pane running the hook's own
// provider, and one the Registry records no provider for, both still answer.
func TestForeignExplicitFallThroughStillTakesACoherentLadderAnswer(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		rowPane string
	}{
		{name: "ladder row runs the hook's own provider", rowPane: aiModeCodex},
		{name: "ladder row records no provider", rowPane: ""},
		{name: "ladder row records an unrecognized provider", rowPane: "some-future-provider"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			handedAgent, handedPane := providerPaneResource("pane-claude", "%53", aiModeClaude, nil)
			rowAgent, rowPane := providerPaneResource("pane-row", "%80", tc.rowPane, nil)
			registry := coremetadata.Registry{
				Agents: []coremetadata.Agent{handedAgent, rowAgent},
				Panes:  []coremetadata.Pane{handedPane, rowPane},
			}
			cmd := inheritedIdentityCommand(t, registry, "%53")
			cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == "tmux" && len(args) > 0 && args[0] == "list-panes" {
					return []byte("%80\x1f/repo/projmux\x1f\x1f\n"), nil
				}
				return nil, os.ErrNotExist
			}
			paneID, reason := cmd.matchAIPane(aiPaneMatchInput{
				ExplicitPane: "pane-claude",
				Provider:     aiModeCodex,
				CWD:          "/repo/projmux",
			})
			if paneID != "%80" || reason != "" {
				t.Fatalf("matchAIPane() = %q, reason %q, want %%80", paneID, reason)
			}
		})
	}
}

// The Registry conversation record is checked the same way. A conversation the
// Registry places on a Pane of another provider is not taken either, and
// refusing it does not consume the ladder's turn behind it.
func TestForeignExplicitFallThroughDoesNotTakeAForeignConversationPane(t *testing.T) {
	t.Parallel()

	claudeAgent, claudePane := providerPaneResource("pane-claude", "%53", aiModeClaude, nil)
	claudeAgent.Status.SessionRef = &coremetadata.AgentSessionRef{
		Provider: aiModeClaude,
		Claude:   &coremetadata.ClaudeSessionRef{SessionID: "conversation-shared"},
	}
	codexAgent, codexPane := providerPaneResource("pane-codex", "%80", aiModeCodex, nil)
	registry := coremetadata.Registry{
		Agents: []coremetadata.Agent{claudeAgent, codexAgent},
		Panes:  []coremetadata.Pane{claudePane, codexPane},
	}

	// The record resolves, and it points at a Pane of the wrong provider.
	if held := registeredConversationPanes(registry)["conversation-shared"]; held != "%53" {
		t.Fatalf("fixture does not record the conversation on the foreign pane: %q", held)
	}

	t.Run("ladder answers behind the refused conversation", func(t *testing.T) {
		t.Parallel()
		cmd := inheritedIdentityCommand(t, registry, "%53")
		cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "tmux" && len(args) > 0 && args[0] == "list-panes" {
				return []byte("%80\x1f/repo/projmux\x1f\x1f\n"), nil
			}
			return nil, os.ErrNotExist
		}
		paneID, reason := cmd.matchAIPane(aiPaneMatchInput{
			ExplicitPane: "pane-claude",
			Provider:     aiModeCodex,
			CWD:          "/repo/projmux",
			SessionID:    "conversation-shared",
		})
		if paneID != "%80" || reason != "" {
			t.Fatalf("matchAIPane() = %q, reason %q, want %%80", paneID, reason)
		}
	})

	t.Run("nothing else answers", func(t *testing.T) {
		t.Parallel()
		cmd := inheritedIdentityCommand(t, registry, "%53")
		paneID, reason := cmd.matchAIPane(aiPaneMatchInput{
			ExplicitPane: "pane-claude",
			Provider:     aiModeCodex,
			SessionID:    "conversation-shared",
		})
		if paneID != "" {
			t.Fatalf("matchAIPane() attributed the event to %q", paneID)
		}
		if reason != aiPaneMatchReasonExplicitForeignOnly {
			t.Fatalf("reason = %q, want %q", reason, aiPaneMatchReasonExplicitForeignOnly)
		}
	})
}

// The global ladder is unchanged. With no explicit value there is no refused
// envelope and therefore no coherence question: a cwd match resolves exactly as
// it did before, foreign provider or not. This is the assertion that proves the
// second-door fix did not leak into `matchAIPaneFromInventory`.
func TestNoExplicitPathStillTakesACwdMatchRegardlessOfProvider(t *testing.T) {
	t.Parallel()

	claudeAgent, claudePane := providerPaneResource("pane-claude", "%53", aiModeClaude, nil)
	registry := coremetadata.Registry{
		Agents: []coremetadata.Agent{claudeAgent},
		Panes:  []coremetadata.Pane{claudePane},
	}
	inventory := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && len(args) > 0 && args[0] == "list-panes" {
			return []byte("%53\x1f/repo/projmux\x1fthread-1\x1fsession-1\n"), nil
		}
		return nil, os.ErrNotExist
	}

	for _, provider := range []string{aiModeCodex, aiModeClaude, aiModeAntigravity, ""} {
		cmd := inheritedIdentityCommand(t, registry, "")
		cmd.readCommand = inventory
		if paneID, reason := cmd.matchAIPane(aiPaneMatchInput{Provider: provider, CWD: "/repo/projmux"}); paneID != "%53" || reason != "" {
			t.Fatalf("no-explicit cwd step for provider %q = %q, reason %q, want %%53", provider, paneID, reason)
		}
		if paneID, reason := cmd.matchAIPane(aiPaneMatchInput{Provider: provider, ThreadID: "thread-1"}); paneID != "%53" || reason != "" {
			t.Fatalf("no-explicit thread step for provider %q = %q, reason %q, want %%53", provider, paneID, reason)
		}
	}
}
