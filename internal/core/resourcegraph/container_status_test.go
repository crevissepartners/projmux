package resourcegraph

import (
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"reflect"
	"testing"
)

// TestContainerStatusRuntimeEvidenceTable pins C-1 without treating Agent phase
// as execution evidence. The process observation applies only to its descendants.
func TestContainerStatusRuntimeEvidenceTable(t *testing.T) {
	for _, mode := range []string{"tmux", "process", "both", "none"} {
		for _, tmuxOK := range []bool{true, false} {
			for _, processOK := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/tmux=%t/process=%t", mode, tmuxOK, processOK), func(t *testing.T) {
					reg := testRegistry(t)
					pane, _ := reg.Pane("pane-alpha-agent")
					hasProcess := mode == "process" || mode == "both"
					if hasProcess {
						pane.Spec.Runtime.Kind = coremetadata.RuntimeProcess
						pane.Status.Activation.Generation = "current"
					}
					inv := Inventory{Transport: Transport{Kind: TransportSocketPath, Value: "/isolated/socket"}}
					hasTmux := mode == "tmux" || mode == "both"
					if !tmuxOK {
						inv.Unavailable = []Unavailability{{Scope: ScopeSessions, Reason: "failed"}, {Scope: ScopeWindows, Reason: "failed"}, {Scope: ScopePanes, Reason: "failed"}}
					} else if hasTmux {
						inv = liveInventory(HostModeAppOwned)
						// A process Pane cannot also claim tmux.
						inv.Panes = inv.Panes[:1]
					}
					if hasProcess {
						key := ProcessKey{Host: "host", Pane: pane.Metadata.UID, Generation: "current"}
						inv.Processes.Declared = []ProcessKey{key}
						if processOK {
							inv.Processes.Observed = []ProcessObservation{{Key: key, Status: StatusLive}}
						}
					}
					before := reg.Clone()
					graph := Resolve(reg, inv)
					want := StatusOffline
					if !tmuxOK || hasProcess && !processOK {
						want = StatusUnknown
					}
					if hasTmux && tmuxOK || hasProcess && processOK {
						want = StatusLive
					}
					for _, p := range graph.Projects {
						if p.Project.Metadata.UID == "project-alpha" && p.Status != want {
							t.Fatalf("project=%s want %s", p.Status, want)
						}
					}
					for _, w := range graph.Windows {
						if w.Window.Metadata.UID == "win-alpha-1" && (w.Status != want || w.Live != (want == StatusLive)) {
							t.Fatalf("window=%+v want %s", w, want)
						}
					}
					// The beta hierarchy has no declared process, regardless of host failure.
					betaWant := StatusOffline
					if !tmuxOK {
						betaWant = StatusUnknown
					} else if hasTmux {
						betaWant = StatusLive
					}
					for _, p := range graph.Projects {
						if p.Project.Metadata.UID == "project-beta" && p.Status != betaWant {
							t.Fatalf("unrelated project=%s want %s", p.Status, betaWant)
						}
					}
					if !reflect.DeepEqual(reg, before) {
						t.Fatal("derived state mutated Registry")
					}
				})
			}
		}
	}
}

func TestContainerStatusOfflineProcessAndMissingRoot(t *testing.T) {
	for _, missing := range []bool{false, true} {
		reg := testRegistry(t)
		pane, _ := reg.Pane("pane-alpha-agent")
		pane.Spec.Runtime.Kind = coremetadata.RuntimeProcess
		pane.Status.Activation.Generation = "current"
		if missing {
			reg.Projects[0].Status.Conditions = reg.Projects[2].Status.Conditions
		}
		key := ProcessKey{Host: "host", Pane: pane.Metadata.UID, Generation: "current"}
		for _, status := range []Status{StatusLive, StatusOffline, StatusUnknown} {
			inv := Inventory{Transport: Transport{Kind: TransportSocketPath, Value: "/isolated/socket"}, Processes: ProcessInventory{Declared: []ProcessKey{key}, Observed: []ProcessObservation{{Key: key, Status: status}}}}
			graph := Resolve(reg, inv)
			want := status
			if missing {
				want = StatusMissingRoot
			}
			for _, w := range graph.Windows {
				if w.Window.Metadata.UID == "win-alpha-1" && (w.Status != want || w.Live != (want == StatusLive) || w.Active || w.Runtime != nil) {
					t.Fatalf("status=%s missing=%t window=%+v", status, missing, w)
				}
			}
			for _, p := range graph.Projects {
				if p.Project.Metadata.UID == "project-alpha" && p.Status != want {
					t.Fatalf("project=%s want %s", p.Status, want)
				}
			}
			if got := paneNode(t, graph, key.Pane).Status; got != status {
				t.Fatalf("leaf state changed: %s want %s", got, status)
			}
		}
	}
}
