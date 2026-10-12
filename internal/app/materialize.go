package app

import (
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// runtimeLedger records the tmux objects one operation created so a later
// failure can undo exactly them.
//
// Rollback is ownership checked, not best effort: an entry is only removed when
// the tmux object still carries the same Projmux uid this operation mirrored
// onto it. A pre-existing session, a window another operation created, and an
// id that tmux has since recycled are all left alone.
type RuntimeLedger = mutationruntime.RuntimeLedger

type RuntimeOwner = mutationruntime.RuntimeOwner

// AdmitSplitAnchor limits the invocation target to the anchor being split.
// A process declaration has no tmux handle; an exact tmux sibling keeps its
// existing transport guard and split behavior on the same materializer.
func (t *processTerminalTarget) AdmitSplitAnchor(anchorPaneID string) error {
	if t == nil {
		return nil
	}
	pane, ok := t.registry.Pane(t.paneUID)
	if ok && anchorPaneID != pane.Status.Activation.RuntimeID {
		return nil
	}
	return t.admit(resourcegraph.ProcessSplit)
}
