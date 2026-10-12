package app

import mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"

import (
	"context"
	"errors"
	"fmt"

	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/candidates"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
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

























// admitSplitAnchor limits the invocation target to the anchor being split.
// A process declaration has no tmux handle; an exact tmux sibling keeps its
// existing transport guard and split behavior on the same materializer.
func (t *processTerminalTarget) admitSplitAnchor(anchorPaneID string) error {
	if t == nil {
		return nil
	}
	pane, ok := t.registry.Pane(t.paneUID)
	if ok && anchorPaneID != pane.Status.Activation.RuntimeID {
		return nil
	}
	return t.admit(resourcegraph.ProcessSplit)
}




















