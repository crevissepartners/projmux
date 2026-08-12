package resources

import (
	"reflect"
	"testing"
)

func TestResolveProjectAnchorsDeterministicTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		inventory []PaneInventory
		roots     []string
		want      []string
	}{
		{
			name:      "explicit wins over current path",
			inventory: []PaneInventory{{PaneID: "%1", CurrentPath: "/managed/fallback/work", ProjectAnchor: "/explicit/project"}},
			roots:     []string{"/managed/fallback"},
			want:      []string{"/explicit/project"},
		},
		{
			name:      "blank anchor falls back",
			inventory: []PaneInventory{{PaneID: "%1", CurrentPath: "/managed/project/work"}},
			roots:     []string{"/managed/project"},
			want:      []string{"/managed/project"},
		},
		{
			name:      "longest root",
			inventory: []PaneInventory{{PaneID: "%1", CurrentPath: "/managed/project/services/api/work"}},
			roots:     []string{"/managed/project", "/managed/project/services/api"},
			want:      []string{"/managed/project/services/api"},
		},
		{
			name:      "home remains unmatched",
			inventory: []PaneInventory{{PaneID: "%1", CurrentPath: "/home/tester"}},
			roots:     []string{"/home/tester/source/repos/project"},
			want:      []string{""},
		},
		{
			name:      "outside remains unmatched",
			inventory: []PaneInventory{{PaneID: "%1", CurrentPath: "/tmp/scratch"}},
			roots:     []string{"/managed/project"},
			want:      []string{""},
		},
		{
			name: "linked explicit fallback conflict",
			inventory: []PaneInventory{
				{SessionID: "$1", PaneID: "%1", CurrentPath: "/managed/two/work", ProjectAnchor: "/managed/one"},
				{SessionID: "$2", PaneID: "%1", CurrentPath: "/managed/two/work"},
			},
			roots: []string{"/managed/one", "/managed/two"},
			want:  []string{"/managed/one", "/managed/two"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]PaneInventory(nil), tc.inventory...)
			got := ResolveProjectAnchors(tc.inventory, tc.roots)
			anchors := make([]string, len(got))
			for i := range got {
				anchors[i] = got[i].ProjectAnchor
			}
			if !reflect.DeepEqual(anchors, tc.want) {
				t.Fatalf("ResolveProjectAnchors() anchors = %#v, want %#v", anchors, tc.want)
			}
			if !reflect.DeepEqual(tc.inventory, before) {
				t.Fatalf("ResolveProjectAnchors() mutated input: got %#v, before %#v", tc.inventory, before)
			}
		})
	}
}

func TestResolveProjectAnchorsPreservesAttributionAggregation(t *testing.T) {
	t.Parallel()

	inventory := []PaneInventory{
		{Socket: "/s", SessionID: "$1", WindowID: "@1", PaneID: "%1", PanePID: 100, CurrentPath: "/repo/a/work"},
		{Socket: "/s", SessionID: "$2", WindowID: "@1", PaneID: "%1", PanePID: 100, CurrentPath: "/repo/b/work", ProjectAnchor: "/repo/b"},
		{Socket: "/s", SessionID: "$3", WindowID: "@2", PaneID: "%2", PanePID: 200, CurrentPath: "/outside"},
	}
	resolved := ResolveProjectAnchors(inventory, []string{"/repo/a", "/repo/b"})
	previous := sampleWithHost(1000, 600, 4, 10_000, 4_000,
		process(100, 1, 100, 10, 100), process(101, 2, 100, 20, 200), process(200, 3, 200, 30, 300))
	current := sampleWithHost(1400, 800, 4, 10_000, 4_000,
		process(100, 1, 100, 30, 110), process(101, 2, 100, 40, 220), process(200, 3, 200, 50, 330))

	got := BuildSnapshot(resolved, &previous, current)
	if got.Panes[0].ProjectKey != ProjectShared || got.Panes[1].ProjectKey != ProjectUnassigned {
		t.Fatalf("resolved project buckets = %#v", got.Panes)
	}
	if got.Panes[0].ProcessCount != 2 || got.Panes[0].Memory.RSSBytes != 330 || got.Panes[1].ProcessCount != 1 || got.Panes[1].Memory.RSSBytes != 330 {
		t.Fatalf("pane attribution changed = %#v", got.Panes)
	}
	assertFloat(t, got.Panes[0].CPU.HostSharePercent, 10)
	assertFloat(t, got.Panes[1].CPU.HostSharePercent, 5)
	if err := validateConservation(got); err != nil {
		t.Fatal(err)
	}
}
