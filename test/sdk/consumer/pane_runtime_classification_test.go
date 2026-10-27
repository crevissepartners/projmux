package consumer_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// A consumer reads Pane runtime kinds, graph process keys and the
// classification values through the public packages alone.
func TestExternalPaneRuntimeAndClassification(t *testing.T) {
	snapshot := core.RegistrySnapshot{Panes: []core.Pane{
		{Metadata: core.ObjectMeta{UID: "process"}, Spec: core.PaneSpec{Runtime: core.PaneRuntimeSpec{Kind: core.RuntimeProcess}}},
		{Metadata: core.ObjectMeta{UID: "legacy"}},
	}}
	if snapshot.Panes[0].Spec.Runtime.EffectiveKind() != core.RuntimeProcess || snapshot.Panes[1].Spec.Runtime.EffectiveKind() != core.RuntimeTmux {
		t.Fatal("runtime kinds")
	}
	key := &core.GraphProcessKey{Host: "host", Pane: "process", Generation: "gen"}
	node := core.GraphPaneNode{Process: key, Class: "managed", Status: "live"}
	agent := core.GraphAgentNode{Process: &core.GraphProcessKey{Pane: "process", Generation: "gen"}}
	if !node.Process.Valid() || agent.Process.Valid() {
		t.Fatal("process key validity")
	}
	b, err := json.Marshal(node)
	if err != nil {
		t.Fatal(err)
	}
	var back core.GraphPaneNode
	if err := json.Unmarshal(b, &back); err != nil || back.Process != nil {
		t.Fatalf("process key reached JSON: %s %v", b, err)
	}
	if projmux.NormalizeProvider(" Claude ") != "claude" || projmux.ReservedProfileName != "none" {
		t.Fatal("helpers")
	}
	wrapped := fmt.Errorf("send: %w", projmux.ErrOperatorOriginTargetNotClaude)
	if !errors.Is(wrapped, projmux.ErrOperatorOriginTargetNotClaude) || projmux.CodeOperatorOriginTargetNotClaude != "operator-origin-target-not-claude" {
		t.Fatal("operator classification")
	}
	for _, code := range []projmux.Code{projmux.CodePersonaProviderUnsupported, projmux.CodePersonaUnavailable, projmux.CodeOperatorOriginTargetNotClaude} {
		if code == "" {
			t.Fatal("empty code")
		}
	}
}
