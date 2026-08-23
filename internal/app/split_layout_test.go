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
		{id: "%right-last", left: 42, top: 0, width: 20, height: 10},
		{id: "%other-row", left: 0, top: 11, width: 62, height: 9},
		{id: "%anchor", left: 21, top: 0, width: 20, height: 10},
		{id: "%right-first", left: 0, top: 0, width: 20, height: 10},
		{id: "%other-column", left: 63, top: 0, width: 10, height: 21},
	}
	if got, want := paneGeometryIDs(splitLayoutPeers(panes, panes[2], "right")), []string{"%right-first", "%anchor", "%right-last"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("right peers = %v, want %v", got, want)
	}

	panes = []aiPaneGeometry{
		{id: "%down-last", left: 0, top: 22, width: 40, height: 10},
		{id: "%other-column", left: 41, top: 0, width: 20, height: 32},
		{id: "%anchor", left: 0, top: 11, width: 40, height: 10},
		{id: "%down-first", left: 0, top: 0, width: 40, height: 10},
	}
	if got, want := paneGeometryIDs(splitLayoutPeers(panes, panes[2], placementDown)), []string{"%down-first", "%anchor", "%down-last"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("down peers = %v, want %v", got, want)
	}
}

func TestResizePanesEvenlyDistributesDeterministicRemainder(t *testing.T) {
	t.Parallel()

	peers := []aiPaneGeometry{{id: "%1", width: 10}, {id: "%2", width: 10}, {id: "%3", width: 11}}
	var got []string
	resizePanesEvenly(peers, func(p aiPaneGeometry, size int) {
		got = append(got, p.id+"="+strconv.Itoa(size))
	}, func(p aiPaneGeometry) int { return p.width })
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
		{name: "one pane", panes: []aiPaneGeometry{{id: "%1", width: 80, height: 24}}},
		{name: "target absent", panes: []aiPaneGeometry{{id: "%2", width: 80, height: 24}}},
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
		{id: "%1", left: 0, top: 0, width: 20, height: 10},
		{id: "%2", left: 21, top: 0, width: 10, height: 10},
		{id: "%3", left: 32, top: 0, width: 10, height: 10},
		{id: "%4", left: 0, top: 11, width: 42, height: 10},
	})
	want := []plannedPaneResize{
		{paneID: "%1", axis: "-x", size: 14},
		{paneID: "%2", axis: "-x", size: 13},
		{paneID: "%3", axis: "-x", size: 13},
	}
	if !reflect.DeepEqual(planned, want) {
		t.Fatalf("resize plan = %v, want %v", planned, want)
	}
}
