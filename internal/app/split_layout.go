package app

import (
	"fmt"
	"sort"
	"strings"
)

const splitPaneGeometryFormat = "#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}"

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

// applyEvenSplitLayout observes the anchor Window and equalizes only the peers
// that share the split axis. Observation and resize failures are deliberately
// silent: a successful split must never become a failed create or a rollback
// merely because layout best-effort work was unavailable.
func applyEvenSplitLayout(
	targetPane, direction string,
	read func(args ...string) ([]byte, error),
	run func(args ...string) error,
) {
	targetPane = strings.TrimSpace(targetPane)
	if targetPane == "" || read == nil || run == nil {
		return
	}
	out, err := read("list-panes", "-t", targetPane, "-F", splitPaneGeometryFormat)
	if err != nil {
		return
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
		return
	}

	peers := splitLayoutPeers(panes, target, direction)
	if len(peers) < 2 {
		return
	}
	if direction == placementDown {
		resizePanesEvenly(peers, func(p aiPaneGeometry, size int) {
			_ = run("resize-pane", "-t", p.id, "-y", fmt.Sprintf("%d", size))
		}, func(p aiPaneGeometry) int { return p.height })
		return
	}
	resizePanesEvenly(peers, func(p aiPaneGeometry, size int) {
		_ = run("resize-pane", "-t", p.id, "-x", fmt.Sprintf("%d", size))
	}, func(p aiPaneGeometry) int { return p.width })
}

func parseSplitPaneGeometry(value string) []aiPaneGeometry {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	panes := make([]aiPaneGeometry, 0, len(lines))
	for _, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) != 5 || strings.TrimSpace(fields[0]) == "" {
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
