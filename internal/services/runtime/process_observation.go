package runtime

import (
	"context"
	"errors"
	"os"
	"sort"
	"sync"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	claudeadapter "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// ProcessHostObservation is a separate read protocol. Provider input, pending
// requests, output, diagnostics and control authority never cross this
// boundary.
type ProcessHostObservation struct {
	Binding         processhost.Binding
	Host, Child     coremetadata.ProcessIdentity
	Provider, State string
	Exit            *processhost.Exit `json:",omitempty"`
	// Control protocol v1 (ProcessHostProtocol). Revision is the owner's build
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
	// Reads are the read requests the owner serves, separate from Actions;
	// an owner that leaves them out serves none (ProcessHostReads).
	Reads []string `json:",omitempty"`
	// Session, Connection, Sequence and TurnID copy the owner's snapshot:
	// the provider session, its connection, the last event sequence and the
	// running turn. An owner that leaves them out predates the fields.
	Session, Connection, TurnID string `json:",omitempty"`
	Sequence                    uint64 `json:",omitempty"`
}

// ProcessSchemaBinding converts a Registry process binding to the host's
// binding. Binding conversions are shared across creation, authority and
// observation.
func ProcessSchemaBinding(b coremetadata.ProcessBinding) processhost.Binding {
	return processhost.Binding{Host: b.HostInstanceID, Project: b.ProjectUID, Window: b.WindowUID, Agent: b.AgentUID, Pane: b.PaneUID, Generation: b.Generation, Operation: b.OperationID}
}

// MetadataProcessBinding converts a host binding to the Registry's binding.
func MetadataProcessBinding(b processhost.Binding) coremetadata.ProcessBinding {
	return coremetadata.ProcessBinding{HostInstanceID: b.Host, ProjectUID: b.Project, WindowUID: b.Window, AgentUID: b.Agent, PaneUID: b.Pane, Generation: b.Generation, OperationID: b.Operation}
}

// ProcessObservationMatches is the check both the responder and reader apply:
// they compare the complete immutable activation. An unavailable child is not
// an exit; only the host's reaped Wait can say so.
func ProcessObservationMatches(reg coremetadata.Registry, binding processhost.Binding, view ProcessHostObservation) bool {
	if !ProcessObservationOwnership(reg, binding, view) {
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

// ProcessObservationOwnership reports whether view names the exact current
// activation of binding, owned by this user.
func ProcessObservationOwnership(reg coremetadata.Registry, binding processhost.Binding, view ProcessHostObservation) bool {
	current := MetadataProcessBinding(binding)
	activation, provider, ok := reg.CurrentProcessActivation(current)
	return ok && view.Binding == binding && provider == view.Provider &&
		activation.HostProcess == view.Host && activation.Child == view.Child &&
		int64(view.Host.OwnerUID) == int64(os.Getuid()) && int64(view.Child.OwnerUID) == int64(os.Getuid())
}

// ProcessObserver reads one exact binding from a Registry value. RegistryPath
// locates the owners' control sockets; Ctx bounds every host read.
type ProcessObserver struct {
	Ctx          context.Context
	Registry     coremetadata.Registry
	RegistryPath string
	Binding      processhost.Binding
}

func (r ProcessObserver) Observe(binding processhost.Binding) (processhost.Snapshot, error) {
	if binding != r.Binding || r.Ctx.Err() != nil {
		return processhost.Snapshot{}, processhost.ErrStale
	}
	pane, ok := r.Registry.Pane(binding.Pane)
	if !ok {
		return processhost.Snapshot{}, processhost.ErrStale
	}
	if pane.Status.Activation.IsZero() {
		return retiredProcessObservation(r.Registry, binding)
	}
	if pane.Status.Activation.Process == nil {
		return processhost.Snapshot{}, processhost.ErrStale
	}
	activation := pane.Status.Activation.Process
	agent, ok := r.Registry.Agent(binding.Agent)
	if !ok {
		return processhost.Snapshot{}, processhost.ErrStale
	}
	if snapshot, ok := RecordedProcessExit(r.Registry, binding, *pane, *activation, agent.Spec.Provider); ok {
		return snapshot, nil
	}
	return r.observeHost(binding, *activation, agent.Spec.Provider)
}

// RecordedProcessExit projects a durable supervisor receipt for the exact
// current activation. Any mismatch falls through to the live host read.
func RecordedProcessExit(reg coremetadata.Registry, binding processhost.Binding, pane coremetadata.Pane, activation coremetadata.ProcessActivation, provider string) (processhost.Snapshot, bool) {
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
	view := ProcessHostObservation{Binding: binding, Host: activation.HostProcess, Child: activation.Child, Provider: provider, State: "exited", Exit: exit}
	if !coremetadata.MatchesProcessWait(MetadataProcessBinding(binding), receipt) || !ProcessObservationOwnership(reg, binding, view) {
		return processhost.Snapshot{}, false
	}
	return processhost.Snapshot{Binding: binding, Provider: view.Provider, State: view.State, PID: view.Child.PID, Exit: exit}, true
}

func (r ProcessObserver) observeHost(binding processhost.Binding, activation coremetadata.ProcessActivation, provider string) (processhost.Snapshot, error) {
	view, err := r.ObserveHostView(binding, activation, provider)
	if err != nil {
		return processhost.Snapshot{}, err
	}
	return processhost.Snapshot{Binding: binding, Provider: view.Provider, State: view.State, PID: view.Child.PID, Exit: view.Exit}, nil
}

// ObserveHostView reads the exact live owner's observation, including the
// protocol fields a protocol-0 owner leaves empty.
func (r ProcessObserver) ObserveHostView(binding processhost.Binding, activation coremetadata.ProcessActivation, provider string) (ProcessHostObservation, error) {
	socket := ProcessHostSocket(provider, r.RegistryPath, binding.Pane, binding.Generation)
	identity, err := localipc.InspectOwnedSocket(socket)
	if err != nil {
		return ProcessHostObservation{}, err
	}
	var result processObservationResult
	err = localipc.ExchangeOwnedJSON(r.Ctx, socket, identity, activation.HostProcess, ProcessObservationRequest(provider, binding), &result)
	// Socket inode replacement and PID reuse are refusal, never a reason to
	// rediscover another host or replay.
	if errors.Is(err, localipc.ErrSocketReplaced) {
		err = processhost.ErrStale
	}
	if err != nil {
		return ProcessHostObservation{}, err
	}
	current, err := localipc.InspectOwnedSocket(socket)
	if err != nil || current != identity || !result.Accepted || result.Observation == nil || !ProcessObservationMatches(r.Registry, binding, *result.Observation) {
		return ProcessHostObservation{}, processhost.ErrStale
	}
	return *result.Observation, nil
}

// processObservationResult is the part of the host's foreground answer an
// observation reads. Every other field is ignored.
type processObservationResult struct {
	Accepted    bool
	Observation *ProcessHostObservation `json:",omitempty"`
}

// ClaudeHostSocket names the owned control socket of a Claude provider host.
func ClaudeHostSocket(registryPath, pane, generation string) string {
	return claudeadapter.ActivationLeaseDir(registryPath, pane, generation) + "/host.sock"
}

// CodexHostSocket names the owned control socket of a Codex provider host.
func CodexHostSocket(registryPath, pane, generation string) string {
	return claudeadapter.ActivationLeaseDir(registryPath, pane, generation) + "/codex-host.sock"
}

// ProcessHostSocket names the owned control socket of one provider's host.
func ProcessHostSocket(provider, registryPath, pane, generation string) string {
	if provider == codexProvider {
		return CodexHostSocket(registryPath, pane, generation)
	}
	return ClaudeHostSocket(registryPath, pane, generation)
}

const codexProvider = "codex"

// The observation request is the provider host's own wire envelope with only
// Observe (or, for an owner advertising it, Events) set. The other members are
// always empty here and keep their exact JSON spelling, so the bytes an owner
// reads for an observation are those it always read.
type claudeObservationRequest struct {
	Observe    *processhost.Binding `json:",omitempty"`
	Binding    processhost.Binding
	Session    string
	Register   bool
	Lookup     bool
	Input      *struct{}
	Helper     *struct{}
	Questions  *struct{}
	Foreground *struct{}
	Events     *ProcessEventsRequest `json:",omitempty"`
}

type codexObservationRequest struct {
	Observe    *processhost.Binding `json:",omitempty"`
	Binding    processhost.Binding
	Evidence   coremetadata.CodexProcessRouteEvidence
	MessageRef string
	Questions  *struct{}
	Foreground *struct{}
	Events     *ProcessEventsRequest `json:",omitempty"`
}

// ProcessObservationRequest wraps one observation in the provider host's wire
// envelope.
func ProcessObservationRequest(provider string, binding processhost.Binding) any {
	if provider == codexProvider {
		return codexObservationRequest{Observe: &binding}
	}
	return claudeObservationRequest{Observe: &binding}
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
	if !ok || agent.Status.Phase != coremetadata.PhaseOffline || agent.Status.PaneRef != binding.Pane || record.Provider != agent.Spec.Provider || ProcessSchemaBinding(record.Binding) != binding {
		return processhost.Snapshot{}, processhost.ErrStale
	}
	receipt := pane.Status.LastTermination
	if reg.ProcessHostLostOffline(binding.Pane) {
		// A lost-host retirement proved absence, not exit: no Exit is invented.
		return processhost.Snapshot{Binding: binding, Provider: agent.Spec.Provider, State: processhost.StateRetired}, nil
	}
	if !coremetadata.MatchesProcessWait(MetadataProcessBinding(binding), receipt) || !coremetadata.SameProcessWait(receipt, agent.Status.LastTermination) {
		return processhost.Snapshot{}, processhost.ErrStale
	}
	exit := &processhost.Exit{Signal: receipt.Signal}
	if receipt.ExitCode != nil {
		exit.Code = *receipt.ExitCode
	}
	return processhost.Snapshot{Binding: binding, Provider: agent.Spec.Provider, State: "exited", Exit: exit}, nil
}

// Each owner gets this budget after acquiring a probe slot. Queueing behind
// another owner does not consume it. Failures remain unknown.
const ProcessObservationReadLimit = 250 * time.Millisecond

// Bound the whole inventory separately, including queueing. Large inventories
// can still exhaust this cap; their unobserved declarations remain unknown.
const processObservationTotalLimit = 2 * time.Second

// ProcessDeadlines receives the counts of owners whose reads hit their own
// budget or the total cap, only when at least one did. It carries no provider
// content or control authority.
type ProcessDeadlines func(ownerTimeouts, totalLimited int)

// ObserveRegistryProcesses observes every process declaration in registry.
// Declarations are collected before probing. One unavailable host cannot erase
// another Pane or cause a tmux lookup, and the entire observation is bounded.
// A nil deadlines sink drops the deadline counts.
func ObserveRegistryProcesses(ctx context.Context, registry coremetadata.Registry, registryPath string, deadlines ProcessDeadlines) resourcegraph.ProcessInventory {
	ctx, cancel := context.WithTimeout(ctx, processObservationTotalLimit)
	defer cancel()
	totalDeadline, _ := ctx.Deadline()
	targets := []processhost.InventoryTarget{}
	for _, pane := range registry.Panes {
		if pane.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess {
			continue
		}
		var binding processhost.Binding
		if pane.Status.Activation.Process != nil {
			binding = ProcessSchemaBinding(pane.Status.Activation.Process.Binding)
		} else if pane.Status.ProcessSession != nil {
			binding = ProcessSchemaBinding(pane.Status.ProcessSession.Binding)
		} else {
			continue
		}
		targets = append(targets, processhost.InventoryTarget{Binding: binding, Observer: ProcessObserver{Ctx: ctx, Registry: registry, RegistryPath: registryPath, Binding: binding}})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Binding.Pane < targets[j].Binding.Pane })
	results := make([]resourcegraph.ProcessInventory, len(targets))
	ownerTimeouts := make([]bool, len(targets))
	totalLimited := make([]bool, len(targets))
	var group sync.WaitGroup
	slots := make(chan struct{}, 8)
	for i, target := range targets {
		pane, ok := registry.Pane(target.Binding.Pane)
		if ok && pane.Status.Activation.IsZero() {
			// Exact retired Wait/Phase/Binding evidence needs no owner or
			// network budget. Keep the same validator, even after cancellation.
			target.Observer = ProcessObserver{Ctx: context.Background(), Registry: registry, RegistryPath: registryPath, Binding: target.Binding}
			results[i] = processhost.ObserveInventory([]processhost.InventoryTarget{target})
			continue
		}
		group.Go(func() {
			// Start unknown so every declaration survives an exhausted cap.
			results[i] = processhost.ObserveInventory([]processhost.InventoryTarget{{Binding: target.Binding}})
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				totalLimited[i] = true
				return
			}
			if ctx.Err() != nil {
				totalLimited[i] = true
				return
			}
			ownerCtx, cancelOwner := context.WithTimeout(ctx, ProcessObservationReadLimit)
			defer cancelOwner()
			target.Observer = ProcessObserver{Ctx: ownerCtx, Registry: registry, RegistryPath: registryPath, Binding: target.Binding}
			results[i] = processhost.ObserveInventory([]processhost.InventoryTarget{target})
			// The socket deadline can fire just before context's timer runs.
			// Compare the deadline as well so those timeouts leave a signal.
			deadline, _ := ownerCtx.Deadline()
			now := time.Now()
			if results[i].Observed[0].Status == resourcegraph.StatusUnknown && (ownerCtx.Err() != nil || !now.Before(deadline)) {
				if ctx.Err() != nil || !now.Before(totalDeadline) {
					totalLimited[i] = true
				} else {
					ownerTimeouts[i] = true
				}
			}
		})
	}
	group.Wait()
	var inventory resourcegraph.ProcessInventory
	var ownerTimeoutCount, totalLimitedCount int
	for i, result := range results {
		inventory.Declared = append(inventory.Declared, result.Declared...)
		inventory.Observed = append(inventory.Observed, result.Observed...)
		if ownerTimeouts[i] {
			ownerTimeoutCount++
		}
		if totalLimited[i] {
			totalLimitedCount++
		}
	}
	if ownerTimeoutCount+totalLimitedCount > 0 && deadlines != nil {
		deadlines(ownerTimeoutCount, totalLimitedCount)
	}
	return inventory
}
