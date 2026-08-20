package app

import (
	"encoding/json"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/core/runtimediag"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

func TestResourceReconcilePlanItemsHaveExactlyOneDivergenceAndReason(t *testing.T) {
	command, _, _, _, _ := newReconcileFixture(t, "-L", "primary")
	stdout, _, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("dry-run: %v\n%s", err, stdout)
	}
	var report resourceReconcileReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Items) == 0 {
		t.Fatal("fixture produced no plan items")
	}
	for _, item := range report.Items {
		if !item.Divergence.Valid() || strings.TrimSpace(item.Reason) == "" {
			t.Fatalf("plan item is not exactly classified with a reason: %+v", item)
		}
	}
	if len(report.DivergenceCounts) != len(resourcegraph.Divergences()) {
		t.Fatalf("divergence counts = %+v, want all six classes", report.DivergenceCounts)
	}
	total := 0
	for _, count := range report.DivergenceCounts {
		total += count.Count
	}
	if total != len(report.Items) {
		t.Fatalf("classified count = %d, plan items = %d", total, len(report.Items))
	}
}

func TestControlOwnedHomeGraphSurvivesSidebarRuntimeAndSessionAttribution(t *testing.T) {
	registry := controlOwnerFixtureRegistry(true)
	graph := resourcegraph.Resolve(registry, resourcegraph.Inventory{
		Transport: resourcegraph.Transport{Kind: resourcegraph.TransportSocketName, Value: "projmux", Source: resourcegraph.TransportSourceSocketName},
		HostMode:  resourcegraph.HostModeAppOwned,
		Sessions: []resourcegraph.Session{
			{ID: "$0", Name: "alpha", ProjectUID: "prj-alpha", ProjectName: "alpha", Root: "/srv/alpha"},
			{ID: "$1", Name: "home", Role: resourcegraph.ControlSessionRole},
		},
		Windows: []resourcegraph.Window{
			{ID: "@0", SessionID: "$0", UID: "win-alpha-main"},
			{ID: "@1", SessionID: "$1", UID: "win-home"},
		},
		Panes: []resourcegraph.Pane{
			{ID: "%0", WindowID: "@0", UID: "pan-alpha-zsh"},
			{ID: "%1", WindowID: "@1", UID: "pan-home"},
		},
	})

	view := registryview.Build(registryview.Input{Graph: graph})
	controlRows := view.Section(registryview.SectionControl)
	if len(controlRows) != 3 || controlRows[0].Kind != registryview.RowKindControlSession {
		t.Fatalf("control sidebar projection = %+v, want root/Window/Pane", controlRows)
	}
	for _, row := range controlRows {
		if row.Root != "" || len(row.Actions) != 0 {
			t.Fatalf("control row gained Project path or Phase 14 actions: %+v", row)
		}
	}

	rows := runtimediag.Rows(graph)
	foundControl := false
	for _, row := range rows {
		if row.ID == "$1" {
			foundControl = row.Managed() && row.Resource != nil && row.Resource.Kind == string(coremetadata.KindControlSession) && row.Resource.UID == "ctl-home"
		}
	}
	if !foundControl {
		t.Fatalf("runtime diagnostics lost ControlSession root: %+v", rows)
	}

	attribution := attributeSessionSummaries(graph, []inttmux.RecentSessionSummary{
		{ID: "$0", Name: "alpha"}, {ID: "$1", Name: "home"},
	})
	if len(attribution.managed) != 1 || attribution.managed[0].Name != "alpha" || attribution.withheld.Control != 1 {
		t.Fatalf("session attribution = %+v, want Project managed and Home control-withheld", attribution)
	}
}
