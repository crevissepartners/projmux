package metadata

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAgentInteractionIsClosedAndLifecycleInvalidatesCurrentProjection(t *testing.T) {
	t.Parallel()
	want := []AgentInteractionKind{
		InteractionUnknown,
		InteractionIdle,
		InteractionInProgress,
		InteractionApprovalRequired,
		InteractionInputRequired,
		InteractionResponseComplete,
	}
	got := AgentInteractionKinds()
	if len(got) != len(want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] || !ValidAgentInteractionKind(want[i]) {
			t.Fatalf("kinds = %v, want %v", got, want)
		}
	}
	if ValidAgentInteractionKind(AgentInteractionKind("waiting")) {
		t.Fatal("transport state waiting must not enter the semantic enum")
	}

	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	agent := Agent{Status: AgentStatus{
		Phase: PhaseRunning, PaneRef: "pane-1",
		Interaction: AgentInteraction{Kind: InteractionResponseComplete, ObservedAt: now.Add(-time.Minute), Source: "provider-hook"},
	}}
	if current := agent.EffectiveInteraction(now); current.Kind != InteractionResponseComplete {
		t.Fatalf("fresh running current = %+v", current)
	}
	for _, phase := range []AgentPhase{PhaseOffline, PhaseFailed, PhasePending} {
		agent.Status.Phase = phase
		if current := agent.EffectiveInteraction(now); current.Kind != InteractionUnknown || !current.ObservedAt.IsZero() || current.Source != "" {
			t.Fatalf("%s current = %+v, want clean unknown", phase, current)
		}
	}
	agent.Status.Phase = PhaseRunning
	agent.Status.Interaction.ObservedAt = now.Add(-AgentInteractionFreshFor - time.Second)
	if current := agent.EffectiveInteraction(now); current.Kind != InteractionUnknown || !current.ObservedAt.IsZero() || current.Source != "" {
		t.Fatalf("stale current = %+v, want clean unknown", current)
	}
}

func TestAgentInteractionPersistsSemanticsWithoutPresentation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	agent := Agent{
		APIVersion: APIVersion, Kind: KindAgent,
		Metadata: ObjectMeta{UID: "agent-1", Name: "codex"},
		Status: AgentStatus{Phase: PhaseRunning, PaneRef: "pane-1", Interaction: AgentInteraction{
			Kind: InteractionApprovalRequired, ObservedAt: now, Source: "provider-hook",
		}},
	}
	raw, err := json.Marshal(agent)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{"●", "⚠", "\\u001b", "color", "glyph"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("durable Agent contains presentation %q: %s", forbidden, text)
		}
	}
	for _, required := range []string{"approval_required", "provider-hook", now.Format(time.RFC3339)} {
		if !strings.Contains(text, required) {
			t.Fatalf("durable Agent is missing %q: %s", required, text)
		}
	}
}

func TestAgentSemanticAndActivationMetadataRejectFreeFormText(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	reg.Agents = []Agent{{
		APIVersion: APIVersion, Kind: KindAgent,
		Metadata: ObjectMeta{UID: "agent-1", Name: "codex"},
		Status:   AgentStatus{Phase: PhaseRunning, PaneRef: "pane-1"},
	}}
	mutator := Mutator{Now: func() time.Time { return time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC) }}
	for _, source := range []string{"operator secret", "prompt=deploy", "credential-token"} {
		if _, err := mutator.SetAgentInteraction(&reg, "agent-1", InteractionIdle, source); err == nil {
			t.Fatalf("free-form interaction source %q persisted", source)
		}
	}
	if _, err := mutator.SetAgentInteraction(&reg, "agent-1", InteractionIdle, string(InteractionSourceManual)); err != nil {
		t.Fatalf("closed manual source rejected: %v", err)
	}
	if _, err := mutator.SetAgentActivation(&reg, "agent-1", ActivationUnconfirmed, string(InteractionSourceProviderHook), "raw provider error with token"); err == nil {
		t.Fatal("free-form activation reason persisted")
	}
	if _, err := mutator.SetAgentActivation(&reg, "agent-1", ActivationUnconfirmed, "custom-hook", ActivationReasonFailed); err == nil {
		t.Fatal("free-form activation source persisted")
	}
	if _, err := mutator.SetAgentActivation(&reg, "agent-1", ActivationUnconfirmed, string(InteractionSourceProviderHook), ActivationReasonFailed); err != nil {
		t.Fatalf("bounded activation metadata rejected: %v", err)
	}
}
