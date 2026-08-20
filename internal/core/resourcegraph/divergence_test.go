package resourcegraph

import (
	"fmt"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestDivergenceTaxonomyTableIsClosedAndPrintable(t *testing.T) {
	want := []Divergence{
		DivergenceUnrealized, DivergenceUnattributed, DivergenceOrphanMirror,
		DivergenceContaminated, DivergenceDrifted, DivergenceUnknown,
	}
	if got := Divergences(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("Divergences() = %v, want %v", got, want)
	}
	for _, divergence := range want {
		if !divergence.Valid() || strings.TrimSpace(string(divergence)) == "" {
			t.Fatalf("taxonomy contains an invalid printable value %q", divergence)
		}
		t.Logf("%s", divergence)
	}
}

func TestDivergenceClassifierCoversEveryRegistryInventoryCombination(t *testing.T) {
	registry := testRegistry(t)
	tests := []struct {
		name      string
		registry  coremetadata.Registry
		inventory Inventory
		want      Divergence
	}{
		{"D1 unrealized", registry, Inventory{Transport: Transport{Kind: TransportSocketName}, HostMode: HostModeAppOwned}, DivergenceUnrealized},
		{"D2 unattributed", coremetadata.NewRegistry(), Inventory{Transport: Transport{Kind: TransportSocketName}, HostMode: HostModeAppOwned, Sessions: []Session{{ID: "$9", Name: "plain"}}}, DivergenceUnattributed},
		{"D3 orphan mirror", coremetadata.NewRegistry(), Inventory{Transport: Transport{Kind: TransportSocketName}, HostMode: HostModeAppOwned, Sessions: []Session{{ID: "$9", Name: "plain", ProjectUID: "missing"}}}, DivergenceOrphanMirror},
		{"D4 contamination", registry, func() Inventory {
			inv := liveInventory(HostModeAppOwned)
			inv.Windows = append(inv.Windows, Window{ID: "@9", SessionID: "$1", UID: "win-alpha-1"})
			return inv
		}(), DivergenceContaminated},
		{"D5 drift", registry, func() Inventory {
			inv := liveInventory(HostModeAppOwned)
			inv.Panes[0].MirroredName = "wrong"
			return inv
		}(), DivergenceDrifted},
		{"D6 unknown", registry, Inventory{Transport: Transport{Kind: TransportNone, Source: TransportSourceNone}, HostMode: HostModeUnknown}, DivergenceUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			items := ClassifyDivergences(test.registry, test.inventory)
			found := false
			for _, item := range items {
				if !item.Divergence.Valid() || item.Key == "" || strings.TrimSpace(item.Reason) == "" {
					t.Fatalf("unclassified item: %+v", item)
				}
				found = found || item.Divergence == test.want
			}
			if !found {
				t.Fatalf("items = %+v, want at least one %s", items, test.want)
			}
		})
	}
}

func TestIncident20260820ClassifiesExactlyTwoOrphanMirrors(t *testing.T) {
	items := ClassifyDivergences(coremetadata.NewRegistry(), Inventory{
		Transport: Transport{Kind: TransportSocketName, Value: "projmux", Source: TransportSourceSocketName},
		HostMode:  HostModeAppOwned,
		Sessions:  []Session{{ID: "$1", Name: "home"}},
		Windows:   []Window{{ID: "@1", SessionID: "$1"}},
		Panes: []Pane{
			{ID: "%1", WindowID: "@1", UID: "pane-orphan-one"},
			{ID: "%2", WindowID: "@1", UID: "pane-orphan-two"},
		},
	})
	count := 0
	for _, item := range items {
		if item.Divergence == DivergenceOrphanMirror {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("D3 count = %d, want 2; items=%+v", count, items)
	}
}

func TestControlSessionRootPreservesRuntimeAndDescendantAttribution(t *testing.T) {
	now := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	owner := func(kind coremetadata.Kind, uid string) *coremetadata.OwnerRef {
		return &coremetadata.OwnerRef{Kind: kind, UID: uid}
	}
	registry := coremetadata.NewRegistry()
	registry.ControlSessions = []coremetadata.ControlSession{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindControlSession,
		Metadata: coremetadata.ObjectMeta{UID: "ctl-home", Name: "home", CreatedAt: now},
		Spec:     coremetadata.ControlSessionSpec{Session: "home"},
	}}
	registry.Windows = []coremetadata.Window{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
		Metadata: coremetadata.ObjectMeta{UID: "win-home", Name: "window", OwnerRef: owner(coremetadata.KindControlSession, "ctl-home"), CreatedAt: now},
		Spec:     coremetadata.WindowSpec{PrimaryPaneRef: "pane-home"},
	}}
	registry.Panes = []coremetadata.Pane{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
		Metadata: coremetadata.ObjectMeta{UID: "pane-home", Name: "shell", OwnerRef: owner(coremetadata.KindWindow, "win-home"), CreatedAt: now},
		Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell},
	}}
	graph := Resolve(registry, Inventory{
		Transport: Transport{Kind: TransportSocketName, Value: "projmux", Source: TransportSourceSocketName}, HostMode: HostModeAppOwned,
		Sessions: []Session{{ID: "$1", Name: "home", Role: ControlSessionRole}},
		Windows:  []Window{{ID: "@1", SessionID: "$1", UID: "win-home"}},
		Panes:    []Pane{{ID: "%1", WindowID: "@1", UID: "pane-home"}},
	})
	if len(graph.ControlSessions) != 1 || graph.ControlSessions[0].Status != StatusLive {
		t.Fatalf("control roots = %+v, want one live root", graph.ControlSessions)
	}
	if node := runtimeNode(t, graph, "$1"); node.Class != ClassManaged || node.ResourceUID != "ctl-home" {
		t.Fatalf("Home runtime = %+v, want managed ControlSession binding", node)
	}
	if node := windowNode(t, graph, "win-home"); node.RootKind != coremetadata.KindControlSession || node.RootUID != "ctl-home" || node.ProjectUID != "" {
		t.Fatalf("control Window attribution = %+v", node)
	}
}

func FuzzDivergenceClassifierLabelsEveryRegistryInventoryItem(f *testing.F) {
	f.Add([]byte{1, 1, 0, 1})
	f.Add([]byte{0, 2, 1, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		registry := coremetadata.NewRegistry()
		inventory := Inventory{Transport: Transport{Kind: TransportSocketName, Source: TransportSourceSocketName}, HostMode: HostModeAppOwned}
		for index, value := range data {
			uid := fmt.Sprintf("uid-%d-%d", index, value%4)
			switch value % 4 {
			case 0:
				registry.Projects = append(registry.Projects, coremetadata.Project{Kind: coremetadata.KindProject, Metadata: coremetadata.ObjectMeta{UID: uid, Name: uid}, Spec: coremetadata.ProjectSpec{Root: "/" + uid}})
			case 1:
				inventory.Sessions = append(inventory.Sessions, Session{ID: fmt.Sprintf("$%d", index), Name: uid})
			case 2:
				inventory.Sessions = append(inventory.Sessions, Session{ID: fmt.Sprintf("$%d", index), Name: uid, ProjectUID: uid})
			case 3:
				inventory = inventory.MarkUnavailable(ScopeSessions, "generated observation failure")
			}
		}
		items := ClassifyDivergences(registry, inventory)
		for _, item := range items {
			if !item.Divergence.Valid() || item.Key == "" || strings.TrimSpace(item.Reason) == "" {
				t.Fatalf("classifier emitted an unclassified item: %+v", item)
			}
		}
		counts := CountDivergences(items)
		total := 0
		for _, count := range counts {
			total += count.Count
		}
		if total != len(items) {
			t.Fatalf("classified count = %d, items = %d", total, len(items))
		}
	})
}
