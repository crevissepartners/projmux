package registryview

import (
	"reflect"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
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
		got := agentActions(resourcegraph.AgentNode{Process: tc.process, Status: tc.status, MissingRoot: tc.missingRoot}, nil)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("process=%t status=%s missingRoot=%t: %v, want %v", tc.process != nil, tc.status, tc.missingRoot, got, tc.want)
		}
	}
}

// Offline is an observation backed by this retired generation's exact Wait,
// not an inference from PhaseOffline or a durable resumable flag alone.
func TestOfflineProcessResumeActions(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, retirement := range []string{"exact-wait", "host-lost"} {
			t.Run(provider+"/"+retirement, func(t *testing.T) {
				registry := registryFixture()
				agent, _ := registry.Agent("agent-one")
				agent.Spec.Provider = provider
				agent.Status.Phase = coremetadata.PhaseOffline
				pane, _ := registry.Pane("pane-agent")
				pane.Spec.Runtime.Kind = coremetadata.RuntimeProcess
				binding := coremetadata.ProcessBinding{
					HostInstanceID: "host", ProjectUID: "proj-bravo", WindowUID: "win-main",
					AgentUID: "agent-one", PaneUID: "pane-agent", Generation: "retired", OperationID: "op-retired",
				}
				pane.Status.ProcessSession = &coremetadata.ProcessSessionRecord{
					Provider: provider, Binding: binding, ConnectionID: "op-retired", ResumeState: coremetadata.ProcessResumable,
				}
				if provider == "codex" {
					pane.Status.ProcessSession.ThreadID = "thread-one"
				} else {
					pane.Status.ProcessSession.SessionID = "session-one"
				}
				code := 0
				pane.Status.LastTermination = &coremetadata.TerminationEvidence{
					Source: coremetadata.TerminationSourceSupervisor, Classification: coremetadata.ClassifyProcessExit(0, ""),
					ObservedAt: pane.Metadata.CreatedAt.Add(1), ExitCode: &code,
					AgentUID: binding.AgentUID, PaneUID: binding.PaneUID, Generation: binding.Generation, OperationID: binding.OperationID,
				}
				if retirement == "host-lost" {
					pane.Status.LastTermination.Source = coremetadata.TerminationSourceReconcile
					pane.Status.LastTermination.Classification = coremetadata.TerminationUnknown
					pane.Status.LastTermination.ExitCode = nil
				}
				key := resourcegraph.ProcessKey{Host: binding.HostInstanceID, Pane: binding.PaneUID, Generation: binding.Generation}
				inventory := resourcegraph.Inventory{Processes: resourcegraph.ProcessInventory{
					Declared: []resourcegraph.ProcessKey{key},
					Observed: []resourcegraph.ProcessObservation{{Key: key, Status: resourcegraph.StatusOffline}},
				}}
				graph := resourcegraph.Resolve(registry, inventory)
				view := Build(Input{Graph: graph})
				row := rowByID(t, view, "uid:agent-one")
				if row.Status != resourcegraph.StatusOffline {
					t.Fatalf("fixture must prove Offline with exact retirement evidence: %s", row.Status)
				}
				want := []Action{ActionResume, ActionDelete}
				if !reflect.DeepEqual(row.Actions, want) {
					t.Errorf("retired resumable %s actions = %v, want %v", provider, row.Actions, want)
				}
			})
		}
	}
}
