package runtime

import (
	"encoding/json"
	"strings"
	"testing"
)

// An ordinary resume request keeps its wire bytes: the policy field exists only
// when a caller sets it, so an older owner never sees an unknown field.
func TestProcessDetachedResumeWireOmitsEmptyHostLostPolicy(t *testing.T) {
	plain, err := json.Marshal(ProcessDetachedResumeRequest{Agent: "agent-01", WaitForPeer: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "HostLostPolicy") {
		t.Fatalf("empty policy reached the wire: %s", plain)
	}
	set, err := json.Marshal(ProcessDetachedResumeRequest{Agent: "agent-01", HostLostPolicy: ProcessResumeHostLostRequire})
	if err != nil || !strings.Contains(string(set), `"HostLostPolicy":"require"`) {
		t.Fatalf("policy missing from the wire: %s %v", set, err)
	}
}
