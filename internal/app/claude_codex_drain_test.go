package app

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// A Claude handoff must recheck the Codex sender's exact live lease even when
// its broker is draining. The previous fresh Dial failed before that check.
func TestClaudeHandoffChecksBoundCodexSourceDuringVintageDrain(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	stateDir := filepath.Dir(filepath.Dir(fixture.registryPath))
	store := intmetadata.NewStore(fixture.registryPath)
	registry, err := store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	var replaced atomic.Bool
	source, host, closeSource := processFixtureCodexSource(t, &registry, fixture.route.AgentUID, stateDir, &replaced)
	defer closeSource()
	if _, err := store.Update(func(reg *coremetadata.Registry) error { *reg = registry.Clone(); return nil }); err != nil {
		t.Fatal(err)
	}
	broker, err := newLiveClaudeDialogueBroker(fixture.registryPath)
	if err != nil {
		t.Fatal(err)
	}
	makeEnvelope := func(ref string) coremessage.Envelope {
		envelope := *dialogueForRoute(ref, fixture.route, time.Now().UTC()).BrokerEnvelope
		envelope.Source = publicMessageRoute(source)
		if _, _, err := messagestore.NewStore(stateDir).PutAccepted(envelope, "claude-coordination"); err != nil {
			t.Fatal(err)
		}
		return envelope
	}
	before := makeEnvelope("message-before-vintage-drain")
	if err := broker.MarkHandoff(before); err != nil {
		t.Fatalf("pre-install handoff: %v", err)
	}
	replaced.Store(true)
	authority := source.Authority().(coremetadata.CodexRouteAuthority)
	key, err := codexbroker.NewEndpointKey(authority.Authority.StateDomainID, authority.Authority.EndpointGenerationID)
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := codexbroker.NewDiscovery(stateDir, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codexbroker.Dial(t.Context(), discovery, codexbroker.DialConfig{}); codexbroker.RefusalOf(err) != codexbroker.RefusalDrainRequired {
		t.Fatalf("fresh Dial during drain = %v, want drain-required", err)
	}
	if !host.Stats().Draining {
		t.Fatal("fixture did not enter vintage drain")
	}
	during := makeEnvelope("message-during-vintage-drain")
	if err := broker.MarkHandoff(during); err != nil {
		t.Fatalf("bound source handoff during drain: %v", err)
	}
	if record, found, err := messagestore.NewStore(stateDir).Get(during.MessageRef); err != nil || !found || !record.HandoffObserved {
		t.Fatalf("drain handoff was not durable: found=%t observed=%t err=%v", found, record.HandoffObserved, err)
	}
	closeSource()
	stale := makeEnvelope("message-after-source-release")
	if err := broker.MarkHandoff(stale); err == nil {
		t.Fatal("released source passed handoff check")
	}
	if record, found, err := messagestore.NewStore(stateDir).Get(stale.MessageRef); err != nil || !found || record.HandoffObserved {
		t.Fatalf("stale source changed the durable handoff: found=%t observed=%t err=%v", found, record.HandoffObserved, err)
	}
}
