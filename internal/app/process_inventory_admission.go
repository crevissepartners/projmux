package app

import (
	"context"
	"fmt"

	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// processPaneRuntime admits exact process declarations. Production observes
// the invocation's Registry snapshot; explicit typed targets remain injectable.
// Missing host evidence never grants a tmux fallback or process control.
type processPaneRuntime struct {
	targets         []processhost.InventoryTarget
	observe         func(context.Context, coremetadata.Registry) resourcegraph.ProcessInventory
	controlOverride func(context.Context, coremetadata.Registry, string, resourcegraph.ProcessAction, string, string) error
}

func newProcessPaneRuntime() *processPaneRuntime {
	return &processPaneRuntime{observe: observeRegistryProcesses}
}

func (p *processPaneRuntime) inventoryFor(ctx context.Context, registry coremetadata.Registry) resourcegraph.ProcessInventory {
	if p != nil && p.observe != nil && len(p.targets) == 0 {
		return p.observe(ctx, registry)
	}
	return p.inventory()
}

// processTerminalTarget is an invocation-scoped exact process target. A nil
// target keeps the terminal consumer's existing tmux behavior.
type processTerminalTarget struct {
	runtime  *processPaneRuntime
	registry coremetadata.Registry
	paneUID  string
}

func (t *processTerminalTarget) admit(action resourcegraph.ProcessAction) error {
	if t == nil {
		return nil
	}
	_, handled, err := t.runtime.admit(t.registry, t.paneUID, action)
	if !handled {
		return fmt.Errorf("process terminal target is not declared for the current Pane generation")
	}
	return err
}

func (t *processTerminalTarget) admitTmux(name string, args []string) error {
	if t == nil || name != "tmux" {
		return nil
	}
	for len(args) >= 2 && (args[0] == "-L" || args[0] == "-S" || args[0] == "-f") {
		args = args[2:]
	}
	if len(args) == 0 {
		return nil
	}
	switch args[0] {
	case "send-keys":
		return t.admit(resourcegraph.ProcessKeys)
	case "capture-pane":
		return t.admit(resourcegraph.ProcessCapture)
	case "display-popup":
		return t.admit(resourcegraph.ProcessPopup)
	case "attach-session":
		return t.admit(resourcegraph.ProcessAttach)
	}
	return nil
}

func (p *processPaneRuntime) inventory() resourcegraph.ProcessInventory {
	if p == nil {
		return resourcegraph.ProcessInventory{}
	}
	return processhost.ObserveInventory(p.targets)
}

var processPaneUIDs = mutationruntime.ProcessPaneUIDs
var processDeclarations = mutationruntime.ProcessDeclarations

func (p *processPaneRuntime) admit(registry coremetadata.Registry, paneUID string, action resourcegraph.ProcessAction) (resourcegraph.ProcessKey, bool, error) {
	pane, ok := registry.Pane(paneUID)
	if !ok {
		return resourcegraph.ProcessKey{}, false, nil
	}
	inventory := p.inventory()
	if !inventory.Declares(*pane) {
		return resourcegraph.ProcessKey{}, false, nil
	}
	switch action {
	case resourcegraph.ProcessTurn, resourcegraph.ProcessInterrupt, resourcegraph.ProcessStop:
		if pane.Spec.Runtime.EffectiveKind() == coremetadata.RuntimeProcess {
			if pane.Status.Activation.Process == nil {
				return resourcegraph.ProcessKey{}, true, fmt.Errorf("process-host-unavailable: current process activation is absent")
			}
			if _, _, current := registry.CurrentProcessActivation(pane.Status.Activation.Process.Binding); !current {
				return resourcegraph.ProcessKey{}, true, fmt.Errorf("process-host-unavailable: current process ownership is stale")
			}
		}
		inventory = p.inventoryFor(context.Background(), registry)
	}

	key, err := inventory.AdmitProcess(*pane, action)
	return key, true, err
}
