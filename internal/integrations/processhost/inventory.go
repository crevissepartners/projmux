package processhost

import "github.com/crevissepartners/projmux/internal/core/resourcegraph"

// InventoryTarget retains an exact declaration even when its host is unavailable.
// Handles and observers belong to the caller; observation never discovers or
// starts a host.
type InventoryTarget struct {
	Binding Binding
	Handle  *Handle
	// Observer also admits dedicated Codex handles and bounded remote reads.
	// It has no process-control or runtime-discovery authority.
	Observer interface {
		Observe(Binding) (Snapshot, error)
	}
}

func ObserveInventory(targets []InventoryTarget) resourcegraph.ProcessInventory {
	var inventory resourcegraph.ProcessInventory
	for _, target := range targets {
		binding := target.Binding
		key := resourcegraph.ProcessKey{Host: binding.Host, Pane: binding.Pane, Generation: binding.Generation}
		inventory.Declared = append(inventory.Declared, key)
		status := resourcegraph.StatusUnknown
		observer := target.Observer
		if observer == nil && target.Handle != nil {
			observer = target.Handle
		}
		if observer != nil {
			if snapshot, err := observer.Observe(binding); err == nil && snapshot.Binding == binding {
				if snapshot.Exit != nil && snapshot.State == "exited" {
					status = resourcegraph.StatusOffline
				} else if snapshot.Exit == nil && snapshot.State == "ready" {
					status = resourcegraph.StatusLive
				}
			}
		}
		inventory.Observed = append(inventory.Observed, resourcegraph.ProcessObservation{Key: key, Status: status})
	}
	return inventory
}
