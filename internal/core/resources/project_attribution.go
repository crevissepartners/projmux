package resources

import (
	"strings"

	"github.com/crevissepartners/projmux/internal/core/candidates"
)

// ResolveProjectAnchors returns a detached inventory whose blank explicit
// anchors are filled from pane current paths and known project roots. It is
// intentionally I/O-free and never mutates tmux metadata or the input slice.
// Explicit anchors are authoritative for each linked-session view.
func ResolveProjectAnchors(inventory []PaneInventory, projectRoots []string) []PaneInventory {
	resolved := make([]PaneInventory, len(inventory))
	copy(resolved, inventory)
	for i := range resolved {
		if strings.TrimSpace(resolved[i].ProjectAnchor) != "" {
			continue
		}
		resolved[i].ProjectAnchor = candidates.MostSpecificProjectRoot(resolved[i].CurrentPath, projectRoots)
	}
	return resolved
}
