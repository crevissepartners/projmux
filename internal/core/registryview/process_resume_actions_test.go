package registryview

import (
	"reflect"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

// These graph-level guards deliberately keep the Offline overlay while
// removing one durable candidate condition. Observation and candidacy are
// separate requirements; a resumable flag must not authorize a different row.
func TestOfflineProcessResumeRejectsNonCandidates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*resourcegraph.Graph)
	}{
		{"missing root", func(g *resourcegraph.Graph) { g.Agents[0].MissingRoot = true }},
		{"running phase", func(g *resourcegraph.Graph) { g.Agents[0].Agent.Status.Phase = coremetadata.PhaseRunning }},
		{"pending phase", func(g *resourcegraph.Graph) { g.Agents[0].Agent.Status.Phase = coremetadata.PhasePending }},
		{"failed phase", func(g *resourcegraph.Graph) { g.Agents[0].Agent.Status.Phase = coremetadata.PhaseFailed }},
		{"missing pane", func(g *resourcegraph.Graph) { g.Panes = nil }},
		{"ambiguous process panes", func(g *resourcegraph.Graph) {
			other := g.Panes[0]
			other.Pane = other.Pane.Clone()
			other.Pane.Metadata.UID = "pane-other"
			g.Panes = append(g.Panes, other)
		}},
		{"foreign owner", func(g *resourcegraph.Graph) { g.Panes[0].Pane.Metadata.OwnerRef.UID = "agent-other" }},
		{"foreign pane ref", func(g *resourcegraph.Graph) { g.Agents[0].Agent.Status.PaneRef = "pane-other" }},
		{"foreign graph pane", func(g *resourcegraph.Graph) { g.Agents[0].PaneUID = "pane-other" }},
		{"tmux pane", func(g *resourcegraph.Graph) { g.Panes[0].Pane.Spec.Runtime.Kind = coremetadata.RuntimeTmux }},
		{"activation not retired", func(g *resourcegraph.Graph) { g.Panes[0].Pane.Status.Activation.Generation = "retired" }},
		{"no session", func(g *resourcegraph.Graph) { g.Panes[0].Pane.Status.ProcessSession = nil }},
		{"unknown resume state", func(g *resourcegraph.Graph) {
			g.Panes[0].Pane.Status.ProcessSession.ResumeState = coremetadata.ProcessResumeUnknown
		}},
		{"foreign binding agent", func(g *resourcegraph.Graph) { g.Panes[0].Pane.Status.ProcessSession.Binding.AgentUID = "agent-other" }},
		{"foreign binding pane", func(g *resourcegraph.Graph) { g.Panes[0].Pane.Status.ProcessSession.Binding.PaneUID = "pane-other" }},
		{"foreign generation", func(g *resourcegraph.Graph) { g.Panes[0].Pane.Status.ProcessSession.Binding.Generation = "other" }},
		{"foreign host", func(g *resourcegraph.Graph) { g.Panes[0].Pane.Status.ProcessSession.Binding.HostInstanceID = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := registryFixture()
			agent, _ := registry.Agent("agent-one")
			agent.Status.Phase = coremetadata.PhaseOffline
			pane, _ := registry.Pane("pane-agent")
			pane.Spec.Runtime.Kind = coremetadata.RuntimeProcess
			pane.Status.ProcessSession = &coremetadata.ProcessSessionRecord{
				Provider: "codex", ThreadID: "thread-one", ConnectionID: "op-retired", ResumeState: coremetadata.ProcessResumable,
				Binding: coremetadata.ProcessBinding{HostInstanceID: "host", ProjectUID: "proj-bravo", WindowUID: "win-main", AgentUID: "agent-one", PaneUID: "pane-agent", Generation: "retired", OperationID: "op-retired"},
			}
			key := resourcegraph.ProcessKey{Host: "host", Pane: "pane-agent", Generation: "retired"}
			graph := resourcegraph.Resolve(registry, resourcegraph.Inventory{})
			graph.Agents[0].Process = &key
			graph.Agents[0].Status = resourcegraph.StatusOffline
			// Drop the unrelated shell Pane to leave only this candidate.
			for _, node := range graph.Panes {
				if node.Pane.Metadata.UID == "pane-agent" {
					graph.Panes = []resourcegraph.PaneNode{node}
					break
				}
			}
			graph.Panes[0].Process = &key
			graph.Panes[0].Status = resourcegraph.StatusOffline
			tc.change(&graph)
			row := rowByID(t, Build(Input{Graph: graph}), "uid:agent-one")
			if !reflect.DeepEqual(row.Actions, []Action{ActionDelete}) {
				t.Fatalf("noncandidate actions = %v, want [delete]", row.Actions)
			}
		})
	}
}
