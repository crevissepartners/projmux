package runtime

import "sort"

// AiPaneGeometry is the shared geometry record used by legacy AI splits and
// canonical resource creates. Keeping the established name avoids changing the
// legacy test seam while moving the algorithm out of the AI route.
type AiPaneGeometry struct {
	Id     string
	Left   int
	Top    int
	Width  int
	Height int
}

type PlannedPaneResize struct {
	PaneID string
	Axis   string
	Size   int
}

func PlanEvenSplitResizes(targetPane, direction string, panes []AiPaneGeometry) []PlannedPaneResize {
	var target AiPaneGeometry
	found := false
	for _, pane := range panes {
		if pane.Id == targetPane {
			target = pane
			found = true
			break
		}
	}
	if !found {
		return nil
	}

	peers := SplitLayoutPeers(panes, target, direction)
	if len(peers) < 2 {
		return nil
	}
	var planned []PlannedPaneResize
	if direction == PlacementDown {
		ResizePanesEvenly(peers, func(p AiPaneGeometry, size int) {
			planned = append(planned, PlannedPaneResize{PaneID: p.Id, Axis: "-y", Size: size})
		}, func(p AiPaneGeometry) int { return p.Height })
		return planned
	}
	ResizePanesEvenly(peers, func(p AiPaneGeometry, size int) {
		planned = append(planned, PlannedPaneResize{PaneID: p.Id, Axis: "-x", Size: size})
	}, func(p AiPaneGeometry) int { return p.Width })
	return planned
}

func SplitLayoutPeers(panes []AiPaneGeometry, target AiPaneGeometry, direction string) []AiPaneGeometry {
	peers := make([]AiPaneGeometry, 0, len(panes))
	for _, pane := range panes {
		if direction == PlacementDown {
			if pane.Left == target.Left && pane.Width == target.Width {
				peers = append(peers, pane)
			}
			continue
		}
		if pane.Top == target.Top && pane.Height == target.Height {
			peers = append(peers, pane)
		}
	}
	if direction == PlacementDown {
		sort.Slice(peers, func(i, j int) bool { return peers[i].Top < peers[j].Top })
		return peers
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].Left < peers[j].Left })
	return peers
}

func ResizePanesEvenly(peers []AiPaneGeometry, resize func(AiPaneGeometry, int), currentSize func(AiPaneGeometry) int) {
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
