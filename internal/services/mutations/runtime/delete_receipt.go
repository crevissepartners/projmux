package runtime

import (
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/receipt"
)

func ChildDeleteReceipt(
	kind coremetadata.Kind,
	plan DeletePlan,
	windows WindowLiveDeletePlan,
	panes PaneLiveDeletePlan,
	selfTarget bool,
) receipt.OperationReceipt {
	operation := receipt.OperationDeleteWindow
	switch kind {
	case coremetadata.KindPane:
		operation = receipt.OperationDeletePane
	case coremetadata.KindAgent:
		operation = receipt.OperationDeleteAgent
	}
	runtime := receipt.RuntimeUnchanged
	if len(windows.Targets) > 0 || len(panes.Targets) > 0 {
		runtime = receipt.RuntimeStopped
	}
	focus := receipt.FocusUnchanged
	if selfTarget {
		focus = receipt.FocusMovedCurrentClient
	}
	target := receipt.ReceiptTarget{Kind: string(kind)}
	if len(plan.Targets) > 0 {
		target.UID, target.Name = plan.Targets[0].Match.UID, plan.Targets[0].Match.Name
	}
	r := receipt.NewReceipt(operation, target, receipt.ReceiptEffects{
		Identity:     receipt.IdentityRemoved,
		Address:      receipt.AddressReleased,
		Topology:     receipt.TopologyRemoved,
		DesiredState: receipt.DesiredStateRemoved,
		Runtime:      runtime,
		Focus:        focus,
	})
	for _, entry := range plan.Targets {
		r.Add(string(kind), entry.Match.UID, entry.Match.Name, receipt.ActionRemoved)
		for _, descendant := range entry.Descendants {
			r.Add(string(descendant.Kind), descendant.UID, descendant.Name, receipt.ActionRemoved)
		}
	}
	return r
}
