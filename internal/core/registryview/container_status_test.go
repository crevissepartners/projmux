package registryview

import (
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"reflect"
	"testing"
)

func TestContainerActionsStatusTmuxKindTable(t *testing.T) {
	for _, kind := range []RowKind{RowKindProject, RowKindWindow} {
		for _, status := range []resourcegraph.Status{resourcegraph.StatusLive, resourcegraph.StatusOffline, resourcegraph.StatusUnknown, resourcegraph.StatusMissingRoot} {
			for _, hasTmux := range []bool{false, true} {
				for _, missing := range []bool{false, true} {
					want := []Action{ActionStart, ActionDelete}
					switch {
					case missing || status == resourcegraph.StatusMissingRoot:
						want = []Action{ActionRebind, ActionDelete}
					case status == resourcegraph.StatusUnknown:
						want = []Action{ActionDelete}
					case hasTmux || status == resourcegraph.StatusLive:
						want = []Action{ActionOpen, ActionDelete}
					}
					if got := containerActions(kind, status, missing, hasTmux); !reflect.DeepEqual(got, want) {
						t.Fatalf("%s %s tmux=%t missing=%t got=%v want=%v", kind, status, hasTmux, missing, got, want)
					}
				}
			}
		}
	}
}

func TestProcessContainerGraphAndPickerStatusAgree(t *testing.T) {
	for _, virtual := range []bool{false, true} {
		for _, status := range []resourcegraph.Status{resourcegraph.StatusLive, resourcegraph.StatusOffline, resourcegraph.StatusUnknown} {
			t.Run(fmt.Sprintf("virtual=%t/%s", virtual, status), func(t *testing.T) {
				reg := registryFixture()
				reg.Panes = reg.Panes[1:]
				pane := &reg.Panes[0]
				pane.Spec.Runtime.Kind = coremetadata.RuntimeProcess
				pane.Status.Activation.Generation = "current"
				if virtual {
					reg.Windows[0].Spec.AnchorPaneRef = pane.Metadata.UID
				} else {
					reg.Windows[0].Spec.AnchorPaneRef = ""
				}
				key := resourcegraph.ProcessKey{Host: "host", Pane: pane.Metadata.UID, Generation: "current"}
				inv := resourcegraph.Inventory{Transport: resourcegraph.Transport{Kind: resourcegraph.TransportSocketPath, Value: "/isolated/socket"}, Processes: resourcegraph.ProcessInventory{Declared: []resourcegraph.ProcessKey{key}, Observed: []resourcegraph.ProcessObservation{{Key: key, Status: status}}}}
				graph := resourcegraph.Resolve(reg, inv)
				view := Build(Input{Graph: graph})
				project, _ := view.Row("uid:proj-bravo")
				if project.Status != status {
					t.Fatalf("project=%+v", project)
				}
				window, _ := view.Row("uid:win-main")
				want := status
				if virtual && status == resourcegraph.StatusOffline {
					want = StatusVirtual
				}
				if window.Status != want || window.Live != (status == resourcegraph.StatusLive) || window.Active || window.Runtime != nil {
					t.Fatalf("window=%+v want %s", window, want)
				}
				wantActions := []Action{ActionStart, ActionDelete}
				if status == resourcegraph.StatusUnknown {
					wantActions = []Action{ActionDelete}
				} else if status == resourcegraph.StatusLive || virtual {
					wantActions = []Action{ActionOpen, ActionDelete}
				}
				if !reflect.DeepEqual(window.Actions, wantActions) {
					t.Fatalf("actions=%v want %v", window.Actions, wantActions)
				}
				agent, _ := view.Row("uid:agent-one")
				leaf, _ := view.Row("uid:" + pane.Metadata.UID)
				wantAgentActions := []Action{ActionDelete}
				if status == resourcegraph.StatusUnknown {
					wantAgentActions = []Action{ActionResume, ActionMarkOffline, ActionDelete}
				}
				if agent.Status != status || leaf.Status != status || !reflect.DeepEqual(agent.Actions, wantAgentActions) || !reflect.DeepEqual(leaf.Actions, []Action{ActionDelete}) {
					t.Fatalf("leaf changed agent=%+v pane=%+v", agent, leaf)
				}
			})
		}
	}
}
