package metadata

import (
	"encoding/json"
	"maps"
	"testing"
)

func TestCreatorAnnotationsAreTheThreeBareUIDKeysAndSurviveARegistryRoundTrip(t *testing.T) {
	t.Parallel()

	want := map[string]string{
		"projmux.io/creator-agent": "agent-creator",
		"projmux.io/creator-pane":  "pane-creator",
		"projmux.io/creator-basis": "pane-chain",
	}
	got := CreatorAnnotations("agent-creator", "pane-creator")
	if !maps.Equal(got, want) {
		t.Fatalf("CreatorAnnotations = %v, want %v", got, want)
	}
	got[AnnotationCreatorAgent] = "mutated"
	if again := CreatorAnnotations("agent-creator", "pane-creator"); again[AnnotationCreatorAgent] != "agent-creator" {
		t.Fatal("CreatorAnnotations returned a shared map")
	}

	m := testMutator(dirSet{"/src/projmux": true})
	reg := NewRegistry()
	registered, err := registerFixture(m, &reg, "/src/projmux")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := m.CreateAgent(&reg, registered.Windows[0].Metadata.UID, CreateAgentOptions{
		Provider: "claude", Annotations: CreatorAnnotations("agent-creator", "pane-creator"), OperationID: "op-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Normalize().Validate(); err != nil {
		t.Fatalf("Registry with creator annotations is invalid: %v", err)
	}
	raw, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Registry
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	stored, ok := decoded.Agent(agent.Metadata.UID)
	if !ok || !maps.Equal(stored.Metadata.Annotations, want) {
		t.Fatalf("decoded Agent annotations = %v, want %v", stored.Metadata.Annotations, want)
	}
}
