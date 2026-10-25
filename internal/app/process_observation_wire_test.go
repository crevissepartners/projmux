package app

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	runtimeservice "github.com/crevissepartners/projmux/internal/services/runtime"
)

// The shared observer sends the provider host's own envelope with only Observe
// set, so the bytes an owner reads stay those it always read.
func TestProcessObservationRequestWireMatchesHostEnvelope(t *testing.T) {
	binding := processhost.Binding{Host: "host", Project: "project", Window: "window", Agent: "agent", Pane: "pane", Generation: "gen", Operation: "op"}
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		want, err := json.Marshal(processHostRequest(provider, &binding, nil))
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(runtimeservice.ProcessObservationRequest(provider, binding))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s observation request:\n got %s\nwant %s\n%v", provider, got, want, err)
		}
		if processHostSocket(provider, "/state/metadata/registry.json", "pane", "gen") != runtimeservice.ProcessHostSocket(provider, "/state/metadata/registry.json", "pane", "gen") {
			t.Fatal(provider)
		}
	}
	// The response decode reads the same Observation the host writes.
	view := processHostObservation{Binding: binding, Provider: aiModeCodex, State: "ready", Protocol: processHostProtocol, Revision: "r", Actions: []string{"stop"}, Turn: true, Pending: 2, OwnerMode: "detached", Coordination: 3}
	raw, err := json.Marshal(processForegroundResult{Accepted: true, Observation: &view})
	if err != nil {
		t.Fatal(err)
	}
	var shared struct {
		Accepted    bool
		Observation *runtimeservice.ProcessHostObservation
	}
	if err := json.Unmarshal(raw, &shared); err != nil || !shared.Accepted || shared.Observation == nil {
		t.Fatal(err)
	}
	again, _ := json.Marshal(processHostObservation(*shared.Observation))
	original, _ := json.Marshal(view)
	if !bytes.Equal(again, original) {
		t.Fatalf("observation JSON differs:\n%s\n%s", again, original)
	}
}
