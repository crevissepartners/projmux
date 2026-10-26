package app

import (
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/i18n"
)

func TestRegistryNavigationOfflineProcessResumeIsCLIAdvice(t *testing.T) {
	registry := processResumeQueryFixture(t)
	candidate := listResumableProcessAgents(registry, processResumeFilter{})[0]
	pane, _ := registry.Pane(candidate.Pane.Metadata.UID)
	binding := candidate.Record.Binding
	code := 0
	pane.Status.LastTermination = &coremetadata.TerminationEvidence{
		Source: coremetadata.TerminationSourceSupervisor, Classification: coremetadata.ClassifyProcessExit(0, ""),
		ObservedAt: pane.Metadata.CreatedAt.Add(1), ExitCode: &code,
		AgentUID: binding.AgentUID, PaneUID: binding.PaneUID, Generation: binding.Generation, OperationID: binding.OperationID,
	}
	key := resourcegraph.ProcessKey{Host: binding.HostInstanceID, Pane: binding.PaneUID, Generation: binding.Generation}
	graph := resourcegraph.Resolve(registry, resourcegraph.Inventory{Processes: resourcegraph.ProcessInventory{
		Declared: []resourcegraph.ProcessKey{key},
		Observed: []resourcegraph.ProcessObservation{{Key: key, Status: resourcegraph.StatusOffline}},
	}})
	view := registryview.Build(registryview.Input{Graph: graph})
	row, ok := view.Row("uid:" + binding.AgentUID)
	if !ok || row.Status != resourcegraph.StatusOffline || row.Process == nil {
		t.Fatalf("missing retired process row: %+v", row)
	}
	for _, inside := range []bool{false, true} {
		nav := registryNavigationView{locale: i18n.FallbackLocale, view: view}
		found := false
		for _, entry := range nav.actionEntries(row, "/isolated/tmux.sock", inside) {
			if entry.Value == navActionResume {
				t.Fatal("process resume must not execute a picker action")
			}
			if strings.Contains(entry.Label, "projmux agent resume uid:"+binding.AgentUID) {
				found = true
				if entry.Value != settingsNoopValue {
					t.Fatalf("CLI advice must be non-executing: %+v", entry)
				}
			}
		}
		if !found {
			t.Errorf("insideTmux=%t: same row has no resume CLI advice", inside)
		}
	}
}
