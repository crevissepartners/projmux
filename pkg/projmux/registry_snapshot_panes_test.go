package projmux

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// Pane rows keep their stored order (duplicates included), runtime kinds
// (legacy rows without a runtime are tmux) and nested values, owned by the
// caller.
func TestReadRegistrySnapshotPanesOrderRuntimeAndOwnership(t *testing.T) {
	r := metadata.NewRegistry()
	pane := func(uid, name string, kind metadata.RuntimeKind, generation string) metadata.Pane {
		return metadata.Pane{
			APIVersion: metadata.APIVersion, Kind: metadata.KindPane,
			Metadata: metadata.ObjectMeta{UID: uid, Name: name, OwnerRef: &metadata.OwnerRef{Kind: metadata.KindAgent, UID: "agent-" + name}, Labels: map[string]string{"nested": "original"}},
			Spec:     metadata.PaneSpec{Runtime: metadata.PaneRuntimeSpec{Kind: kind}, Role: metadata.PaneRoleAgent},
			Status:   metadata.PaneStatus{Activation: metadata.PaneActivation{Kind: kind, Generation: generation}, Conditions: []metadata.Condition{{Type: "fixture"}}},
		}
	}
	r.Panes = []metadata.Pane{
		pane("pane-z", "process", metadata.RuntimeProcess, "gen-z"),
		pane("pane-a", "legacy", "", "gen-a"),
		pane("pane-m", "tmux", metadata.RuntimeTmux, "gen-m"),
		pane("pane-a", "duplicate", metadata.RuntimeProcess, "gen-dup"),
	}
	r.Panes[0].Status.Activation.Process = &metadata.ProcessActivation{Binding: metadata.ProcessBinding{PaneUID: "pane-z", Generation: "gen-z"}}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := snapshotClient(t, raw)
	got, err := c.ReadRegistrySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := resources.Load(c.paths)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(loaded.Panes)
	gotJSON, _ := json.Marshal(got.Panes)
	if string(want) != string(gotJSON) {
		t.Fatalf("stored Pane JSON drift:\n%s\n%s", want, gotJSON)
	}
	type row struct {
		UID, Name string
		Kind      core.RuntimeKind
	}
	rows := []row{}
	for _, p := range got.Panes {
		rows = append(rows, row{p.Metadata.UID, p.Metadata.Name, p.Spec.Runtime.EffectiveKind()})
	}
	if !reflect.DeepEqual(rows, []row{{"pane-z", "process", core.RuntimeProcess}, {"pane-a", "legacy", core.RuntimeTmux}, {"pane-m", "tmux", core.RuntimeTmux}, {"pane-a", "duplicate", core.RuntimeProcess}}) {
		t.Fatalf("pane rows: %+v", rows)
	}
	if got.Panes[1].Spec.Runtime.Kind != core.RuntimeKind(loaded.Panes[1].Spec.Runtime.Kind) {
		t.Fatal("legacy runtime was rewritten instead of defaulted by EffectiveKind")
	}
	for i, p := range loaded.Panes {
		if got.Panes[i].Spec.Runtime.EffectiveKind() != core.RuntimeKind(p.Spec.Runtime.EffectiveKind()) {
			t.Fatalf("runtime kind parity %s", p.Metadata.UID)
		}
	}
	got.Panes[0].Metadata.Labels["nested"] = "changed"
	got.Panes[0].Metadata.OwnerRef.UID = "changed"
	got.Panes[0].Status.Conditions[0].Type = "changed"
	got.Panes[0].Status.Activation.Process.Binding.Generation = "changed"
	fresh, err := c.ReadRegistrySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	freshJSON, _ := json.Marshal(fresh.Panes)
	if string(freshJSON) != string(want) {
		t.Fatal("returned Panes alias a later read")
	}
	projected, err := registrySnapshotRows(loaded)
	if err != nil {
		t.Fatal(err)
	}
	projected.Panes[0].Status.Activation.Process.Binding.Generation = "changed"
	projected.Panes[0].Metadata.Labels["nested"] = "changed"
	if loaded.Panes[0].Status.Activation.Process.Binding.Generation != "gen-z" || loaded.Panes[0].Metadata.Labels["nested"] != "original" {
		t.Fatal("returned Panes alias the loaded Registry")
	}
}
