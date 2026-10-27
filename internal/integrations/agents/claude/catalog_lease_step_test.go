package claude

import (
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// A route without a valid Claude authority is refused at the first step,
// before any process read or socket check, and naming the step leaves the
// outcome ClassifyRegistrationLease returns unchanged.
func TestClassifyRegistrationLeaseStepNamesAuthority(t *testing.T) {
	t.Parallel()
	route := coremetadata.AgentRouteRef{AgentUID: "agent-abc234", PaneUID: "pane-abc234", Generation: "gen-abc234"}
	outcome, step := ClassifyRegistrationLeaseStep("/unused/state/registry.json", route)
	if outcome != ProbeStale || step != LeaseStepAuthority {
		t.Fatalf("outcome=%v step=%q", outcome, step)
	}
	if got := ClassifyRegistrationLease("/unused/state/registry.json", route); got != outcome {
		t.Fatalf("ClassifyRegistrationLease=%v, want %v", got, outcome)
	}
}
