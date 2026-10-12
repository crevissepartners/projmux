package runtime

import (
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
)

// lifecycleWindowUnlinkedWindow resolves the exact `$N/@N` window-unlinked hook
// handles to the one Registry Window whose last exact binding carries both and
// whose owner resolves one managed root session. No match, or more than one,
// resolves nothing. The handles alone are only a locator: a caller that acts on
// the Window must still prove it and its Panes absent from the hook server.
func lifecycleWindowUnlinkedWindow(registry coremetadata.Registry, event lifecycleDirtyEvent) (*coremetadata.Window, bool) {
	windowID, sessionID := strings.TrimSpace(event.runtimeWindowID), strings.TrimSpace(event.runtimeSessionID)
	if windowID == "" || sessionID == "" {
		return nil, false
	}
	var window *coremetadata.Window
	for i := range registry.Windows {
		candidate := &registry.Windows[i]
		if candidate.Status.RuntimeID != windowID || candidate.Status.RuntimeSessionID != sessionID {
			continue
		}
		root := candidate.Metadata.OwnerRef
		if root == nil || (root.Kind != coremetadata.KindProject && root.Kind != coremetadata.KindControlSession) ||
			lifecycleRootSessionName(registry, *root) == "" {
			continue
		}
		if window != nil {
			return nil, false
		}
		window = candidate
	}
	return window, window != nil
}
