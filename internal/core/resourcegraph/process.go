package resourcegraph

import (
	"fmt"
	"slices"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// ProcessKey identifies an owned child without inventing a tmux handle.
// Declarations are invocation inputs until durable runtime activation is enabled.
type ProcessKey struct{ Host, Pane, Generation string }

func (k ProcessKey) Valid() bool { return k.Host != "" && k.Pane != "" && k.Generation != "" }

// ProcessObservation is an exact host observation. Offline requires actual
// child exit evidence from the adapter; a failed host read is always unknown.
type ProcessObservation struct {
	Key    ProcessKey
	Status Status
}

// ProcessInventory keeps declarations separate from observations so an
// unavailable host cannot erase its children. It contains no provider content.
type ProcessInventory struct {
	Declared []ProcessKey
	Observed []ProcessObservation
}

func (p ProcessInventory) Clone() ProcessInventory {
	return ProcessInventory{Declared: slices.Clone(p.Declared), Observed: slices.Clone(p.Observed)}
}

// Declares checks current generation only; ambiguous hosts still protect the
// child from tmux convergence but authorize no process control.
func (p ProcessInventory) Declares(pane coremetadata.Pane) bool {
	if pane.Spec.Runtime.EffectiveKind() == coremetadata.RuntimeProcess {
		return true
	}
	return slices.ContainsFunc(p.Declared, func(k ProcessKey) bool {
		return k.Valid() && k.Pane == pane.Metadata.UID && k.Generation == pane.Status.Activation.Generation
	})
}

func (p ProcessInventory) current(pane coremetadata.Pane) (*ProcessKey, Status) {
	var key *ProcessKey
	for _, k := range p.Declared {
		if !k.Valid() || k.Pane != pane.Metadata.UID || k.Generation != pane.Status.Activation.Generation {
			continue
		}
		if key != nil {
			return &ProcessKey{Pane: pane.Metadata.UID, Generation: pane.Status.Activation.Generation}, StatusUnknown
		}
		copy := k
		key = &copy
	}
	if key == nil {
		if pane.Spec.Runtime.EffectiveKind() == coremetadata.RuntimeProcess {
			return &ProcessKey{Pane: pane.Metadata.UID, Generation: pane.Status.Activation.Generation}, StatusUnknown
		}
		return nil, StatusUnknown
	}
	if pane.Status.Activation.RuntimeID != "" {
		return key, StatusUnknown
	}
	status := StatusUnknown
	found := false
	for _, observation := range p.Observed {
		if observation.Key != *key {
			continue
		}
		if found {
			return key, StatusUnknown
		}
		found = true
		if observation.Status == StatusLive || observation.Status == StatusOffline {
			status = observation.Status
		}
	}
	return key, status
}

// ProcessAction is the capability boundary for Pane-only operations.
type ProcessAction string

const (
	ProcessAttach     ProcessAction = "attach"
	ProcessFocus      ProcessAction = "focus"
	ProcessKeys       ProcessAction = "send-keys"
	ProcessCapture    ProcessAction = "capture"
	ProcessPopup      ProcessAction = "popup"
	ProcessRelaunch   ProcessAction = "relaunch"
	ProcessCreatePane ProcessAction = "create-pane"
	ProcessSplit      ProcessAction = "split"
	ProcessTurn       ProcessAction = "turn"
	ProcessInterrupt  ProcessAction = "interrupt"
	ProcessStop       ProcessAction = "stop"
)

// ProcessCapabilityError identifies an unsupported process action before
// transports or mutations run. Its stable token is also the CLI error prefix.
type ProcessCapabilityError struct {
	Action ProcessAction
	Anchor bool
}

func (e ProcessCapabilityError) Error() string {
	if e.Anchor {
		return fmt.Sprintf("process-%s-unsupported: %s has no tmux target for this process Pane; use a tmux Pane or Window anchor in the same Window", e.Action, e.Action)
	}
	return fmt.Sprintf("process-%s-unsupported: %s is unavailable for this process Pane; use host turn, interrupt, or Stop", e.Action, e.Action)
}

// AdmitProcess rejects terminal operations before any transport or Registry
// write. Supported controls still require an exact current live host.
func (p ProcessInventory) AdmitProcess(pane coremetadata.Pane, action ProcessAction) (ProcessKey, error) {
	key, status := p.current(pane)
	if key == nil {
		return ProcessKey{}, fmt.Errorf("process target is not declared for this Pane generation")
	}
	switch action {
	case ProcessAttach, ProcessFocus, ProcessKeys, ProcessCapture, ProcessPopup, ProcessRelaunch:
		return ProcessKey{}, ProcessCapabilityError{Action: action}
	case ProcessCreatePane, ProcessSplit:
		return ProcessKey{}, ProcessCapabilityError{Action: action, Anchor: true}
	case ProcessTurn, ProcessInterrupt, ProcessStop:
		if status == StatusLive {
			return *key, nil
		}
		return ProcessKey{}, fmt.Errorf("process-host-unavailable: reobserve the exact process host before %s", action)
	default:
		return ProcessKey{}, fmt.Errorf("process-capability-unsupported: %s", action)
	}
}

func (r *resolver) projectProcesses() {
	for index := range r.panes {
		node := &r.panes[index]
		key, status := r.inventory.Processes.current(node.Pane)
		if key == nil {
			continue
		}
		node.Process, node.Status, node.Runtime = key, status, nil
		if node.Class == ClassConflict {
			node.Status = StatusUnknown
		}
	}
	for index := range r.agents {
		node := &r.agents[index]
		for _, pane := range r.panes {
			if pane.Pane.Metadata.UID == node.PaneUID && pane.AgentUID == node.Agent.Metadata.UID && pane.Process != nil {
				copy := *pane.Process
				node.Process, node.Status, node.Runtime = &copy, pane.Status, nil
				node.Class = pane.Class
			}
		}
	}
}
