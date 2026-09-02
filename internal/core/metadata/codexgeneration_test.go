package metadata

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCodexEndpointAndAuthorityRefsRoundTripExactlyAndIgnorePaneGeneration(t *testing.T) {
	reg := lifecycleFixture(t)
	agent, _ := reg.Agent(lifecycleAgentUID)
	pane, _ := reg.Pane(lifecyclePaneUID)
	endpoint := &CodexEndpointRef{StateDomainID: "state-domain-a", EndpointGenerationID: "endpoint-generation-7"}
	agent.Status.SessionRef = &AgentSessionRef{
		Provider: "codex", ObservedAt: lifecycleClock,
		Codex: &CodexSessionRef{ThreadID: "thread-exact", Endpoint: endpoint},
	}
	pane.Status.Activation.Generation = "pane-materialization-unrelated"
	pane.Status.Activation.Codex = &CodexActivationBinding{
		ThreadID: "thread-exact", TurnID: "turn-exact",
		Authority: &CodexAuthorityRef{
			StateDomainID: "state-domain-a", EndpointGenerationID: "endpoint-generation-7",
			BrokerRuntimeID: "broker-runtime-2", ConnectionEpoch: 1, BindingEpoch: 1,
		},
	}
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	for _, exact := range []string{
		`"endpoint":{"stateDomainID":"state-domain-a","endpointGenerationID":"endpoint-generation-7"}`,
		`"authority":{"stateDomainID":"state-domain-a","endpointGenerationID":"endpoint-generation-7","brokerRuntimeID":"broker-runtime-2","connectionEpoch":1,"bindingEpoch":1}`,
		`"generation":"pane-materialization-unrelated"`,
	} {
		if !strings.Contains(string(raw), exact) {
			t.Fatalf("encoded Registry misses exact ref %s: %s", exact, raw)
		}
	}
	var reopened Registry
	if err := json.Unmarshal(raw, &reopened); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Validate(); err != nil {
		t.Fatal(err)
	}
	gotAgent, _ := reopened.Agent(lifecycleAgentUID)
	gotPane, _ := reopened.Pane(lifecyclePaneUID)
	if !reflect.DeepEqual(gotAgent.Status.SessionRef.Codex.Endpoint, endpoint) ||
		gotPane.Status.Activation.Codex.Authority.EndpointGenerationID != "endpoint-generation-7" ||
		gotPane.Status.Activation.Generation == gotPane.Status.Activation.Codex.Authority.EndpointGenerationID {
		t.Fatal("endpoint/authority identity did not survive independently of PaneActivation.Generation")
	}
	if again, err := json.Marshal(&reopened); err != nil || string(again) != string(raw) {
		t.Fatalf("reopened Registry bytes drifted: err=%v", err)
	}
}

func TestLegacyCodexRefsRemainGenerationUnavailableWithoutInference(t *testing.T) {
	reg := lifecycleFixture(t)
	agent, _ := reg.Agent(lifecycleAgentUID)
	pane, _ := reg.Pane(lifecyclePaneUID)
	agent.Status.SessionRef = &AgentSessionRef{Provider: "codex", ObservedAt: lifecycleClock, Codex: &CodexSessionRef{ThreadID: "legacy-thread"}}
	pane.Status.Activation.Codex = &CodexActivationBinding{ThreadID: "legacy-thread"}
	raw, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "stateDomainID") || strings.Contains(string(raw), "endpointGenerationID") || strings.Contains(string(raw), "brokerRuntimeID") {
		t.Fatalf("legacy encoding inferred endpoint authority: %s", raw)
	}
	var reopened Registry
	if err := json.Unmarshal(raw, &reopened); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Validate(); err != nil {
		t.Fatal(err)
	}
	gotAgent, _ := reopened.Agent(lifecycleAgentUID)
	gotPane, _ := reopened.Pane(lifecyclePaneUID)
	if gotAgent.Status.SessionRef.Codex.Endpoint != nil || gotPane.Status.Activation.Codex.Authority != nil {
		t.Fatal("legacy decode invented a generation or authority")
	}
	if again, err := json.Marshal(&reopened); err != nil || string(again) != string(raw) {
		t.Fatal("legacy generation-unavailable state did not round-trip exactly")
	}
}

func TestCodexEndpointAndAuthorityValidationRejectsPartialRefs(t *testing.T) {
	reg := lifecycleFixture(t)
	agent, _ := reg.Agent(lifecycleAgentUID)
	pane, _ := reg.Pane(lifecyclePaneUID)
	agent.Status.SessionRef = &AgentSessionRef{Provider: "codex", ObservedAt: lifecycleClock, Codex: &CodexSessionRef{
		ThreadID: "thread", Endpoint: &CodexEndpointRef{StateDomainID: "domain"},
	}}
	if err := reg.Validate(); err == nil {
		t.Fatal("partial durable endpoint ref validated")
	}
	agent.Status.SessionRef.Codex.Endpoint = nil
	pane.Status.Activation.Codex = &CodexActivationBinding{ThreadID: "thread", Authority: &CodexAuthorityRef{
		StateDomainID: "domain", EndpointGenerationID: "generation", BrokerRuntimeID: "runtime", ConnectionEpoch: 1,
	}}
	if err := reg.Validate(); err == nil {
		t.Fatal("partial live authority ref validated")
	}
}

func TestCodexEndpointIdentityRejectsNonCanonicalWhitespace(t *testing.T) {
	for _, endpoint := range []CodexEndpointRef{
		{StateDomainID: " domain", EndpointGenerationID: "generation"},
		{StateDomainID: "domain", EndpointGenerationID: "generation\n"},
	} {
		if endpoint.Valid() {
			t.Fatalf("non-canonical endpoint validated: %#v", endpoint)
		}
		if _, ok := NewAgentSessionRef(AgentSessionObservation{Provider: "codex", ThreadID: "thread", Endpoint: &endpoint}, lifecycleClock); ok {
			t.Fatalf("ingest normalized non-canonical endpoint: %#v", endpoint)
		}
	}
}

func TestLegacyHookReobservationPreservesAnExactCodexEndpointRef(t *testing.T) {
	reg := lifecycleFixture(t)
	agent, _ := reg.Agent(lifecycleAgentUID)
	agent.Status.SessionRef = &AgentSessionRef{
		Provider: "codex", ObservedAt: lifecycleClock,
		Codex: &CodexSessionRef{ThreadID: "thread", SessionID: "optional-session", Endpoint: &CodexEndpointRef{
			StateDomainID: "domain", EndpointGenerationID: "generation",
		}},
	}
	mut := Mutator{Now: func() time.Time { return lifecycleClock.Add(time.Minute) }}
	if _, changed, err := mut.RecordAgentSessionRef(reg, lifecycleAgentUID, AgentSessionObservation{Provider: "codex", ThreadID: "thread"}); err != nil || changed {
		t.Fatalf("legacy reobservation = changed:%t err:%v", changed, err)
	}
	got, _ := reg.Agent(lifecycleAgentUID)
	if got.Status.SessionRef.Codex.Endpoint == nil || got.Status.SessionRef.Codex.Endpoint.EndpointGenerationID != "generation" {
		t.Fatal("legacy hook erased exact endpoint identity")
	}
	if got.Status.SessionRef.Codex.SessionID != "optional-session" {
		t.Fatal("legacy hook erased an omitted optional session identifier")
	}
}
