package app

import (
	"sort"
	"strings"
)

// splitPaneGeometryFormat joins its fields with the materializer's row
// separator rather than a raw tab. A raw tab is not a usable separator here:
// tmux 3.5a sanitizes it to `_` in list output while 3.6 emits it verbatim, so
// a tab-separated format parses as a single field on 3.5a and silently
// equalizes nothing. The escaped `\037` spelling comes back identically on both
// and splitTmuxRows folds it to the raw byte.
var splitPaneGeometryFormat = tmuxRowFormat(
	"#{pane_id}", "#{pane_left}", "#{pane_top}", "#{pane_width}", "#{pane_height}",
)

// aiPaneGeometry is the shared geometry record used by legacy AI splits and
// canonical resource creates. Keeping the established name avoids changing the
// legacy test seam while moving the algorithm out of the AI route.
type aiPaneGeometry struct {
	id     string
	left   int
	top    int
	width  int
	height int
}

type plannedPaneResize struct {
	paneID string
	axis   string
	size   int
}

// planEvenSplitLayout observes the anchor Window and returns only typed resize
// operands for peers on the split axis. It never executes tmux: the materializer
// adds stable targets and guards, then sends the complete ordered set through
// the shared mutation executor.
func planEvenSplitLayout(
	targetPane, direction string,
	read func(args ...string) ([]byte, error),
) (string, []plannedPaneResize) {
	targetPane = strings.TrimSpace(targetPane)
	if targetPane == "" || read == nil {
		return "", nil
	}
	out, err := read("list-panes", "-t", targetPane, "-F", splitPaneGeometryFormat)
	if err != nil {
		return "", nil
	}
	panes := parseSplitPaneGeometry(string(out))
	var target aiPaneGeometry
	found := false
	for _, pane := range panes {
		if pane.id == targetPane {
			target = pane
			found = true
			break
		}
	}
	if !found {
		return "", nil
	}

	peers := splitLayoutPeers(panes, target, direction)
	if len(peers) < 2 {
		return "", nil
	}
	var planned []plannedPaneResize
	if direction == placementDown {
		resizePanesEvenly(peers, func(p aiPaneGeometry, size int) {
			planned = append(planned, plannedPaneResize{paneID: p.id, axis: "-y", size: size})
		}, func(p aiPaneGeometry) int { return p.height })
		return strings.TrimSpace(string(out)), planned
	}
	resizePanesEvenly(peers, func(p aiPaneGeometry, size int) {
		planned = append(planned, plannedPaneResize{paneID: p.id, axis: "-x", size: size})
	}, func(p aiPaneGeometry) int { return p.width })
	return strings.TrimSpace(string(out)), planned
}

func parseSplitPaneGeometry(value string) []aiPaneGeometry {
	rows := splitTmuxRows(value, 5)
	panes := make([]aiPaneGeometry, 0, len(rows))
	for _, fields := range rows {
		if strings.TrimSpace(fields[0]) == "" {
			continue
		}
		pane := aiPaneGeometry{
			id:     strings.TrimSpace(fields[0]),
			left:   parsePositiveInt(fields[1]),
			top:    parsePositiveInt(fields[2]),
			width:  parsePositiveInt(fields[3]),
			height: parsePositiveInt(fields[4]),
		}
		if pane.width <= 0 || pane.height <= 0 {
			continue
		}
		panes = append(panes, pane)
	}
	return panes
}

func splitLayoutPeers(panes []aiPaneGeometry, target aiPaneGeometry, direction string) []aiPaneGeometry {
	peers := make([]aiPaneGeometry, 0, len(panes))
	for _, pane := range panes {
		if direction == placementDown {
			if pane.left == target.left && pane.width == target.width {
				peers = append(peers, pane)
			}
			continue
		}
		if pane.top == target.top && pane.height == target.height {
			peers = append(peers, pane)
		}
	}
	if direction == placementDown {
		sort.Slice(peers, func(i, j int) bool { return peers[i].top < peers[j].top })
		return peers
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].left < peers[j].left })
	return peers
}

func resizePanesEvenly(peers []aiPaneGeometry, resize func(aiPaneGeometry, int), currentSize func(aiPaneGeometry) int) {
	if len(peers) == 0 || resize == nil || currentSize == nil {
		return
	}
	total := 0
	for _, pane := range peers {
		total += currentSize(pane)
	}
	if total <= 0 {
		return
	}
	base := total / len(peers)
	remainder := total % len(peers)
	for index, pane := range peers {
		size := base
		if index < remainder {
			size++
		}
		resize(pane, size)
	}
}
