package registryview

import (
	"reflect"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

func TestVirtualWindowStatusAndOpenPreserveProcessChildren(t *testing.T) {
	for _, phase := range []coremetadata.AgentPhase{coremetadata.PhaseRunning, coremetadata.PhaseOffline} {
		for _, observed := range []bool{false, true} {
			t.Run(string(phase)+map[bool]string{false: "/unobserved", true: "/observed"}[observed], func(t *testing.T) {
				reg := registryFixture()
				reg.Windows[0].Spec.AnchorPaneRef = "pane-agent"
				reg.Panes = reg.Panes[1:]
				reg.Panes[0].Spec.Runtime.Kind = coremetadata.RuntimeProcess
				reg.Agents[0].Status.Phase = phase
				before := reg.Clone()
				inv := resourcegraph.Inventory{}
				if observed {
					inv = liveInventory()
					inv.Windows = nil
					inv.Panes = nil
				}
				view := Build(Input{Graph: resourcegraph.Resolve(reg, inv)})
				row, ok := view.Row("uid:win-main")
				if !ok || row.Status != StatusVirtual || row.Live || row.Active || row.Runtime != nil || !reflect.DeepEqual(row.Actions, []Action{ActionOpen, ActionDelete}) {
					t.Fatalf("virtual Window = %+v", row)
				}
				child, ok := view.Row("uid:agent-one")
				if !ok || child.ParentID != row.ID || child.Phase != string(phase) {
					t.Fatalf("child = %+v", child)
				}
				if !reflect.DeepEqual(reg, before) {
					t.Fatal("read projection wrote Registry")
				}
			})
		}
	}
}

func TestVirtualWindowUsesEligibleAnchorAndKeepsMissingRootAdmission(t *testing.T) {
	reg := registryFixture()
	reg.Windows[0].Spec.AnchorPaneRef = "pane-agent"
	reg.Panes[1].Spec.Runtime.Kind = coremetadata.RuntimeProcess
	graph := resourcegraph.Resolve(reg, resourcegraph.Inventory{})
	graph.Windows[0].MissingRoot = true
	graph.Windows[0].Status = resourcegraph.StatusMissingRoot
	row, _ := Build(Input{Graph: graph}).Row("uid:win-main")
	if row.Status != StatusVirtual || !row.MissingRoot || row.Allows(ActionOpen) || !row.Allows(ActionRebind) {
		t.Fatalf("missing root = %+v", row)
	}
	reg.Agents[0].Status.PaneRef = "superseded"
	row, _ = Build(Input{Graph: resourcegraph.Resolve(reg, resourcegraph.Inventory{})}).Row("uid:win-main")
	if row.Status == StatusVirtual || row.Allows(ActionOpen) {
		t.Fatalf("ineligible process anchor = %+v", row)
	}
	reg = registryFixture()
	row, _ = Build(Input{Graph: resourcegraph.Resolve(reg, liveInventory())}).Row("uid:win-main")
	if row.Status == StatusVirtual || !row.IsLive() {
		t.Fatalf("real Window = %+v", row)
	}
}
