package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	claudeadapter "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// newClaudeHandoffStageSource registers one tmux Claude Agent whose recorded
// provider is this test's parent process and whose lease helper is this test
// process, unless forged to a birth no live process has. No lease socket is
// served. It returns the Registry path and the Agent's route.
func newClaudeHandoffStageSource(t *testing.T, forgeProvider, forgeHelper bool) (string, coremetadata.AgentRouteRef) {
	t.Helper()
	h := newSessionRefHarness(t, aiModeClaude)
	provider, _, err := localipc.Process(os.Getppid())
	if err != nil {
		t.Fatal(err)
	}
	helper, _, err := localipc.Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if forgeProvider {
		provider.Start = "forged-" + provider.Start
	}
	if forgeHelper {
		helper.Start = "forged-" + helper.Start
	}
	mutator := intmetadata.DefaultMutator()
	if err := mutator.RecordClaudeProcess(h.registry, h.paneUID, h.agentUID, h.envGeneration, provider); err != nil {
		t.Fatal(err)
	}
	authority := coremetadata.ClaudeAuthorityRef{SessionID: "synthetic-session", Process: provider,
		RegistrationGeneration: "synthetic-registration", LeaseProcess: helper}
	if err := mutator.BeginClaudeRegistration(h.registry, h.paneUID, h.agentUID, h.envGeneration, authority); err != nil {
		t.Fatal(err)
	}
	if err := mutator.RecordClaudeRegistration(h.registry, h.paneUID, h.agentUID, h.envGeneration,
		coremetadata.ClaudeRegistration{Authority: authority}); err != nil {
		t.Fatal(err)
	}
	registryPath := intmetadata.PathFor(filepath.Join(shortTempDomain(t), "state"))
	if _, err := intmetadata.NewStore(registryPath).Update(func(reg *coremetadata.Registry) error { *reg = h.registry.Clone(); return nil }); err != nil {
		t.Fatal(err)
	}
	route, reason := coremetadata.ResolveAgentRoute(*h.registry, h.agentUID)
	if reason != "" {
		t.Fatal(reason)
	}
	return registryPath, route
}

// A Claude source the helper cannot prove names the proof step that refused
// it: its recorded provider or lease helper birth, or the source registration
// lease probe step. The refusal stays the known zero-write handoff refusal,
// writes nothing durable, and journals exactly one record per refusal.
func TestClaudeHandoffRouteNamesTheRefusingSourceStage(t *testing.T) {
	for _, test := range []struct {
		name                       string
		forgeProvider, forgeHelper bool
		stage                      diagnostics.ClaudeHandoffRouteStage
	}{
		{"provider-birth", true, false, diagnostics.ClaudeHandoffStageProviderProcessMismatch},
		{"lease-helper-birth", false, true, diagnostics.ClaudeHandoffStageLeaseHelperProcessMismatch},
		// Both births are live, so the proof reaches the lease probe, which
		// refuses at its first socket check: no lease socket is served.
		{"lease-socket", false, false, diagnostics.ClaudeHandoffStageLeaseSocketUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			registryPath, source := newClaudeHandoffStageSource(t, test.forgeProvider, test.forgeHelper)
			broker, err := newLiveClaudeDialogueBroker(registryPath)
			if err != nil {
				t.Fatal(err)
			}
			var refused []diagnostics.ClaudeHandoffRouteRecord
			broker.handoffRoute = func(r diagnostics.ClaudeHandoffRouteRecord) { refused = append(refused, r) }
			envelope := dialogueEnvelope("message-handoff-stage-"+test.name, time.Now().UTC().Add(time.Minute))
			envelope.BrokerEnvelope.Source = publicMessageRoute(source)
			if broker.Current(*envelope.BrokerEnvelope) {
				t.Fatal("unproved source was current")
			}
			if len(refused) != 0 {
				t.Fatalf("Current journaled %+v", refused)
			}
			if err := broker.MarkHandoff(*envelope.BrokerEnvelope); err == nil {
				t.Fatal("unproved source handoff accepted")
			}
			want := diagnostics.ClaudeHandoffRouteRecord{Side: diagnostics.ClaudeHandoffSourceUnproven,
				Peer: diagnostics.ClaudeHandoffPeerTmuxClaude, AgentUID: source.AgentUID, Stage: test.stage}
			if len(refused) != 1 || refused[0] != want {
				t.Fatalf("journal %+v, want %+v", refused, want)
			}
			store := messagestore.NewStore(filepath.Dir(filepath.Dir(registryPath)))
			if _, found, err := store.Get(envelope.MessageRef); err != nil || found {
				t.Fatalf("refused handoff wrote durable state: found=%v err=%v", found, err)
			}
		})
	}
}

// Every failed lease probe step maps to its own closed stage, a refusal and
// an expired bound at the same step stay distinct, and a ready probe names
// none.
func TestClaudeHandoffLeaseStageIsClosedAndDistinct(t *testing.T) {
	if got := claudeHandoffLeaseStage(claudeProbeReady, ""); got != "" {
		t.Fatalf("ready probe stage = %q", got)
	}
	cases := []struct {
		outcome claudeProbeOutcome
		step    string
		want    diagnostics.ClaudeHandoffRouteStage
	}{
		{claudeProbeStale, "authority", diagnostics.ClaudeHandoffStageLeaseAuthorityInvalid},
		{claudeProbeStale, "process", diagnostics.ClaudeHandoffStageLeaseProcessMismatch},
		{claudeProbeStale, "socket", diagnostics.ClaudeHandoffStageLeaseSocketUnavailable},
		{claudeProbeStale, "dial", diagnostics.ClaudeHandoffStageLeaseDialStale},
		{claudeProbeUnanswered, "dial", diagnostics.ClaudeHandoffStageLeaseDialUnanswered},
		{claudeProbeStale, "peer", diagnostics.ClaudeHandoffStageLeasePeerMismatch},
		{claudeProbeStale, "readiness", diagnostics.ClaudeHandoffStageLeaseReadinessStale},
		{claudeProbeUnanswered, "readiness", diagnostics.ClaudeHandoffStageLeaseReadinessUnanswered},
		{claudeProbeStale, "not-ready", diagnostics.ClaudeHandoffStageLeaseNotReady},
		{claudeProbeStale, "target", diagnostics.ClaudeHandoffStageLeaseTargetInvalid},
		{claudeProbeStale, "coordination", diagnostics.ClaudeHandoffStageLeaseProbeStale},
		{claudeProbeUnanswered, "coordination", diagnostics.ClaudeHandoffStageLeaseProbeUnanswered},
		{claudeProbeStale, "coordination-not-ready", diagnostics.ClaudeHandoffStageLeaseProbeNotReady},
		{claudeProbeStale, "", diagnostics.ClaudeHandoffStageLeaseUnclassified},
	}
	seen := map[diagnostics.ClaudeHandoffRouteStage]bool{}
	for _, test := range cases {
		got := claudeHandoffLeaseStage(test.outcome, claudeadapter.LeaseStep(test.step))
		if got != test.want || seen[got] {
			t.Fatalf("%v/%q stage = %q, want distinct %q", test.outcome, test.step, got, test.want)
		}
		seen[got] = true
	}
}
