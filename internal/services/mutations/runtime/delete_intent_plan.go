package runtime

import (
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// intentionalTerminationPanes lists every Pane whose managed process one delete
// is about to end, in plan order and without duplicates.
//
// The set is derived from the *live* half, not from the resource cascade. A
// receipt explains a mutation, so a delete that ends no live process has
// nothing to explain and writes nothing: removing an offline Pane resource is
// already fully described by the transaction that removed it.
//
// A Window delete reaches its Panes through the resource cascade of the exact
// Windows the live plan will kill, because killing a Window ends every process
// inside it.
func IntentionalTerminationPanes(plan DeletePlan, live WindowLiveDeletePlan, panes PaneLiveDeletePlan) []string {
	var out []string
	seen := map[string]bool{}
	add := func(uid string) {
		uid = strings.TrimSpace(uid)
		if uid == "" || seen[uid] {
			return
		}
		seen[uid] = true
		out = append(out, uid)
	}
	for _, target := range panes.Targets {
		add(target.PaneUID)
	}
	killedWindows := map[string]bool{}
	for _, target := range live.Targets {
		killedWindows[target.UID] = true
	}
	if len(killedWindows) == 0 {
		return out
	}
	for _, target := range plan.Targets {
		if plan.Kind != coremetadata.KindWindow || !killedWindows[target.Match.UID] {
			continue
		}
		for _, descendant := range target.Descendants {
			if descendant.Kind == coremetadata.KindPane {
				add(descendant.UID)
			}
		}
	}
	return out
}
