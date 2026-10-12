package app

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// A raw tab is what tmux 3.5a sanitizes to `_`, which parsed as one field and
// made equalization a silent no-op on that version.
func TestSplitLayoutBatchFormatCarriesNoRawTab(t *testing.T) {
	t.Parallel()

	if strings.Contains(splitLayoutBatchFormat, "\t") {
		t.Fatalf("layout receipt format carries a raw tab: %q", splitLayoutBatchFormat)
	}
}

func TestSplitLayoutPeersAreAxisScopedAndOrdered(t *testing.T) {
	t.Parallel()

	panes := []aiPaneGeometry{
		{Id: "%right-last", Left: 42, Top: 0, Width: 20, Height: 10},
		{Id: "%other-row", Left: 0, Top: 11, Width: 62, Height: 9},
		{Id: "%anchor", Left: 21, Top: 0, Width: 20, Height: 10},
		{Id: "%right-first", Left: 0, Top: 0, Width: 20, Height: 10},
		{Id: "%other-column", Left: 63, Top: 0, Width: 10, Height: 21},
	}
	if got, want := paneGeometryIDs(splitLayoutPeers(panes, panes[2], "right")), []string{"%right-first", "%anchor", "%right-last"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("right peers = %v, want %v", got, want)
	}

	panes = []aiPaneGeometry{
		{Id: "%down-last", Left: 0, Top: 22, Width: 40, Height: 10},
		{Id: "%other-column", Left: 41, Top: 0, Width: 20, Height: 32},
		{Id: "%anchor", Left: 0, Top: 11, Width: 40, Height: 10},
		{Id: "%down-first", Left: 0, Top: 0, Width: 40, Height: 10},
	}
	if got, want := paneGeometryIDs(splitLayoutPeers(panes, panes[2], placementDown)), []string{"%down-first", "%anchor", "%down-last"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("down peers = %v, want %v", got, want)
	}
}

func TestResizePanesEvenlyDistributesDeterministicRemainder(t *testing.T) {
	t.Parallel()

	peers := []aiPaneGeometry{{Id: "%1", Width: 10}, {Id: "%2", Width: 10}, {Id: "%3", Width: 11}}
	var got []string
	resizePanesEvenly(peers, func(p aiPaneGeometry, size int) {
		got = append(got, p.Id+"="+strconv.Itoa(size))
	}, func(p aiPaneGeometry) int { return p.Width })
	if want := []string{"%1=11", "%2=10", "%3=10"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resize distribution = %v, want %v", got, want)
	}
}

func TestPlanEvenSplitResizesNoOpsWithoutExactMultiPaneAnchor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		panes []aiPaneGeometry
	}{
		{name: "empty"},
		{name: "one pane", panes: []aiPaneGeometry{{Id: "%1", Width: 80, Height: 24}}},
		{name: "target absent", panes: []aiPaneGeometry{{Id: "%2", Width: 80, Height: 24}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			planned := planEvenSplitResizes("%1", "right", test.panes)
			if len(planned) != 0 {
				t.Fatalf("resize plan = %#v, want empty", planned)
			}
		})
	}
}

func TestPlanEvenSplitResizesReturnsTypedOrderedOperandsAndIgnoresUnrelatedTopology(t *testing.T) {
	t.Parallel()

	planned := planEvenSplitResizes("%2", "right", []aiPaneGeometry{
		{Id: "%1", Left: 0, Top: 0, Width: 20, Height: 10},
		{Id: "%2", Left: 21, Top: 0, Width: 10, Height: 10},
		{Id: "%3", Left: 32, Top: 0, Width: 10, Height: 10},
		{Id: "%4", Left: 0, Top: 11, Width: 42, Height: 10},
	})
	want := []plannedPaneResize{
		{PaneID: "%1", Axis: "-x", Size: 14},
		{PaneID: "%2", Axis: "-x", Size: 13},
		{PaneID: "%3", Axis: "-x", Size: 13},
	}
	if !reflect.DeepEqual(planned, want) {
		t.Fatalf("resize plan = %v, want %v", planned, want)
	}
}
