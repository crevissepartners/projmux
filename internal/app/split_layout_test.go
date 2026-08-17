package app

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// geometryRows renders fixture rows with the given field separator so the two
// spellings tmux actually emits are both exercised.
func geometryRows(separator string, rows ...[]string) string {
	var out strings.Builder
	for _, row := range rows {
		out.WriteString(strings.Join(row, separator))
		out.WriteString("\n")
	}
	return out.String()
}

func TestParseSplitPaneGeometrySkipsMalformedRows(t *testing.T) {
	t.Parallel()

	// tmux 3.5a returns the literal four characters `\037`; tmux 3.6 returns
	// the raw 0x1F byte. Both must parse identically, which is the whole
	// reason this format does not use a raw tab.
	for _, separator := range []string{tmuxRowSepFormat, tmuxRowSep} {
		got := parseSplitPaneGeometry("\n" + geometryRows(separator,
			[]string{"%1", "0", "0", "41", "20"},
			[]string{"missing-fields", "0"},
			[]string{"%bad-width", "0", "0", "nope", "20"},
			[]string{"%zero-height", "0", "0", "20", "0"},
			[]string{"%2", "42", "0", "40", "20"},
		))
		want := []aiPaneGeometry{
			{id: "%1", left: 0, top: 0, width: 41, height: 20},
			{id: "%2", left: 42, top: 0, width: 40, height: 20},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("geometry(%q) = %#v, want %#v", separator, got, want)
		}
	}
}

// A raw tab is what tmux 3.5a sanitizes to `_`, which parsed as one field and
// made equalization a silent no-op on that version.
func TestSplitPaneGeometryFormatCarriesNoRawTab(t *testing.T) {
	t.Parallel()

	if strings.Contains(splitPaneGeometryFormat, "\t") {
		t.Fatalf("geometry format carries a raw tab: %q", splitPaneGeometryFormat)
	}
	if got := parseSplitPaneGeometry("%1_0_0_41_20\n%2_42_0_40_20\n"); len(got) != 0 {
		t.Fatalf("sanitized tmux 3.5a output parsed as geometry: %#v", got)
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
		{name: "one pane", out: geometryRows(tmuxRowSepFormat, []string{"%1", "0", "0", "80", "24"})},
		{name: "malformed", out: geometryRows(tmuxRowSepFormat, []string{"%1", "0", "0", "bad", "24"})},
		{name: "target absent", out: geometryRows(tmuxRowSepFormat, []string{"%2", "0", "0", "80", "24"})},
		{name: "tab sanitized by tmux 3.5a", out: "%1_0_0_40_24\n%3_41_0_39_24\n"},
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
			return []byte(geometryRows(tmuxRowSepFormat,
				[]string{"%1", "0", "0", "20", "10"},
				[]string{"%2", "21", "0", "10", "10"},
				[]string{"%3", "32", "0", "10", "10"},
				[]string{"%4", "0", "11", "42", "10"},
			)), nil
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
