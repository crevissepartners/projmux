package app

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// Observation is a separate read protocol. Provider input, pending requests,
// output, diagnostics and control authority never cross this boundary.
type processHostObservation struct {
	Binding         processhost.Binding
	Host, Child     coremetadata.ProcessIdentity
	Provider, State string
	Exit            *processhost.Exit `json:",omitempty"`
}

func processSchemaBinding(b coremetadata.ProcessBinding) processhost.Binding {
	return processhost.Binding{Host: b.HostInstanceID, Project: b.ProjectUID, Window: b.WindowUID, Agent: b.AgentUID, Pane: b.PaneUID, Generation: b.Generation, Operation: b.OperationID}
}

// Both the responder and reader compare the complete immutable activation.
// An unavailable child is not an exit; only the host's reaped Wait can say so.
func processObservationMatches(reg coremetadata.Registry, binding processhost.Binding, view processHostObservation) bool {
	if !processObservationOwnership(reg, binding, view) {
		return false
	}
	host, _, err := localipc.Process(view.Host.PID)
	if err != nil || host != view.Host {
		return false
	}
	if view.Exit != nil {
		return view.State == "exited"
	}
	child, parent, err := localipc.Process(view.Child.PID)
	if err != nil || child != view.Child {
		return false
	}
	// The provider belongs to the host's dedicated supervisor, not to an
	// unrelated process that happens to have the same UID.
	_, hostPID, err := localipc.Process(parent)
	if err != nil || hostPID != view.Host.PID {
		return false
	}
	switch view.State {
	case "starting", "ready", "stopping", "unknown":
		return true
	default:
		return false
	}
}

func processObservationOwnership(reg coremetadata.Registry, binding processhost.Binding, view processHostObservation) bool {
	pane, ok := reg.Pane(binding.Pane)
	agent, agentOK := reg.Agent(binding.Agent)
	window, windowOK := reg.Window(binding.Window)
	_, projectOK := reg.Project(binding.Project)
	if !ok || !agentOK || !windowOK || !projectOK || view.Binding != binding ||
		pane.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess || pane.Spec.Role != coremetadata.PaneRoleAgent ||
		pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent || pane.Metadata.OwnerUID() != binding.Agent ||
		agent.Metadata.OwnerRef == nil || agent.Metadata.OwnerRef.Kind != coremetadata.KindWindow || agent.Metadata.OwnerUID() != binding.Window || agent.Status.PaneRef != binding.Pane ||
		window.Metadata.OwnerRef == nil || window.Metadata.OwnerRef.Kind != coremetadata.KindProject || window.Metadata.OwnerUID() != binding.Project ||
		agent.Spec.Provider != view.Provider || (view.Provider != "claude" && view.Provider != "codex") {
		return false
	}
	a := pane.Status.Activation
	if a.Kind != coremetadata.RuntimeProcess || a.Process == nil || a.RuntimeID != "" || a.Generation != binding.Generation || a.OperationID != binding.Operation || a.AgentUID != binding.Agent ||
		processSchemaBinding(a.Process.Binding) != binding || a.Process.HostProcess != view.Host || a.Process.Child != view.Child || !view.Host.Valid() || !view.Child.Valid() || int64(view.Host.OwnerUID) != int64(os.Getuid()) || int64(view.Child.OwnerUID) != int64(os.Getuid()) {
		return false
	}
	return true
}

type remoteProcessObserver struct {
	ctx          context.Context
	registry     coremetadata.Registry
	registryPath string
	binding      processhost.Binding
}

func (r remoteProcessObserver) Observe(binding processhost.Binding) (processhost.Snapshot, error) {
	if binding != r.binding || r.ctx.Err() != nil {
		return processhost.Snapshot{}, processhost.ErrStale
	}
	pane, ok := r.registry.Pane(binding.Pane)
	if !ok || pane.Status.Activation.Process == nil {
		return processhost.Snapshot{}, processhost.ErrStale
	}
	activation := pane.Status.Activation.Process
	agent, ok := r.registry.Agent(binding.Agent)
	if !ok {
		return processhost.Snapshot{}, processhost.ErrStale
	}
	if receipt := pane.Status.LastTermination; receipt != nil && receipt.Source == coremetadata.TerminationSourceSupervisor &&
		receipt.PaneUID == binding.Pane && receipt.AgentUID == binding.Agent && receipt.Generation == binding.Generation && receipt.OperationID == binding.Operation &&
		!receipt.ObservedAt.IsZero() && (receipt.ExitCode != nil || receipt.Signal != "") {
		exit := &processhost.Exit{Signal: receipt.Signal}
		if receipt.ExitCode != nil {
			exit.Code = *receipt.ExitCode
		}
		view := processHostObservation{Binding: binding, Host: activation.HostProcess, Child: activation.Child, Provider: agent.Spec.Provider, State: "exited", Exit: exit}
		if receipt.Classification == coremetadata.ClassifyProcessExit(exit.Code, exit.Signal) && processObservationOwnership(r.registry, binding, view) {
			return processhost.Snapshot{Binding: binding, Provider: view.Provider, State: view.State, PID: view.Child.PID, Exit: exit}, nil
		}
	}
	socket := processClaudeHostSocket(r.registryPath, binding.Pane, binding.Generation)
	var request any = claudeProcessCheck{Observe: &binding}
	if agent.Spec.Provider == "codex" {
		socket = filepath.Join(claudeActivationLeaseDir(r.registryPath, binding.Pane, binding.Generation), "codex-host.sock")
		request = codexProcessExchange{Observe: &binding}
	}
	identity, err := localipc.InspectOwnedSocket(socket)
	if err != nil {
		return processhost.Snapshot{}, err
	}
	result, err := callProcessForeground(r.ctx, socket, identity, activation.HostProcess, request)
	if err != nil {
		return processhost.Snapshot{}, err
	}
	current, err := localipc.InspectOwnedSocket(socket)
	if err != nil || current != identity || !result.Accepted || result.Observation == nil || !processObservationMatches(r.registry, binding, *result.Observation) {
		return processhost.Snapshot{}, processhost.ErrStale
	}
	view := result.Observation
	return processhost.Snapshot{Binding: binding, Provider: view.Provider, State: view.State, PID: view.Child.PID, Exit: view.Exit}, nil
}

// A read gives all probes the same short budget; probes run independently
// with bounded concurrency, so an unresponsive host cannot consume five seconds
// or stop another host from being observed. Failures remain unknown.
const processObservationReadLimit = 250 * time.Millisecond

// Declarations are collected before probing. One unavailable host cannot erase
// another Pane or cause a tmux lookup, and the entire observation is bounded.
func observeRegistryProcesses(ctx context.Context, registry coremetadata.Registry) resourcegraph.ProcessInventory {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return resourcegraph.ProcessInventory{}
	}
	ctx, cancel := context.WithTimeout(ctx, processObservationReadLimit)
	defer cancel()
	targets := []processhost.InventoryTarget{}
	for _, pane := range registry.Panes {
		if pane.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess || pane.Status.Activation.Process == nil {
			continue
		}
		binding := processSchemaBinding(pane.Status.Activation.Process.Binding)
		targets = append(targets, processhost.InventoryTarget{Binding: binding, Observer: remoteProcessObserver{ctx: ctx, registry: registry, registryPath: intmetadata.PathFor(paths.StateDir), binding: binding}})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Binding.Pane < targets[j].Binding.Pane })
	results := make([]resourcegraph.ProcessInventory, len(targets))
	var group sync.WaitGroup
	slots := make(chan struct{}, 8)
	for i, target := range targets {
		group.Go(func() {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
			}
			results[i] = processhost.ObserveInventory([]processhost.InventoryTarget{target})
		})
	}
	group.Wait()
	var inventory resourcegraph.ProcessInventory
	for _, result := range results {
		inventory.Declared = append(inventory.Declared, result.Declared...)
		inventory.Observed = append(inventory.Observed, result.Observed...)
	}
	return inventory
}
