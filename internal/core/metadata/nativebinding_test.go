package metadata

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBindCodexActivationRequiresExactAgentPaneGenerationAndThread(t *testing.T) {
	endpoint := CodexEndpointRef{StateDomainID: "state-native", EndpointGenerationID: "endpoint-native"}
	reg := lifecycleFixture(t)
	agent, _ := reg.Agent(lifecycleAgentUID)
	pane, _ := reg.Pane(lifecyclePaneUID)
	agent.Status.SessionRef = nil
	agent.Status.Activation = AgentActivation{State: ActivationPending}
	mut := Mutator{Now: func() time.Time { return lifecycleClock.Add(time.Minute) }}

	tests := []struct {
		name       string
		agentUID   string
		paneUID    string
		generation string
		threadID   string
		turnID     string
		wantWrite  bool
	}{
		{name: "exact", agentUID: agent.Metadata.UID, paneUID: pane.Metadata.UID, generation: lifecycleGeneration, threadID: "thread-native", turnID: "turn-native", wantWrite: true},
		{name: "previous generation", agentUID: agent.Metadata.UID, paneUID: pane.Metadata.UID, generation: "generation-previous", threadID: "thread-native", turnID: "turn-native"},
		{name: "other pane", agentUID: agent.Metadata.UID, paneUID: "pane-other", generation: lifecycleGeneration, threadID: "thread-native", turnID: "turn-native"},
		{name: "other agent", agentUID: "agent-other", paneUID: pane.Metadata.UID, generation: lifecycleGeneration, threadID: "thread-native", turnID: "turn-native"},
		{name: "empty thread", agentUID: agent.Metadata.UID, paneUID: pane.Metadata.UID, generation: lifecycleGeneration, turnID: "turn-native"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			working := reg.Clone()
			before := working.Clone()
			changed, err := mut.BindCodexActivation(&working, CodexActivationObservation{
				AgentUID: tc.agentUID, PaneUID: tc.paneUID, Generation: tc.generation,
				ThreadID: tc.threadID, TurnID: tc.turnID, Endpoint: endpoint,
			})
			if tc.wantWrite {
				if err != nil || !changed {
					t.Fatalf("BindCodexActivation() = (%t, %v), want write", changed, err)
				}
				gotAgent, _ := working.Agent(agent.Metadata.UID)
				gotPane, _ := working.Pane(pane.Metadata.UID)
				if gotAgent.Status.SessionRef == nil || gotAgent.Status.SessionRef.Codex == nil || gotAgent.Status.SessionRef.Codex.ThreadID != tc.threadID {
					t.Fatalf("sessionRef = %#v", gotAgent.Status.SessionRef)
				}
				if gotPane.Status.Activation.Codex == nil || gotPane.Status.Activation.Codex.ThreadID != tc.threadID || gotPane.Status.Activation.Codex.TurnID != tc.turnID {
					t.Fatalf("activation binding = %#v", gotPane.Status.Activation.Codex)
				}
				if gotAgent.Status.Activation.State != ActivationAcknowledged || gotAgent.Status.Activation.Source != string(InteractionSourceProviderControl) {
					t.Fatalf("activation = %#v", gotAgent.Status.Activation)
				}
				return
			}
			if err == nil || changed {
				t.Fatalf("BindCodexActivation() = (%t, %v), want refusal", changed, err)
			}
			if !reflect.DeepEqual(before, working) {
				t.Fatal("refused binding mutated the Registry")
			}
		})
	}
}

func TestNativeCodexBindingRemainsAdditiveInCurrentSchema(t *testing.T) {
	endpoint := CodexEndpointRef{StateDomainID: "state-native", EndpointGenerationID: "endpoint-native"}
	reg := lifecycleFixture(t)
	agent, _ := reg.Agent(lifecycleAgentUID)
	agent.Status.SessionRef = nil
	mut := Mutator{Now: func() time.Time { return lifecycleClock.Add(time.Minute) }}
	if changed, err := mut.BindCodexActivation(reg, CodexActivationObservation{
		AgentUID: lifecycleAgentUID, PaneUID: lifecyclePaneUID, Generation: lifecycleGeneration,
		ThreadID: "thread-native", TurnID: "turn-native", Endpoint: endpoint,
	}); err != nil || !changed {
		t.Fatalf("BindCodexActivation() = (%t, %v)", changed, err)
	}
	raw, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"schemaVersion":3`, `"codex":{"threadId":"thread-native","turnId":"turn-native"}`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("schema-v2 native bytes missing %s: %s", want, raw)
		}
	}
	var roundTrip Registry
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if err := roundTrip.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRefineCodexActivationRejectsOtherThreadAndStaleGeneration(t *testing.T) {
	endpoint := CodexEndpointRef{StateDomainID: "state-native", EndpointGenerationID: "endpoint-native"}
	reg := lifecycleFixture(t)
	agent, _ := reg.Agent(lifecycleAgentUID)
	pane, _ := reg.Pane(lifecyclePaneUID)
	agent.Status.SessionRef = nil
	agent.Status.Activation = AgentActivation{State: ActivationPending}
	mut := Mutator{Now: func() time.Time { return lifecycleClock.Add(time.Minute) }}
	if changed, err := mut.BindCodexActivation(reg, CodexActivationObservation{
		AgentUID: agent.Metadata.UID, PaneUID: pane.Metadata.UID, Generation: lifecycleGeneration,
		ThreadID: "thread-native", TurnID: "turn-initial", Endpoint: endpoint,
	}); err != nil || !changed {
		t.Fatalf("seed binding = (%t, %v)", changed, err)
	}

	for _, tc := range []struct {
		name, paneUID, generation, threadID, turnID string
		wantWrite                                   bool
	}{
		{name: "same thread refines turn", paneUID: pane.Metadata.UID, generation: lifecycleGeneration, threadID: "thread-native", turnID: "turn-next", wantWrite: true},
		{name: "other thread", paneUID: pane.Metadata.UID, generation: lifecycleGeneration, threadID: "thread-other", turnID: "turn-other"},
		{name: "previous generation", paneUID: pane.Metadata.UID, generation: "generation-previous", threadID: "thread-native", turnID: "turn-stale"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			working := reg.Clone()
			before := working.Clone()
			changed, err := mut.RefineCodexActivation(&working, CodexActivationObservation{
				AgentUID: agent.Metadata.UID, PaneUID: tc.paneUID, Generation: tc.generation,
				ThreadID: tc.threadID, TurnID: tc.turnID, Endpoint: endpoint,
			})
			if err != nil {
				t.Fatal(err)
			}
			if changed != tc.wantWrite {
				t.Fatalf("changed = %t, want %t", changed, tc.wantWrite)
			}
			if !tc.wantWrite && !reflect.DeepEqual(before, working) {
				t.Fatal("stale/foreign refinement mutated the Registry")
			}
		})
	}
}

func TestCASCodexHandoverTargetKeepsAgentPaneThreadAndRejectsDuplicateOwner(t *testing.T) {
	old := CodexEndpointRef{StateDomainID: "state-native", EndpointGenerationID: "old"}
	successor := CodexEndpointRef{StateDomainID: "state-native", EndpointGenerationID: "successor"}
	reg := lifecycleFixture(t)
	agent, _ := reg.Agent(lifecycleAgentUID)
	pane, _ := reg.Pane(lifecyclePaneUID)
	agent.Status.Interaction = AgentInteraction{Kind: InteractionResponseComplete, Source: string(InteractionSourceProviderControl), ObservedAt: lifecycleClock}
	agent.Status.SessionRef.Codex = &CodexSessionRef{ThreadID: "thread-native", HasStartedTurn: true, Endpoint: &old,
		Lifecycle: &CodexGenerationLifecycleRef{State: CodexGenerationHandoverPending,
			Operation: &CodexGenerationOperationRef{ID: "handover", Endpoint: old}}}
	pane.Status.Activation.RuntimeID = "%9"
	pane.Status.Activation.Codex = &CodexActivationBinding{ThreadID: "thread-native", TurnID: "turn-complete"}
	target := CodexHandoverTarget{AgentUID: lifecycleAgentUID, PaneUID: lifecyclePaneUID, PaneRuntimeID: "%9",
		PaneGeneration: lifecycleGeneration, RelaunchGeneration: "handover-generation", ThreadID: "thread-native"}
	mut := Mutator{Now: func() time.Time { return lifecycleClock.Add(time.Minute) }}
	changed, err := mut.CASCodexHandoverTarget(reg, target, old, successor, "handover")
	if err != nil || !changed {
		t.Fatalf("CAS = (%t, %v)", changed, err)
	}
	gotAgent, _ := reg.Agent(lifecycleAgentUID)
	gotPane, _ := reg.Pane(lifecyclePaneUID)
	if gotAgent.Status.SessionRef.Codex.ThreadID != "thread-native" || !gotAgent.Status.SessionRef.Codex.Endpoint.Same(successor) ||
		gotPane.Status.Activation.Generation != "handover-generation" || gotPane.Status.Activation.RuntimeID != "%9" ||
		gotPane.Metadata.UID != lifecyclePaneUID || gotAgent.Metadata.UID != lifecycleAgentUID {
		t.Fatalf("CAS changed identity tuple: agent=%+v pane=%+v", gotAgent, gotPane)
	}
	if changed, err := mut.CASCodexHandoverTarget(reg, target, old, successor, "handover"); err != nil || changed {
		t.Fatalf("repeat CAS = (%t, %v), want no-op", changed, err)
	}

	duplicate := gotAgent.Clone()
	duplicate.Metadata.UID, duplicate.Metadata.Name = "agent-duplicate", "duplicate"
	duplicate.Status.PaneRef = ""
	reg.Agents = append(reg.Agents, duplicate)
	before, _ := json.Marshal(reg)
	if _, err := mut.CASCodexHandoverTarget(reg, target, old, successor, "handover"); err == nil {
		t.Fatal("CAS accepted a duplicate successor thread owner")
	}
	after, _ := json.Marshal(reg)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("duplicate-owner refusal mutated Registry")
	}
}
