package runtime

import (
	"context"
	"sync"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/services/resources"
)

// liveRuntimeInventory is the mirrored-uid inventory seam of the observation.
// It is the read half of the existing tmux identity mirror; nothing here
// writes, re-mirrors, or adopts a uid onto a live tmux object.
type liveRuntimeInventory interface {
	LiveWindowUIDs(ctx context.Context) (map[string]bool, error)
	LivePaneUIDs(ctx context.Context) (map[string]bool, error)
}

// observeRuntime takes one live-tmux observation.
//
// The two reads are independent: a failure of one leaves that half empty rather
// than discarding the other, because a Pane whose tmux pane is provably gone is
// still offline even if the window inventory could not be read.
func observeRuntime(ctx context.Context, inventory liveRuntimeInventory) coremetadata.RuntimeObservation {
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
