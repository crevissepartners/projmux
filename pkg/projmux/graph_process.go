package projmux

import (
	"fmt"

	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// GraphProcessDeclares reports whether processes declares the process child of
// pane for the Pane's activation generation, the same judgement the internal
// process inventory makes. A Pane stored with the process runtime is always
// declared, even when processes is empty. A duplicate declaration still counts
// as declared but admits no control; see AdmitGraphProcess. A Pane that cannot
// be read as a stored Pane is not declared. It reads and writes nothing and
// changes neither argument.
func GraphProcessDeclares(processes core.GraphProcessInventory, pane core.Pane) bool {
	internal, err := internalGraphProcessPane(pane)
	if err != nil {
		return false
	}
	return internalProcessInventory(processes).Declares(internal)
}

// AdmitGraphProcess judges whether action may run on the process child of pane
// before any transport or write. It returns the declared key only for turn,
// interrupt and stop, and only when the internal process inventory finds the
// child exactly live. Every other outcome is an error: no declaration for the
// Pane generation, an action a process Pane does not support, an unknown
// action, or a host that is not exactly live. The error text equals the text of
// the internal admission error for the same input, such as a
// process-<action>-unsupported token, process-host-unavailable,
// process-capability-unsupported or "process target is not declared for this
// Pane generation"; only that text is promised, not an error type. A Pane that
// cannot be read as a stored Pane is an error whose text is not promised.
func AdmitGraphProcess(processes core.GraphProcessInventory, pane core.Pane, action core.GraphProcessAction) (core.GraphProcessKey, error) {
	internal, err := internalGraphProcessPane(pane)
	if err != nil {
		return core.GraphProcessKey{}, fmt.Errorf("process Pane is not readable: %w", err)
	}
	key, err := internalProcessInventory(processes).AdmitProcess(internal, resourcegraph.ProcessAction(action))
	if err != nil {
		return core.GraphProcessKey{}, err
	}
	return core.GraphProcessKey{Host: key.Host, Pane: key.Pane, Generation: key.Generation}, nil
}

func internalGraphProcessPane(pane core.Pane) (metadata.Pane, error) {
	var out metadata.Pane
	err := unregisterCopy(pane, &out)
	return out, err
}
