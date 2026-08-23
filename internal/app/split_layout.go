package app

import "sort"

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

func planEvenSplitResizes(targetPane, direction string, panes []aiPaneGeometry) []plannedPaneResize {
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
		return nil
	}

	peers := splitLayoutPeers(panes, target, direction)
	if len(peers) < 2 {
		return nil
	}
	var planned []plannedPaneResize
	if direction == placementDown {
		resizePanesEvenly(peers, func(p aiPaneGeometry, size int) {
			planned = append(planned, plannedPaneResize{paneID: p.id, axis: "-y", size: size})
		}, func(p aiPaneGeometry) int { return p.height })
		return planned
	}
	resizePanesEvenly(peers, func(p aiPaneGeometry, size int) {
		planned = append(planned, plannedPaneResize{paneID: p.id, axis: "-x", size: size})
	}, func(p aiPaneGeometry) int { return p.width })
	return planned
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
