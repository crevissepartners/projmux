package controller

import (
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"testing"
)

func TestProcessPaneNeverEntersTmuxControllerHandles(t *testing.T) {
	graph := runtimeGraph(paneNode("%7", "process-pane", "@1", resourcegraph.ClassManaged), paneNode("%8", "tmux-pane", "@1", resourcegraph.ClassManaged))
	graph.Panes = []resourcegraph.PaneNode{{Pane: coremetadata.Pane{Metadata: coremetadata.ObjectMeta{UID: "process-pane"}}, Process: &resourcegraph.ProcessKey{Host: "host", Pane: "process-pane", Generation: "gen"}}}
	handles := IndexHandles(graph)
	if _, ok := handles.Lookup("%7"); ok {
		t.Fatal("process acquired tmux handle")
	}
	if _, ok := handles.Lookup("%8"); !ok {
		t.Fatal("tmux parity lost")
	}
	actions, _ := Authorize(handles, testGuardFields, Grant{}, []Candidate{{Key: "repair", Intent: IntentRepairMirror, Target: "%7", Args: setOption("-p", "%7", "@projmux_pane_uid", "process-pane")}})
	if len(actions) != 1 || actions[0].Allowed() {
		t.Fatalf("process write authorized: %+v", actions)
	}
}
