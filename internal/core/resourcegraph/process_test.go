package resourcegraph

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestProcessSpecificationRequiresHostEvidence(t *testing.T) {
	reg := testRegistry(t)
	pane, _ := reg.Pane("pane-alpha-agent")
	pane.Spec.Runtime.Kind = coremetadata.RuntimeProcess
	pane.Status.Activation = coremetadata.PaneActivation{}
	graph := Resolve(reg, Inventory{})
	node := paneNode(t, graph, pane.Metadata.UID)
	if node.Process == nil || node.Runtime != nil || node.Status != StatusUnknown {
		t.Fatalf("process declaration lost without host evidence: %+v", node)
	}
	if !(ProcessInventory{}).Declares(*pane) {
		t.Fatal("process specification lost convergence protection")
	}
	if _, err := (ProcessInventory{}).AdmitProcess(*pane, ProcessTurn); err == nil {
		t.Fatal("missing activation authorized a process turn")
	}
}

func TestProcessInventoryMixedMissingRootAndUnavailable(t *testing.T) {
	for _, missing := range []bool{false, true} {
		for _, live := range []bool{false, true} {
			registry := testRegistry(t)
			pane, _ := registry.Pane("pane-alpha-agent")
			pane.Status.Activation.Generation = "generation-one"
			key := ProcessKey{Host: "owned-host", Pane: pane.Metadata.UID, Generation: pane.Status.Activation.Generation}
			inventory := Inventory{Processes: ProcessInventory{Declared: []ProcessKey{key}}}
			if live {
				inventory.Processes.Observed = []ProcessObservation{{Key: key, Status: StatusLive}}
			}
			if missing {
				registry.Projects[0].Status.Conditions = registry.Projects[2].Status.Conditions
			}
			before, _ := json.Marshal(registry)
			graph := Resolve(registry, inventory)
			var found *PaneNode
			for i := range graph.Panes {
				if graph.Panes[i].Pane.Metadata.UID == key.Pane {
					found = &graph.Panes[i]
				}
			}
			want := StatusUnknown
			if live {
				want = StatusLive
			}
			if found == nil || found.Process == nil || *found.Process != key || found.Runtime != nil || found.Status != want || found.WindowUID != "win-alpha-1" || found.AgentUID != "agent-alpha-1" || found.MissingRoot != missing {
				t.Fatalf("missing=%v live=%v: %+v", missing, live, found)
			}
			for _, agent := range graph.Agents {
				if agent.PaneUID == key.Pane && (agent.Process == nil || agent.Status != want || agent.Runtime != nil) {
					t.Fatalf("agent: %+v", agent)
				}
			}
			after, _ := json.Marshal(registry)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("Resolve changed durable Registry")
			}
			// Mutating an invocation input cannot change the graph's typed identity.
			inventory.Processes.Declared[0].Host = "replacement"
			if found.Process.Host != "owned-host" {
				t.Fatal("graph aliases declaration")
			}
		}
	}
}

func TestProcessInventoryRejectsStaleDuplicateAndTmuxClaims(t *testing.T) {
	registry := testRegistry(t)
	pane, _ := registry.Pane("pane-alpha-agent")
	pane.Status.Activation.Generation = "current"
	key := ProcessKey{Host: "host", Pane: pane.Metadata.UID, Generation: "current"}
	for _, observations := range [][]ProcessObservation{
		{{Key: ProcessKey{Host: "host", Pane: key.Pane, Generation: "old"}, Status: StatusLive}},
		{{Key: key, Status: StatusLive}, {Key: key, Status: StatusOffline}},
	} {
		inventory := Inventory{Processes: ProcessInventory{Declared: []ProcessKey{key}, Observed: observations}}
		_, status := inventory.Processes.current(*pane)
		if status != StatusUnknown {
			t.Fatalf("stale/duplicate accepted: %s", status)
		}
		if _, err := inventory.Processes.AdmitProcess(*pane, ProcessTurn); err == nil {
			t.Fatal("uncertain target admitted")
		}
	}
	inventory := Inventory{Panes: []Pane{{ID: "%7", UID: key.Pane}}, Processes: ProcessInventory{Declared: []ProcessKey{key}, Observed: []ProcessObservation{{Key: key, Status: StatusLive}}}}
	graph := Resolve(registry, inventory)
	if len(graph.Conflicts) != 1 {
		t.Fatalf("contradicting tmux claim: %+v", graph.Conflicts)
	}
	for _, node := range graph.Panes {
		if node.Pane.Metadata.UID == key.Pane && (node.Runtime != nil || node.Status != StatusUnknown || node.Class != ClassConflict) {
			t.Fatalf("conflict gained authority: %+v", node)
		}
	}
	inventory.Processes.Declared = append(inventory.Processes.Declared, key)
	if _, err := inventory.Processes.AdmitProcess(*pane, ProcessStop); err == nil {
		t.Fatal("duplicate declaration admitted")
	}
}

func TestProcessInventorySharesWindowWithLiveTmuxSibling(t *testing.T) {
	registry := testRegistry(t)
	pane, _ := registry.Pane("pane-alpha-agent")
	pane.Status.Activation.Generation = "process-generation"
	key := ProcessKey{Host: "host", Pane: pane.Metadata.UID, Generation: pane.Status.Activation.Generation}
	inventory := liveInventory(HostModeAppOwned)
	inventory.Panes = append(inventory.Panes[:1], inventory.Panes[2:]...)
	inventory.Processes = ProcessInventory{Declared: []ProcessKey{key}, Observed: []ProcessObservation{{Key: key, Status: StatusLive}}}
	graph := Resolve(registry, inventory)
	process, sibling := paneNode(t, graph, key.Pane), paneNode(t, graph, "pane-alpha-1")
	if process.Process == nil || process.Runtime != nil || process.Status != StatusLive || sibling.Process != nil || sibling.Runtime == nil || sibling.Status != StatusLive || process.WindowUID != sibling.WindowUID {
		t.Fatalf("mixed Window lost identity: process=%+v tmux=%+v", process, sibling)
	}
}

func TestProcessAdmissionCapabilities(t *testing.T) {
	pane := coremetadata.Pane{Metadata: coremetadata.ObjectMeta{UID: "pane"}, Status: coremetadata.PaneStatus{Activation: coremetadata.PaneActivation{Generation: "generation"}}}
	key := ProcessKey{Host: "host", Pane: "pane", Generation: "generation"}
	for _, status := range []Status{StatusUnknown, StatusLive, StatusOffline} {
		inventory := ProcessInventory{Declared: []ProcessKey{key}, Observed: []ProcessObservation{{Key: key, Status: status}}}
		for _, action := range []ProcessAction{ProcessAttach, ProcessFocus, ProcessKeys, ProcessCapture, ProcessPopup, ProcessRelaunch} {
			if _, err := inventory.AdmitProcess(pane, action); err == nil {
				t.Fatalf("%s admitted %s", status, action)
			}
		}
		for _, action := range []ProcessAction{ProcessTurn, ProcessInterrupt, ProcessStop} {
			got, err := inventory.AdmitProcess(pane, action)
			if (err == nil) != (status == StatusLive) || err == nil && got != key {
				t.Fatalf("%s %s: %+v %v", status, action, got, err)
			}
		}
	}
}

func TestProcessActionRefusalTokens(t *testing.T) {
	reg := testRegistry(t)
	pane, _ := reg.Pane("pane-alpha-agent")
	pane.Spec.Runtime.Kind = coremetadata.RuntimeProcess
	for _, action := range []ProcessAction{ProcessAttach, ProcessFocus, ProcessKeys, ProcessCapture, ProcessPopup, ProcessRelaunch, ProcessCreatePane, ProcessSplit} {
		_, err := (ProcessInventory{}).AdmitProcess(*pane, action)
		token := "process-" + string(action) + "-unsupported:"
		if err == nil || !strings.HasPrefix(err.Error(), token) {
			t.Fatalf("%s: %v", action, err)
		}
	}
}
