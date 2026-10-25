package registryview

import (
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

func TestAgentActionsOfferMarkOfflineOnlyForUnknownProcessAgents(t *testing.T) {
	key := &resourcegraph.ProcessKey{Host: "h", Pane: "p", Generation: "g"}
	for _, tc := range []struct {
		process     *resourcegraph.ProcessKey
		status      resourcegraph.Status
		missingRoot bool
		want        []Action
	}{
		{process: key, status: resourcegraph.StatusUnknown, want: []Action{ActionResume, ActionMarkOffline, ActionDelete}},
		{process: key, status: resourcegraph.StatusOffline, want: []Action{ActionDelete}},
		{process: key, status: resourcegraph.StatusLive, want: []Action{ActionDelete}},
		{process: key, status: resourcegraph.StatusUnknown, missingRoot: true, want: []Action{ActionDelete}},
		{status: resourcegraph.StatusUnknown, want: []Action{ActionDelete}},
		{status: resourcegraph.StatusOffline, want: []Action{ActionResume, ActionDelete}},
	} {
		got := agentActions(resourcegraph.AgentNode{Process: tc.process, Status: tc.status, MissingRoot: tc.missingRoot})
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("process=%t status=%s missingRoot=%t: %v, want %v", tc.process != nil, tc.status, tc.missingRoot, got, tc.want)
		}
	}
}
