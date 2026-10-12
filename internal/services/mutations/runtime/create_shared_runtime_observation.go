package runtime

import (
	"context"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// LiveRuntimeInventory is the mirrored-uid inventory seam of the observation.
// It is the read half of the existing tmux identity mirror; nothing here
// writes, re-mirrors, or adopts a uid onto a live tmux object.
type LiveRuntimeInventory interface {
	LiveWindowUIDs(ctx context.Context) (map[string]bool, error)
	LivePaneUIDs(ctx context.Context) (map[string]bool, error)
}

// ObserveRuntime takes one live-tmux observation.
//
// The two reads are independent: a failure of one leaves that half empty rather
// than discarding the other, because a Pane whose tmux pane is provably gone is
// still offline even if the window inventory could not be read.
func ObserveRuntime(ctx context.Context, inventory LiveRuntimeInventory) coremetadata.RuntimeObservation {
	if inventory == nil {
		return coremetadata.RuntimeObservation{}
	}
	windows, err := inventory.LiveWindowUIDs(ctx)
	if err != nil {
		windows = nil
	}
	panes, err := inventory.LivePaneUIDs(ctx)
	if err != nil {
		panes = nil
	}
	return coremetadata.RuntimeObservation{Windows: windows, Panes: panes}
}
