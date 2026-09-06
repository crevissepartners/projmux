package app

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	corecap "github.com/crevissepartners/projmux/internal/core/aicapability"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestAgentCapabilitiesProviderProjectionIsStaticAndReadOnly(t *testing.T) {
	t.Parallel()

	for _, provider := range aiprovider.AgentProviders() {
		t.Run(string(provider), func(t *testing.T) {
			t.Parallel()
			// Keep every runtime/transport dependency nil. A provider-only read
			// succeeds only if it is a genuinely static projection.
			cmd := &agentCommand{loadRegistry: func() (coremetadata.Registry, error) {
				t.Fatal("provider-only capabilities must not read the Registry")
				return coremetadata.Registry{}, nil
			}}
			var stdout, stderr bytes.Buffer
			if err := cmd.Run([]string{"capabilities", "--provider", string(provider), "-o", "json"}, &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q", stderr.String())
			}
			var projection agentCapabilityProjection
			if err := json.Unmarshal(stdout.Bytes(), &projection); err != nil {
				t.Fatal(err)
			}
			if projection.Provider != provider || projection.Agent != nil || projection.Runtime != nil || len(projection.Capabilities) != len(aiprovider.AgentActions()) {
				t.Fatalf("projection = %#v", projection)
			}
			if strings.Contains(stdout.String(), `"available"`) || strings.Contains(stdout.String(), `"runtimeEligibility"`) {
				t.Fatalf("provider-only projection leaked runtime eligibility: %s", stdout.String())
			}
		})
	}
}

func TestAgentCapabilitiesExactProjectionSeparatesRuntimeEpochAndAvailability(t *testing.T) {
	t.Parallel()

	cmd, store, _ := exactControlCLICommand(t)
	controlLookup := &countingControlBinding{}
	reviewLookup := &countingReviewBinding{}
	reviewStarts := &countingReviewStarter{}
	providerCommands := &recordingArgv{}
	usageCommands := &recordingArgv{}
	generationCommands := &recordingArgv{}
	tmuxRunner := &exactControlRouteRunner{}
	routeLookups := 0
	providerCalls := 0
	cmd.controlBinding = controlLookup
	cmd.reviewBinding = reviewLookup
	cmd.reviews = reviewStarts
	cmd.ai = providerCommands
	cmd.usage = usageCommands
	cmd.codexUpgrade = generationCommands
	cmd.codexHandover = generationCommands
	cmd.controlRunner = tmuxRunner
	cmd.controlRoute = func(context.Context) (runtimeMutationRoute, error) {
		routeLookups++
		return runtimeMutationRoute{}, nil
	}
	cmd.controlCall = func(context.Context, string, coremetadata.CodexEndpointRef, codexLifecycleIdentity, agentControlRequest) (agentControlResponse, error) {
		providerCalls++
		return agentControlResponse{}, nil
	}
	before := store.snapshot()
	reads, writes, transactions := store.reads, store.writes, store.transactions
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"capabilities", "uid:agt-alpha-codex", "-o", "json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 || store.snapshot() != before || store.writes != writes || store.transactions != transactions || store.reads != reads+1 {
		t.Fatalf("capability read effects: stderr=%q reads=%d writes=%d transactions=%d changed=%t", stderr.String(), store.reads-reads, store.writes-writes, store.transactions-transactions, store.snapshot() != before)
	}
	if controlLookup.calls != 0 || reviewLookup.calls != 0 || reviewStarts.calls != 0 || routeLookups != 0 || providerCalls != 0 || len(tmuxRunner.calls) != 0 || len(providerCommands.calls) != 0 || len(usageCommands.calls) != 0 || len(generationCommands.calls) != 0 {
		t.Fatalf("capability read reached runtime: control=%d reviewLookup=%d reviewStart=%d routes=%d provider=%d tmux=%d providerCommands=%d usage=%d generation=%d",
			controlLookup.calls, reviewLookup.calls, reviewStarts.calls, routeLookups, providerCalls, len(tmuxRunner.calls), len(providerCommands.calls), len(usageCommands.calls), len(generationCommands.calls))
	}
	var projection agentCapabilityProjection
	if err := json.Unmarshal(stdout.Bytes(), &projection); err != nil {
		t.Fatal(err)
	}
	if projection.Agent == nil || projection.Agent.UID != "agt-alpha-codex" || projection.Runtime == nil || !projection.Runtime.Ready {
		t.Fatalf("exact projection = %#v", projection)
	}
	if projection.Runtime.ActivationGeneration != "generation-1" || projection.Runtime.ConnectionEpoch != 1 || projection.Runtime.BindingEpoch != 1 {
		t.Fatalf("runtime epoch = %#v", projection.Runtime)
	}
	if projection.Runtime.Evidence != "registry" || projection.Runtime.LiveVerified {
		t.Fatalf("runtime evidence = %#v, want Registry-only and not live-verified", projection.Runtime)
	}
	entries := map[string]agentCapabilityProjectionEntry{}
	for _, entry := range projection.Capabilities {
		entries[entry.Action] = entry
	}
	if entries["turn.start"].Available == nil || !*entries["turn.start"].Available || entries["message.send"].Available == nil || !*entries["message.send"].Available {
		t.Fatalf("native/coordination availability = start:%#v send:%#v", entries["turn.start"], entries["message.send"])
	}
	if entries["turn.start"].Evidence != "registry" || entries["message.send"].Evidence != "registry" {
		t.Fatalf("per-action evidence = start:%#v send:%#v", entries["turn.start"], entries["message.send"])
	}
}

func TestAgentCapabilitiesClaudeMessageCellsReflectMissingRegistrationLease(t *testing.T) {
	h := newSessionRefHarness(t, aiModeClaude)
	active := insideTmux(h.paneUID, "")
	cmd := &agentCommand{activeTarget: active.lookup, loadRegistry: func() (coremetadata.Registry, error) {
		return h.registry.Clone(), nil
	}}
	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"capabilities", "uid:" + h.agentUID, "-o", "json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var projection agentCapabilityProjection
	if err := json.Unmarshal(stdout.Bytes(), &projection); err != nil {
		t.Fatal(err)
	}
	if projection.Runtime == nil || projection.Runtime.Coordination == nil || projection.Runtime.Coordination.Eligible {
		t.Fatalf("coordination eligibility = %#v", projection.Runtime)
	}
	recovery := projection.Runtime.Coordination.Recovery
	if !strings.Contains(recovery, "agent integrate claude --dry-run") || !strings.Contains(recovery, "agent integrate claude;") ||
		!strings.Contains(recovery, "agent resume uid:"+h.agentUID) {
		t.Fatalf("coordination recovery is not actionable for the same Agent: %q", recovery)
	}
	for _, entry := range projection.Capabilities {
		if entry.Action != "message.send" && entry.Action != "message.status" {
			continue
		}
		if entry.Available == nil || *entry.Available || entry.Evidence != "local-registration-lease" ||
			entry.Reason != "exact Claude source registration lease is stale or unavailable" {
			t.Fatalf("%s did not reflect lease failure: %#v coordination=%#v", entry.Action, entry, projection.Runtime.Coordination)
		}
	}
}

type countingControlBinding struct{ calls int }

func (b *countingControlBinding) Live(context.Context, string) (agentControlLive, bool, error) {
	b.calls++
	return agentControlLive{}, false, nil
}

type countingReviewBinding struct{ calls int }

func (b *countingReviewBinding) LiveThreadID(context.Context, string) (string, bool, error) {
	b.calls++
	return "", false, nil
}

type countingReviewStarter struct{ calls int }

func (s *countingReviewStarter) Start(context.Context, string, corecap.ReviewTarget) (corecap.ReviewResult, error) {
	s.calls++
	return corecap.ReviewResult{}, nil
}

func TestUnsupportedNativeAgentActionsRefuseBeforeRuntimeOrProviderEffects(t *testing.T) {
	for _, provider := range []string{"claude", "antigravity"} {
		for _, test := range []struct {
			name string
			args []string
		}{
			{name: "turn start", args: []string{"turn", "start", "uid:agt-alpha-codex", "--", "text"}},
			{name: "turn steer", args: []string{"turn", "steer", "uid:agt-alpha-codex", "--", "text"}},
			{name: "turn interrupt", args: []string{"turn", "interrupt", "uid:agt-alpha-codex"}},
			{name: "approval review", args: []string{"approval", "review", "uid:agt-alpha-codex"}},
			{name: "review", args: []string{"review", "uid:agt-alpha-codex"}},
		} {
			t.Run(provider+"/"+test.name, func(t *testing.T) {
				store := newFakeResourceStore(t)
				agent, _ := store.registry.Agent("agt-alpha-codex")
				agent.Spec.Provider = provider
				control := &countingControlBinding{}
				reviewBinding := &countingReviewBinding{}
				reviews := &countingReviewStarter{}
				cmd, _, _ := newTestAgentCommand(t, store)
				cmd.controlBinding = control
				cmd.reviewBinding = reviewBinding
				cmd.reviews = reviews
				providerCalls := 0
				cmd.controlCall = func(context.Context, string, coremetadata.CodexEndpointRef, codexLifecycleIdentity, agentControlRequest) (agentControlResponse, error) {
					providerCalls++
					return agentControlResponse{}, nil
				}
				before := store.snapshot()
				stdout, stderr, err := runRoute(t, cmd, test.args...)
				if err == nil || !strings.Contains(err.Error(), "does not support native exact control") {
					t.Fatalf("error = %v", err)
				}
				if stdout != "" || stderr != "" || control.calls != 0 || reviewBinding.calls != 0 || reviews.calls != 0 || providerCalls != 0 || store.writes != 0 || store.transactions != 0 || store.snapshot() != before {
					t.Fatalf("effects stdout=%q stderr=%q control=%d reviewLookup=%d review=%d provider=%d writes=%d transactions=%d changed=%t", stdout, stderr, control.calls, reviewBinding.calls, reviews.calls, providerCalls, store.writes, store.transactions, store.snapshot() != before)
				}
			})
		}
	}
}

func TestAgentDispatchGroupsComeFromCapabilityCatalogAndFutureRoutesStayAbsent(t *testing.T) {
	t.Parallel()

	want := append(aiprovider.AgentGroups(), "capabilities")
	if !reflect.DeepEqual(agentSubcommands, want) {
		t.Fatalf("agentSubcommands = %v, want %v", agentSubcommands, want)
	}
	cmd := newAgentCommand()
	for _, group := range []string{"message", "wait"} {
		var stdout, stderr bytes.Buffer
		err := cmd.Run([]string{group}, &stdout, &stderr)
		if err == nil || stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatalf("future group %s: err=%v stdout=%q stderr=%q", group, err, stdout.String(), stderr.String())
		}
	}
}

func TestAgentCapabilitiesSkipsAutomaticHookMigration(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"agent", "capabilities", "--provider", "codex"},
		{"agent", "capabilities", "uid:agent"},
	} {
		if shouldRunLegacyHookMigrations(args) {
			t.Errorf("shouldRunLegacyHookMigrations(%q) = true", args)
		}
	}
}

func TestGenericStatusAndTopicStayProviderIndependentIncludingEmptyProvider(t *testing.T) {
	t.Parallel()

	var baseline string
	for _, provider := range []string{"codex", "claude", "antigravity", ""} {
		store := newFakeResourceStore(t)
		agent, _ := store.registry.Agent("agt-alpha-codex")
		agent.Spec.Provider = provider
		cmd, ai, usage := newTestAgentCommand(t, store)
		cmd.mirror = &fakeAgentMutationMirror{}
		if _, _, err := runRoute(t, cmd, "topic", "set", "same topic", "uid:agt-alpha-codex"); err != nil {
			t.Fatalf("provider %q topic set: %v", provider, err)
		}
		if _, _, err := runRoute(t, cmd, "status", "set", "idle", "uid:agt-alpha-codex"); err != nil {
			t.Fatalf("provider %q status set: %v", provider, err)
		}
		topic, _, err := runRoute(t, cmd, "topic", "get", "uid:agt-alpha-codex")
		if err != nil {
			t.Fatal(err)
		}
		status, _, err := runRoute(t, cmd, "status", "get", "uid:agt-alpha-codex")
		if err != nil {
			t.Fatal(err)
		}
		got := topic + status
		if baseline == "" {
			baseline = got
		} else if got != baseline {
			t.Fatalf("provider %q projection = %q, baseline %q", provider, got, baseline)
		}
		stored, _ := store.registry.Agent("agt-alpha-codex")
		if stored.Status.Interaction.Source != string(coremetadata.InteractionSourceManual) {
			t.Fatalf("provider %q source = %q", provider, stored.Status.Interaction.Source)
		}
		if len(ai.calls) != 0 || len(usage.calls) != 0 {
			t.Fatalf("provider %q reached provider adapters: ai=%v usage=%v", provider, ai.calls, usage.calls)
		}
	}
}
