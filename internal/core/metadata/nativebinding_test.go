package metadata

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBindCodexActivationRequiresExactAgentPaneGenerationAndThread(t *testing.T) {
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
				ThreadID: tc.threadID, TurnID: tc.turnID,
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
	reg := lifecycleFixture(t)
	agent, _ := reg.Agent(lifecycleAgentUID)
	agent.Status.SessionRef = nil
	mut := Mutator{Now: func() time.Time { return lifecycleClock.Add(time.Minute) }}
	if changed, err := mut.BindCodexActivation(reg, CodexActivationObservation{
		AgentUID: lifecycleAgentUID, PaneUID: lifecyclePaneUID, Generation: lifecycleGeneration,
		ThreadID: "thread-native", TurnID: "turn-native",
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
	reg := lifecycleFixture(t)
	agent, _ := reg.Agent(lifecycleAgentUID)
	pane, _ := reg.Pane(lifecyclePaneUID)
	agent.Status.SessionRef = nil
	agent.Status.Activation = AgentActivation{State: ActivationPending}
	mut := Mutator{Now: func() time.Time { return lifecycleClock.Add(time.Minute) }}
	if changed, err := mut.BindCodexActivation(reg, CodexActivationObservation{
		AgentUID: agent.Metadata.UID, PaneUID: pane.Metadata.UID, Generation: lifecycleGeneration,
		ThreadID: "thread-native", TurnID: "turn-initial",
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
				ThreadID: tc.threadID, TurnID: tc.turnID,
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
