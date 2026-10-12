package runtime

import (
	"fmt"
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// deleteResource routes one uid onto the mutator that owns its cascade.
func DeleteResource(registry *coremetadata.Registry, mutator coremetadata.Mutator, kind coremetadata.Kind, uid string) error {
	switch kind {
	case coremetadata.KindProject:
		return mutator.DeleteProject(registry, uid)
	case coremetadata.KindWindow:
		return mutator.DeleteWindow(registry, uid)
	case coremetadata.KindAgent:
		return mutator.DeleteAgent(registry, uid)
	case coremetadata.KindPane:
		return mutator.DeletePane(registry, uid)
	default:
		return fmt.Errorf("delete: unsupported kind %q", kind)
	}
}

// paneDeleteReplacementShells finds only newly allocated desired shell rows for
// live Windows whose complete target Pane set would otherwise end the Window.
// One stable target in that Window is used as the pre-kill split anchor.
func PaneDeleteReplacementShells(before, desired coremetadata.Registry, live PaneLiveDeletePlan) []PaneReplacementShell {
	beforeUIDs := make(map[string]bool, len(before.Panes))
	for _, pane := range before.Panes {
		beforeUIDs[pane.Metadata.UID] = true
	}
	anchorByWindow := map[string]PaneLiveDeleteTarget{}
	for _, target := range live.Targets {
		if !target.EndsWindow {
			continue
		}
		if _, exists := anchorByWindow[target.WindowUID]; !exists {
			anchorByWindow[target.WindowUID] = target
		}
	}
	var replacements []PaneReplacementShell
	for _, pane := range desired.Panes {
		if beforeUIDs[pane.Metadata.UID] || pane.Metadata.OwnerRef == nil ||
			pane.Metadata.OwnerRef.Kind != coremetadata.KindWindow || pane.Spec.Role != coremetadata.PaneRoleShell {
			continue
		}
		anchor, ok := anchorByWindow[pane.Metadata.OwnerRef.UID]
		if !ok {
			continue
		}
		replacements = append(replacements, PaneReplacementShell{Pane: pane.Clone(), Anchor: anchor})
	}
	return replacements
}

func RetainedPaneReplacementDrift(receipt PaneReplacementReceipt) string {
	if len(receipt.Created) == 0 {
		return ""
	}
	retained := make([]string, 0, len(receipt.Created))
	for _, created := range receipt.Created {
		retained = append(retained, fmt.Sprintf("%s/pane-uid=%s", created.ID, created.UID))
	}
	slices.Sort(retained)
	return "; retained replacement runtime Pane(s) " + strings.Join(retained, ",") + " were not rolled back after exact target removal"
}
