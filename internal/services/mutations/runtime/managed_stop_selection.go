package runtime

import (
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"path/filepath"
	"strings"
)

func cleanStopPath(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return filepath.Clean(s)
}
func ResolveManagedProjectStopTarget(view registryview.View, selection, sessionName string) (ManagedRuntimeStopTarget, bool) {
	selectionUID := ""
	if after, ok := strings.CutPrefix(strings.TrimSpace(selection), "uid:"); ok {
		selectionUID = strings.TrimSpace(after)
	}
	for _, row := range view.Section(registryview.SectionProjects) {
		if row.Kind != registryview.RowKindProject {
			continue
		}
		matches := row.UID == selectionUID ||
			(row.Root != "" && cleanStopPath(row.Root) == cleanStopPath(selection))
		if !matches || row.Runtime == nil || ExactTmuxHandle(row.Runtime.ID, "$") == "" || strings.TrimSpace(row.UID) == "" {
			continue
		}
		observedName := strings.TrimSpace(row.SessionName)
		if observedName == "" {
			observedName = strings.TrimSpace(row.Runtime.Name)
		}
		if observedName != strings.TrimSpace(sessionName) {
			return ManagedRuntimeStopTarget{}, false
		}
		return ManagedRuntimeStopTarget{SessionID: row.Runtime.ID, SessionName: observedName,
			RootKind: coremetadata.KindProject, RootUID: row.UID}, true
	}
	return ManagedRuntimeStopTarget{}, false
}
