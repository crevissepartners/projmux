package runtime

import (
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// LifecycleWindowUnlinkedWindow resolves the exact `$N/@N` window-unlinked hook
// handles to the one Registry Window whose last exact binding carries both and
// whose owner resolves one managed root session. No match, or more than one,
// resolves nothing. The handles alone are only a locator: a caller that acts on
// the Window must still prove it and its Panes absent from the hook server.
func LifecycleWindowUnlinkedWindow(registry coremetadata.Registry, event LifecycleProjectionEvent) (*coremetadata.Window, bool) {
	windowID, sessionID := strings.TrimSpace(event.RuntimeWindowID), strings.TrimSpace(event.RuntimeSessionID)
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
			LifecycleRootSessionName(registry, *root) == "" {
			continue
		}
		if window != nil {
			return nil, false
		}
		window = candidate
	}
	return window, window != nil
}
func LifecycleRootSessionName(registry coremetadata.Registry, root coremetadata.OwnerRef) string {
	switch root.Kind {
	case coremetadata.KindProject:
		project, ok := registry.Project(root.UID)
		if ok && project.Status.Session != nil {
			return strings.TrimSpace(project.Status.Session.Name)
		}
	case coremetadata.KindControlSession:
		control, ok := registry.ControlSession(root.UID)
		if ok {
			return strings.TrimSpace(control.Spec.Session)
		}
	}
	return ""
}
