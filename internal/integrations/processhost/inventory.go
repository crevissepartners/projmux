package processhost

import "github.com/crevissepartners/projmux/internal/core/resourcegraph"

// InventoryTarget retains an exact declaration even when its host is unavailable.
// Handle is owned by the caller; observation never discovers or starts a host.
type InventoryTarget struct {
	Binding Binding
	Handle  *Handle
}

func ObserveInventory(targets []InventoryTarget) resourcegraph.ProcessInventory {
	var inventory resourcegraph.ProcessInventory
	for _, target := range targets {
		binding := target.Binding
		key := resourcegraph.ProcessKey{Host: binding.Host, Pane: binding.Pane, Generation: binding.Generation}
		inventory.Declared = append(inventory.Declared, key)
		status := resourcegraph.StatusUnknown
		if target.Handle != nil {
			if snapshot, err := target.Handle.Observe(binding); err == nil {
				if snapshot.Exit != nil {
					status = resourcegraph.StatusOffline
				} else if snapshot.State == "ready" {
					status = resourcegraph.StatusLive
				}
			}
		}
		inventory.Observed = append(inventory.Observed, resourcegraph.ProcessObservation{Key: key, Status: status})
	}
	return inventory
}
