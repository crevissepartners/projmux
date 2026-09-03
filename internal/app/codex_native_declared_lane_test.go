package app

import (
	"context"
	"reflect"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
)

// TestDeclaredPlainCodexLaneNamesOnlyTheByDesignLanes fixes the declared
// vocabulary at its only producer.
//
// Two shapes reach the plain CLI lane on purpose: an explicit
// --interactive-only create and a picker resume row whose source is rollout.
// Empty-prompt creates are native and therefore deliberately absent here.
func TestDeclaredPlainCodexLaneNamesOnlyTheByDesignLanes(t *testing.T) {
	for _, test := range []struct {
		name     string
		provider string
		flags    resourceCreateFlags
		prompt   string
		want     string
	}{
		{name: "empty prompt native create", provider: aiModeCodex},
		{
			name: "interactive only", provider: aiModeCodex,
			flags:  resourceCreateFlags{interactiveOnly: true, payload: []string{"do the thing"}},
			prompt: "do the thing", want: codexNativeDeclaredInteractiveOnly,
		},
		{
			name: "interactive only with no payload", provider: aiModeCodex,
			flags: resourceCreateFlags{interactiveOnly: true}, want: codexNativeDeclaredInteractiveOnly,
		},
		{
			name: "prompted create", provider: aiModeCodex,
			flags: resourceCreateFlags{payload: []string{"do the thing"}}, prompt: "do the thing",
		},
		{
			name: "multi operand payload", provider: aiModeCodex,
			flags: resourceCreateFlags{payload: []string{"one", "two"}},
		},
		{
			name: "native stored resume", provider: aiModeCodex,
			flags: resourceCreateFlags{resumeConversation: "thread-1", resumeSource: aisessions.SourceCodexAppServer},
		},
		{
			name: "rollout picker resume", provider: aiModeCodex,
			flags: resourceCreateFlags{resumeConversation: "thread-1", resumeSource: aisessions.SourceCodexRollout},
			want:  codexNativeDeclaredRolloutCatalogResume,
		},
		{name: "claude empty create", provider: aiModeClaude},
		{name: "antigravity empty create", provider: aiModeAntigravity},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := declaredPlainCodexLane(test.provider, test.flags, test.prompt); got != test.want {
				t.Fatalf("declared lane = %q, want %q", got, test.want)
			}
		})
	}
}

// TestDeclaredLaneVocabularyIsClosedAgainstUnknownPaneValues keeps the
// declaration honest at the reading end.
//
// The declaration lives in a tmux pane option, which anything with a tmux
// client can write. If an arbitrary value counted as a declaration, writing one
// would be enough to hide a real unexplained native fallback from every
// diagnostics surface, so only the closed vocabulary is accepted.
func TestDeclaredLaneVocabularyIsClosedAgainstUnknownPaneValues(t *testing.T) {
	for _, declared := range codexNativeDeclaredReasons {
		if got := codexNativeDeclaredReason(declared); got != declared {
			t.Fatalf("declared reason %q normalized to %q", declared, got)
		}
		if strings.TrimSpace(declared) != declared || declared == "" {
			t.Fatalf("declared reason %q is not a bare token", declared)
		}
	}
	for _, forged := range []string{
		"", "   ", "native-fallback", "provider-hook", "ready", "empty-prompt",
		"interactive", "EMPTY-PROMPT-UPSTREAM-GATE", "empty-prompt-upstream-gate", "rollout",
	} {
		if got := codexNativeDeclaredReason(forged); got != "" {
			t.Fatalf("value %q was accepted as the declared reason %q", forged, got)
		}
	}
	// The unexplained reason is what a plain Codex Pane carries by default, so
	// it must never itself be a declaration.
	if codexNativeDeclaredReason(codexNativeUnexplainedReason) != "" {
		t.Fatal("the unexplained fallback reason doubles as a declaration")
	}
}

// TestManagedCodexAuthorityCensusSeparatesDeclaredFromUnexplainedFallback is
// the acceptance surface for "unexplained native fallback is zero".
//
// A payload-bearing managed Agent must converge on the control plane. An
// rollout or interactive-only Agent stays on hook observation, but with a
// typed declaration, so it is counted apart rather than inflating the number
// that is supposed to be zero.
func TestManagedCodexAuthorityCensusSeparatesDeclaredFromUnexplainedFallback(t *testing.T) {
	diagnostics := map[string]codexLifecycleAuthorityDiagnostic{
		"pane-native":       {Source: codexAuthorityControlPlane, Reason: "ready", EpochStatus: "active"},
		"pane-rollout":      {Source: codexAuthorityHook, Reason: codexNativeUnexplainedReason, Declared: codexNativeDeclaredRolloutCatalogResume},
		"pane-interactive":  {Source: codexAuthorityHook, Reason: codexNativeUnexplainedReason, Declared: codexNativeDeclaredInteractiveOnly},
		"pane-lost":         {Source: codexAuthorityHook, Reason: codexNativeUnexplainedReason},
		"pane-pending":      {Source: codexAuthorityPending, Reason: "connecting", EpochStatus: "pending"},
		"pane-invalidating": {Source: codexAuthorityInvalidating, Reason: "disconnected", EpochStatus: "active"},
		"pane-unreadable":   {Source: "unavailable", Reason: "tmux observation failed", EpochStatus: "unknown"},
	}
	registry := coremetadata.Registry{}
	for _, agent := range []struct {
		uid      string
		provider string
		pane     string
	}{
		{uid: "agent-native", provider: aiModeCodex, pane: "pane-native"},
		{uid: "agent-rollout", provider: aiModeCodex, pane: "pane-rollout"},
		{uid: "agent-interactive", provider: aiModeCodex, pane: "pane-interactive"},
		{uid: "agent-lost", provider: aiModeCodex, pane: "pane-lost"},
		{uid: "agent-pending", provider: aiModeCodex, pane: "pane-pending"},
		{uid: "agent-invalidating", provider: aiModeCodex, pane: "pane-invalidating"},
		{uid: "agent-unreadable", provider: aiModeCodex, pane: "pane-unreadable"},
		// Neither of these may reach the Codex census at all.
		{uid: "agent-claude", provider: aiModeClaude, pane: "pane-claude"},
		{uid: "agent-detached", provider: aiModeCodex},
	} {
		registry.Agents = append(registry.Agents, coremetadata.Agent{
			Metadata: coremetadata.ObjectMeta{UID: agent.uid},
			Spec:     coremetadata.AgentSpec{Provider: agent.provider},
			Status:   coremetadata.AgentStatus{PaneRef: agent.pane},
		})
	}
	looked := map[string]int{}
	census := censusCodexLifecycleAuthority(registry, func(paneUID string) codexLifecycleAuthorityDiagnostic {
		looked[paneUID]++
		return diagnostics[paneUID]
	})
	want := codexAuthorityCensus{
		Agents: 7, ControlPlane: 1, Pending: 1, Invalidating: 1,
		DeclaredHook: 2, UnexplainedHook: 1, Unavailable: 1,
	}
	if !reflect.DeepEqual(census, want) {
		t.Fatalf("census = %+v, want %+v", census, want)
	}
	if looked["pane-claude"] != 0 {
		t.Fatal("a sibling provider Agent was observed by the Codex census")
	}
	if len(looked) != 7 {
		t.Fatalf("observed %d panes, want exactly the 7 bound Codex Agents", len(looked))
	}
	// With the two by-design lanes declared, the number the contract requires
	// to be zero counts only the Agent that actually lost native authority.
	if census.UnexplainedHook != 1 {
		t.Fatalf("unexplained native fallback = %d, want only the lost binding", census.UnexplainedHook)
	}
	if census.Agents == 0 || census.Agents != census.ControlPlane+census.Pending+census.Invalidating+
		census.DeclaredHook+census.UnexplainedHook+census.Unavailable {
		t.Fatalf("census classes do not partition the managed Codex Agents: %+v", census)
	}
}

// TestDeclaredLaneReadsBackFromTheExactPaneOption closes the loop from the
// write site to the diagnostics read: a declared plain Codex Pane is reported
// as declared, and the same Pane without the option is reported as an
// unexplained native fallback.
func TestDeclaredLaneReadsBackFromTheExactPaneOption(t *testing.T) {
	const paneUID = "pan-alpha-codex"
	declared := observeCodexLifecycleAuthority(context.Background(), phase3StaticTmuxRunner{
		output: paneUID + "\x1f" + codexAuthorityHook + "\x1f\x1f" + codexNativeUnexplainedReason +
			"\x1f0\x1f0\x1f0\x1f" + codexNativeDeclaredRolloutCatalogResume,
	}, paneUID)
	if declared.Declared != codexNativeDeclaredRolloutCatalogResume || declared.unexplainedNativeFallback() {
		t.Fatalf("declared observation = %+v", declared)
	}
	undeclared := observeCodexLifecycleAuthority(context.Background(), phase3StaticTmuxRunner{
		output: paneUID + "\x1f" + codexAuthorityHook + "\x1f\x1f" + codexNativeUnexplainedReason + "\x1f0\x1f0\x1f0\x1f",
	}, paneUID)
	if undeclared.Declared != "" || !undeclared.unexplainedNativeFallback() {
		t.Fatalf("undeclared observation = %+v", undeclared)
	}
	forged := observeCodexLifecycleAuthority(context.Background(), phase3StaticTmuxRunner{
		output: paneUID + "\x1f" + codexAuthorityHook + "\x1f\x1f" + codexNativeUnexplainedReason +
			"\x1f0\x1f0\x1f0\x1fnot-a-declared-reason",
	}, paneUID)
	if forged.Declared != "" || !forged.unexplainedNativeFallback() {
		t.Fatalf("forged declaration was accepted: %+v", forged)
	}
}

// TestRetiredCodexAppServerWatchRouteIsRefused closes the retirement at the
// process boundary.
//
// A pre-upgrade binary's observer child keeps running its own old code, and
// nothing this release does can stop it; the exact-binding guard is what keeps
// it from writing to a replaced activation. What this release can guarantee is
// that it never revives the retired producer: the route the legacy observer was
// spawned under is not accepted, so no argv, hook, or script can start one
// against a current binary.
func TestRetiredCodexAppServerWatchRouteIsRefused(t *testing.T) {
	command := &aiCommand{}
	var stdout, stderr strings.Builder
	err := command.runIngest([]string{"codex-appserver-watch",
		"--agent-uid", "agent-1", "--pane-uid", "pane-1", "--pane", "%1",
		"--generation", "generation-1", "--thread", "thread-1",
		"--tmux-socket-name", "projmux",
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "unknown internal agent-hook ingest source") {
		t.Fatalf("the retired route was accepted: err=%v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("the retired route produced output: %q", stdout.String())
	}
	if codexNativeLifecycleIngestRoute == "codex-appserver-watch" {
		t.Fatal("the surviving route still names the retired app-server proxy producer")
	}
}

// TestPlainCodexCreatesWriteTheirDeclarationOntoTheExactPane closes the write
// half of the declared plain lane, through the real create route and the real
// pane binder.
//
// The read half and the vocabulary are pinned above, but neither would notice
// if the create route stopped passing the declaration to the binder, or the
// binder stopped writing it. Then every by-design plain Agent would silently
// start counting as an unexplained native fallback again, which is the exact
// signal this phase exists to keep meaningful.
func TestPlainCodexCreatesWriteTheirDeclarationOntoTheExactPane(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "interactive only",
			args: []string{"agent", "--provider", "codex", "--project", "alpha", "--window", "main",
				"--interactive-only", "-o", "pane-id", "--", "do the thing"},
			want: codexNativeDeclaredInteractiveOnly,
		},
		{
			// A sibling provider has no native lane to opt out of, so it may
			// never carry a Codex declaration.
			name: "claude",
			args: []string{"agent", "--provider", "claude", "--project", "alpha", "--window", "main", "-o", "pane-id"},
			want: "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, planner := newTestAgentCreateCommand(t, store, tmux)
			create.agents = &productionBindingAgentLauncher{fakeAgentLauncher: planner, binder: testAICommand(t.TempDir())}
			create.codexNative = &fakeNativeThreadController{createErr: errFakeNativeUnavailable, fallback: true}
			create.resumes = &fakeNativeResumeLauncher{
				fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: &fakeNativePaneLauncher{},
			}
			stdout, stderr, err := runRoute(t, create, test.args...)
			if err != nil || stderr != "" {
				t.Fatalf("create stdout=%q stderr=%q err=%v", stdout, stderr, err)
			}
			paneID := strings.TrimSpace(stdout)
			_, _, pane := tmux.pane(paneID)
			if pane == nil {
				t.Fatalf("create reported pane %q, which the tmux ledger does not have", paneID)
			}
			if got := pane.opts[aiPaneCodexDeclaredOption]; got != test.want {
				t.Fatalf("%s = %q, want %q", aiPaneCodexDeclaredOption, got, test.want)
			}
			if test.want == "" {
				return
			}
			// The declaration explains a hook observation; it never claims one
			// that does not exist.
			if got := pane.opts[aiPaneCodexAuthorityOption]; got != codexAuthorityHook {
				t.Fatalf("declared lane authority = %q, want %q", got, codexAuthorityHook)
			}
			if got := pane.opts[aiPaneCodexReasonOption]; got != codexNativeUnexplainedReason {
				t.Fatalf("declared lane reason = %q, want %q", got, codexNativeUnexplainedReason)
			}
			// And the declaration is what turns that observation from
			// unexplained into declared.
			diagnostic := codexLifecycleAuthorityDiagnostic{
				Source:   pane.opts[aiPaneCodexAuthorityOption],
				Reason:   pane.opts[aiPaneCodexReasonOption],
				Declared: codexNativeDeclaredReason(pane.opts[aiPaneCodexDeclaredOption]),
			}
			if diagnostic.unexplainedNativeFallback() {
				t.Fatalf("a declared plain lane still counts as unexplained: %+v", diagnostic)
			}
		})
	}
}
