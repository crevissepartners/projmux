package app

import (
	"errors"
	"reflect"
	"strconv"
	"testing"
)

func TestParseSplitPaneGeometrySkipsMalformedRows(t *testing.T) {
	t.Parallel()

	got := parseSplitPaneGeometry("\n" +
		"%1\t0\t0\t41\t20\n" +
		"missing-fields\t0\n" +
		"%bad-width\t0\t0\tnope\t20\n" +
		"%zero-height\t0\t0\t20\t0\n" +
		"%2\t42\t0\t40\t20\n")
	want := []aiPaneGeometry{
		{id: "%1", left: 0, top: 0, width: 41, height: 20},
		{id: "%2", left: 42, top: 0, width: 40, height: 20},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("geometry = %#v, want %#v", got, want)
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

func TestApplyEvenSplitLayoutNoOpsOnUnreadableOrInvalidGeometry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		out  string
		err  error
	}{
		{name: "unreadable", err: errors.New("no server")},
		{name: "one pane", out: "%1\t0\t0\t80\t24\n"},
		{name: "malformed", out: "%1\t0\t0\tbad\t24\n"},
		{name: "target absent", out: "%2\t0\t0\t80\t24\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var resizeCalls int
			applyEvenSplitLayout("%1", "right",
				func(args ...string) ([]byte, error) { return []byte(test.out), test.err },
				func(args ...string) error { resizeCalls++; return nil })
			if resizeCalls != 0 {
				t.Fatalf("resize calls = %d, want 0", resizeCalls)
			}
		})
	}
}

func TestApplyEvenSplitLayoutIgnoresResizeFailuresAndUnrelatedTopology(t *testing.T) {
	t.Parallel()

	var readArgs []string
	var resizeArgs [][]string
	applyEvenSplitLayout("%2", "right",
		func(args ...string) ([]byte, error) {
			readArgs = append([]string(nil), args...)
			return []byte("%1\t0\t0\t20\t10\n%2\t21\t0\t10\t10\n%3\t32\t0\t10\t10\n%4\t0\t11\t42\t10\n"), nil
		},
		func(args ...string) error {
			resizeArgs = append(resizeArgs, append([]string(nil), args...))
			return errors.New("best effort")
		})
	if want := []string{"list-panes", "-t", "%2", "-F", splitPaneGeometryFormat}; !reflect.DeepEqual(readArgs, want) {
		t.Fatalf("read args = %v, want %v", readArgs, want)
	}
	want := [][]string{
		{"resize-pane", "-t", "%1", "-x", "14"},
		{"resize-pane", "-t", "%2", "-x", "13"},
		{"resize-pane", "-t", "%3", "-x", "13"},
	}
	if !reflect.DeepEqual(resizeArgs, want) {
		t.Fatalf("resize args = %v, want %v", resizeArgs, want)
	}
}
