package runtime

import (
	"slices"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

func ProcessPaneUIDs(registry coremetadata.Registry, inventory resourcegraph.ProcessInventory) map[string]bool {
	out := map[string]bool{}
	for _, pane := range registry.Panes {
		if inventory.Declares(pane) {
			out[pane.Metadata.UID] = true
		}
	}
	return out
}

// ProcessDeclarations supplies convergence with durable declarations only.
// Host liveness is irrelevant to topology and must never be probed under the
// Registry transaction lock. Explicit legacy declarations remain injectable.
func ProcessDeclarations(registry coremetadata.Registry, inventory resourcegraph.ProcessInventory) resourcegraph.ProcessInventory {
	declarations := resourcegraph.ProcessInventory{Declared: inventory.Clone().Declared}
	for _, pane := range registry.Panes {
		if pane.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess || pane.Status.Activation.Process == nil {
			continue
		}
		binding := pane.Status.Activation.Process.Binding
		key := resourcegraph.ProcessKey{Host: binding.HostInstanceID, Pane: pane.Metadata.UID, Generation: pane.Status.Activation.Generation}
		found := slices.Contains(declarations.Declared, key)
		if !found {
			declarations.Declared = append(declarations.Declared, key)
		}
	}
	return declarations
}
