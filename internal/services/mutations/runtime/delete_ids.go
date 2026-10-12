package runtime

import (
	"fmt"
	"strings"
)

func PaneDeleteIDs(targets []PaneLiveDeleteTarget) string {
	if len(targets) == 0 {
		return "<none>"
	}
	ids := make([]string, 0, len(targets))
	for _, target := range targets {
		ids = append(ids, fmt.Sprintf("%s/pane-uid=%s", target.PaneID, target.PaneUID))
	}
	return strings.Join(ids, ",")
}
