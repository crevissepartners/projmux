package app

import (
	"context"
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

	// Every token stays a bounded phrase: no path, payload, or provider text.
	for token := range seen {
		if strings.ContainsAny(token, "/\\\"") || len(token) > 64 {
			t.Fatalf("reason token is not a bounded vocabulary entry: %q", token)
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
