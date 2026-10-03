package app

import (
	"context"
	"fmt"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// processPaneRuntime is the internal invocation seam. Its declarations survive
// unavailable handles; nil preserves every existing tmux path. It is not a
// discovery service or durable runtime schema.
type processPaneRuntime struct{ targets []processhost.InventoryTarget }

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

func processPaneUIDs(registry coremetadata.Registry, inventory resourcegraph.ProcessInventory) map[string]bool {
	out := map[string]bool{}
	for _, pane := range registry.Panes {
		if inventory.Declares(pane) {
			out[pane.Metadata.UID] = true
		}
	}
	return out
}

func (p *processPaneRuntime) admit(registry coremetadata.Registry, paneUID string, action resourcegraph.ProcessAction) (resourcegraph.ProcessKey, bool, error) {
	pane, ok := registry.Pane(paneUID)
	inventory := p.inventory()
	if !ok || !inventory.Declares(*pane) {
		return resourcegraph.ProcessKey{}, false, nil
	}
	key, err := inventory.AdmitProcess(*pane, action)
	return key, true, err
}

// control carries a verified typed target all the way to its owned Handle.
// Host methods revalidate current ownership and never fall back to tmux.
func (p *processPaneRuntime) control(ctx context.Context, registry coremetadata.Registry, paneUID string, action resourcegraph.ProcessAction, turn, prompt string) error {
	key, handled, err := p.admit(registry, paneUID, action)
	if err != nil {
		return err
	}
	if !handled {
		return fmt.Errorf("process target is not declared")
	}
	for _, target := range p.targets {
		binding := target.Binding
		if key != (resourcegraph.ProcessKey{Host: binding.Host, Pane: binding.Pane, Generation: binding.Generation}) || target.Handle == nil {
			continue
		}
		pane, _ := registry.Pane(paneUID)
		agent, ok := registry.Agent(binding.Agent)
		window, windowOK := registry.Window(binding.Window)
		if !ok || !windowOK || window.Metadata.OwnerUID() != binding.Project || pane.Spec.Role != coremetadata.PaneRoleAgent || pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent || pane.Metadata.OwnerUID() != binding.Agent || pane.Status.Activation.AgentUID != binding.Agent || agent.Status.PaneRef != paneUID || agent.Metadata.OwnerUID() != binding.Window {
			return processhost.ErrStale
		}
		snapshot, observeErr := target.Handle.Observe(binding)
		if observeErr != nil {
			return observeErr
		}
		authority := processhost.Authority{Binding: binding, Connection: snapshot.Connection, Session: snapshot.Session}
		switch action {
		case resourcegraph.ProcessTurn:
			return target.Handle.Turn(ctx, authority, turn, prompt)
		case resourcegraph.ProcessInterrupt:
			return target.Handle.Interrupt(ctx, authority, turn)
		case resourcegraph.ProcessStop:
			if err := target.Handle.ValidateAuthority(ctx, authority); err != nil {
				return err
			}
			return target.Handle.Stop(binding)
		}
	}
	return processhost.ErrStale
}
