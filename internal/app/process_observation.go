package app

import (
	"context"
	"os"
	"slices"
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
	// Control protocol v1 (processHostProtocol). Revision is the owner's build
	// revision, Actions its supported actions, Turn whether a provider turn is
	// running, and Pending the count of open control requests; their content
	// never crosses. An answer without Protocol is a protocol-0 owner.
	// Coordination is the owner build's claudeCoordinationVersion; an owner
	// that leaves it out predates the field and its version is unknown.
	Protocol     int      `json:",omitempty"`
	Revision     string   `json:",omitempty"`
	Actions      []string `json:",omitempty"`
	Turn         bool     `json:",omitempty"`
	Pending      int      `json:",omitempty"`
	OwnerMode    string   `json:",omitempty"`
	Coordination int      `json:",omitempty"`
}

// newProcessHostObservation describes one owner's current snapshot in control
// protocol v1.
func newProcessHostObservation(binding processhost.Binding, host, child coremetadata.ProcessIdentity, snap processhost.Snapshot, actions []string) processHostObservation {
	return processHostObservation{
		Binding: binding, Host: host, Child: child, Provider: snap.Provider, State: snap.State, Exit: snap.Exit,
		Protocol: processHostProtocol, Revision: processHostRevision(), Actions: slices.Clone(actions),
		Turn: snap.Turn != "", Pending: len(snap.Pending),
		Coordination: claudeCoordinationVersion,
	}
}

// hostRevision is the owner build an observation reports, or empty for a
// protocol-0 owner or a build without a revision.
func (v processHostObservation) hostRevision() string {
	if v.Protocol < 1 || !processRevisionPattern.MatchString(v.Revision) {
		return ""
	}
	return v.Revision
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
	current := metadataProcessBinding(binding)
	activation, provider, ok := reg.CurrentProcessActivation(current)
	return ok && view.Binding == binding && provider == view.Provider &&
		activation.HostProcess == view.Host && activation.Child == view.Child &&
		int64(view.Host.OwnerUID) == int64(os.Getuid()) && int64(view.Child.OwnerUID) == int64(os.Getuid())
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
	if !ok {
		return processhost.Snapshot{}, processhost.ErrStale
	}
	if pane.Status.Activation.IsZero() {
		return retiredProcessObservation(r.registry, binding)
	}
	if pane.Status.Activation.Process == nil {
		return processhost.Snapshot{}, processhost.ErrStale
	}
	activation := pane.Status.Activation.Process
	agent, ok := r.registry.Agent(binding.Agent)
	if !ok {
		return processhost.Snapshot{}, processhost.ErrStale
	}
	if snapshot, ok := recordedProcessExit(r.registry, binding, *pane, *activation, agent.Spec.Provider); ok {
		return snapshot, nil
	}
	return r.observeHost(binding, *activation, agent.Spec.Provider)
}

// recordedProcessExit projects a durable supervisor receipt for the exact
// current activation. Any mismatch falls through to the live host read.
func recordedProcessExit(reg coremetadata.Registry, binding processhost.Binding, pane coremetadata.Pane, activation coremetadata.ProcessActivation, provider string) (processhost.Snapshot, bool) {
	receipt := pane.Status.LastTermination
	if receipt == nil || receipt.Source != coremetadata.TerminationSourceSupervisor ||
		receipt.PaneUID != binding.Pane || receipt.AgentUID != binding.Agent || receipt.Generation != binding.Generation || receipt.OperationID != binding.Operation ||
		receipt.ObservedAt.IsZero() || (receipt.ExitCode == nil && receipt.Signal == "") {
		return processhost.Snapshot{}, false
	}
	exit := &processhost.Exit{Signal: receipt.Signal}
	if receipt.ExitCode != nil {
		exit.Code = *receipt.ExitCode
	}
	view := processHostObservation{Binding: binding, Host: activation.HostProcess, Child: activation.Child, Provider: provider, State: "exited", Exit: exit}
	if receipt.Classification != coremetadata.ClassifyProcessExit(exit.Code, exit.Signal) || !processObservationOwnership(reg, binding, view) {
		return processhost.Snapshot{}, false
	}
	return processhost.Snapshot{Binding: binding, Provider: view.Provider, State: view.State, PID: view.Child.PID, Exit: exit}, true
}

func (r remoteProcessObserver) observeHost(binding processhost.Binding, activation coremetadata.ProcessActivation, provider string) (processhost.Snapshot, error) {
	view, err := r.observeHostView(binding, activation, provider)
	if err != nil {
		return processhost.Snapshot{}, err
	}
	return processhost.Snapshot{Binding: binding, Provider: view.Provider, State: view.State, PID: view.Child.PID, Exit: view.Exit}, nil
}

// observeHostView reads the exact live owner's observation, including the
// protocol fields a protocol-0 owner leaves empty.
func (r remoteProcessObserver) observeHostView(binding processhost.Binding, activation coremetadata.ProcessActivation, provider string) (processHostObservation, error) {
	socket := processHostSocket(provider, r.registryPath, binding.Pane, binding.Generation)
	identity, err := localipc.InspectOwnedSocket(socket)
	if err != nil {
		return processHostObservation{}, err
	}
	result, err := callProcessForeground(r.ctx, socket, identity, activation.HostProcess, processHostRequest(provider, &binding, nil))
	if err != nil {
		return processHostObservation{}, err
	}
	current, err := localipc.InspectOwnedSocket(socket)
	if err != nil || current != identity || !result.Accepted || result.Observation == nil || !processObservationMatches(r.registry, binding, *result.Observation) {
		return processHostObservation{}, processhost.ErrStale
	}
	return *result.Observation, nil
}

// processHostRevisionLookup reports the build revision of the owner hosting
// an exact process Pane's current activation, or empty when it is unknown.
type processHostRevisionLookup func(coremetadata.Registry, coremetadata.Pane) string

// defaultProcessHostRevisionLookup asks the live owner within the shared
// observation read budget. An unreachable or protocol-0 owner is unknown.
func defaultProcessHostRevisionLookup() processHostRevisionLookup {
	return func(registry coremetadata.Registry, pane coremetadata.Pane) string {
		paths, err := config.DefaultPathsFromEnv()
		if err != nil {
			return ""
		}
		view, ok := observeProcessHostOwner(registry, intmetadata.PathFor(paths.StateDir), pane)
		if !ok {
			return ""
		}
		return view.hostRevision()
	}
}

// observeProcessHostOwner reads the observation of the owner hosting an exact
// process Pane's current activation within the shared read budget.
func observeProcessHostOwner(registry coremetadata.Registry, registryPath string, pane coremetadata.Pane) (processHostObservation, bool) {
	activation := pane.Status.Activation.Process
	if activation == nil {
		return processHostObservation{}, false
	}
	binding := processSchemaBinding(activation.Binding)
	agent, ok := registry.Agent(binding.Agent)
	if !ok {
		return processHostObservation{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), processObservationReadLimit)
	defer cancel()
	observer := remoteProcessObserver{ctx: ctx, registry: registry, registryPath: registryPath, binding: binding}
	view, err := observer.observeHostView(binding, *activation, agent.Spec.Provider)
	if err != nil {
		return processHostObservation{}, false
	}
	return view, true
}

// processHostSocket names the owned control socket of one provider's host.
func processHostSocket(provider, registryPath, pane, generation string) string {
	if provider == aiModeCodex {
		return processCodexHostSocket(registryPath, pane, generation)
	}
	return processClaudeHostSocket(registryPath, pane, generation)
}

// processHostRequest wraps an observation or foreground request in the
// provider host's wire envelope.
func processHostRequest(provider string, observe *processhost.Binding, foreground *processForegroundRequest) any {
	if provider == aiModeCodex {
		return codexProcessExchange{Observe: observe, Foreground: foreground}
	}
	return claudeProcessCheck{Observe: observe, Foreground: foreground}
}

// Retired session history can prove offline state, but cannot grant a host or
// control authority. CurrentProcessActivation deliberately remains false.
func retiredProcessObservation(reg coremetadata.Registry, binding processhost.Binding) (processhost.Snapshot, error) {
	if err := reg.Validate(); err != nil {
		return processhost.Snapshot{}, processhost.ErrStale
	}
	pane, ok := reg.Pane(binding.Pane)
	if !ok || !pane.Status.Activation.IsZero() || pane.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess || pane.Status.ProcessSession == nil {
		return processhost.Snapshot{}, processhost.ErrStale
	}
	record := pane.Status.ProcessSession
	agent, ok := reg.Agent(binding.Agent)
	if !ok || agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != binding.Pane || record.Provider != agent.Spec.Provider || processSchemaBinding(record.Binding) != binding {
		return processhost.Snapshot{}, processhost.ErrStale
	}
	receipt := pane.Status.LastTermination
	if !coremetadata.MatchesProcessWait(metadataProcessBinding(binding), receipt) || !coremetadata.SameProcessWait(receipt, agent.Status.LastTermination) {
		return processhost.Snapshot{}, processhost.ErrStale
	}
	exit := &processhost.Exit{Signal: receipt.Signal}
	if receipt.ExitCode != nil {
		exit.Code = *receipt.ExitCode
	}
	return processhost.Snapshot{Binding: binding, Provider: agent.Spec.Provider, State: "exited", Exit: exit}, nil
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
		if pane.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess {
			continue
		}
		var binding processhost.Binding
		if pane.Status.Activation.Process != nil {
			binding = processSchemaBinding(pane.Status.Activation.Process.Binding)
		} else if pane.Status.ProcessSession != nil {
			binding = processSchemaBinding(pane.Status.ProcessSession.Binding)
		} else {
			continue
		}
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
				key := resourcegraph.ProcessKey{Host: target.Binding.Host, Pane: target.Binding.Pane, Generation: target.Binding.Generation}
				results[i] = resourcegraph.ProcessInventory{Declared: []resourcegraph.ProcessKey{key}, Observed: []resourcegraph.ProcessObservation{{Key: key, Status: resourcegraph.StatusUnknown}}}
				return
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
