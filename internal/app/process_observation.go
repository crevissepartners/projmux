package app

import (
	"context"
	"fmt"
	"os"
	"slices"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	runtimeservice "github.com/crevissepartners/projmux/internal/services/runtime"
)

// Observation is a separate read protocol owned by the shared runtime service.
// The defined type keeps the owner-side methods in this package.
type processHostObservation runtimeservice.ProcessHostObservation

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
func processObservationMatches(reg coremetadata.Registry, binding processhost.Binding, view processHostObservation) bool {
	return runtimeservice.ProcessObservationMatches(reg, binding, runtimeservice.ProcessHostObservation(view))
}

func processObservationOwnership(reg coremetadata.Registry, binding processhost.Binding, view processHostObservation) bool {
	return runtimeservice.ProcessObservationOwnership(reg, binding, runtimeservice.ProcessHostObservation(view))
}

type remoteProcessObserver struct {
	ctx          context.Context
	registry     coremetadata.Registry
	registryPath string
	binding      processhost.Binding
}

func (r remoteProcessObserver) service() runtimeservice.ProcessObserver {
	return runtimeservice.ProcessObserver{Ctx: r.ctx, Registry: r.registry, RegistryPath: r.registryPath, Binding: r.binding}
}

func (r remoteProcessObserver) Observe(binding processhost.Binding) (processhost.Snapshot, error) {
	return r.service().Observe(binding)
}

// recordedProcessExit projects a durable supervisor receipt for the exact
// current activation. Any mismatch falls through to the live host read.
func recordedProcessExit(reg coremetadata.Registry, binding processhost.Binding, pane coremetadata.Pane, activation coremetadata.ProcessActivation, provider string) (processhost.Snapshot, bool) {
	return runtimeservice.RecordedProcessExit(reg, binding, pane, activation, provider)
}

// observeHostView reads the exact live owner's observation, including the
// protocol fields a protocol-0 owner leaves empty.
func (r remoteProcessObserver) observeHostView(binding processhost.Binding, activation coremetadata.ProcessActivation, provider string) (processHostObservation, error) {
	view, err := r.service().ObserveHostView(binding, activation, provider)
	return processHostObservation(view), err
}

// processHostRevisionLookup reports the build revision of the owner hosting
// an exact process Pane's current activation, or empty when it is unknown.
type processHostRevisionLookup func(coremetadata.Registry, coremetadata.Pane) string

// defaultProcessHostRevisionLookup asks the live owner within its independent
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
// process Pane's current activation within an independent read budget.
func observeProcessHostOwner(registry coremetadata.Registry, registryPath string, pane coremetadata.Pane) (processHostObservation, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), processObservationReadLimit)
	defer cancel()
	return observeProcessHostOwnerContext(ctx, registry, registryPath, pane)
}

// observeProcessHostOwnerContext consumes its caller's remaining read budget.
// Existing callers retain the wrapper's independent 250ms bound.
func observeProcessHostOwnerContext(ctx context.Context, registry coremetadata.Registry, registryPath string, pane coremetadata.Pane) (processHostObservation, bool) {
	if ctx.Err() != nil {
		return processHostObservation{}, false
	}
	activation := pane.Status.Activation.Process
	if activation == nil {
		return processHostObservation{}, false
	}
	binding := processSchemaBinding(activation.Binding)
	agent, ok := registry.Agent(binding.Agent)
	if !ok {
		return processHostObservation{}, false
	}
	observer := remoteProcessObserver{ctx: ctx, registry: registry, registryPath: registryPath, binding: binding}
	view, err := observer.observeHostView(binding, *activation, agent.Spec.Provider)
	if err != nil {
		return processHostObservation{}, false
	}
	return view, true
}

// processHostSocket names the owned control socket of one provider's host.
func processHostSocket(provider, registryPath, pane, generation string) string {
	return runtimeservice.ProcessHostSocket(provider, registryPath, pane, generation)
}

// processHostRequest wraps an observation or foreground request in the
// provider host's wire envelope.
func processHostRequest(provider string, observe *processhost.Binding, foreground *processForegroundRequest) any {
	if provider == aiModeCodex {
		return codexProcessExchange{Observe: observe, Foreground: foreground}
	}
	return claudeProcessCheck{Observe: observe, Foreground: foreground}
}

// Each owner gets this budget after acquiring a probe slot. Queueing behind
// another owner does not consume it. Failures remain unknown.
const processObservationReadLimit = runtimeservice.ProcessObservationReadLimit

// observeRegistryProcesses observes the default Registry's process
// declarations through the shared runtime service and keeps the CLI's stderr
// deadline diagnostic.
func observeRegistryProcesses(ctx context.Context, registry coremetadata.Registry) resourcegraph.ProcessInventory {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return resourcegraph.ProcessInventory{}
	}
	return runtimeservice.ObserveRegistryProcesses(ctx, registry, intmetadata.PathFor(paths.StateDir), func(ownerTimeouts, totalLimited int) {
		// Internal diagnostic only: no provider content or control authority.
		fmt.Fprintf(os.Stderr, "process observation deadline: owner_timeouts=%d total_limited=%d\n", ownerTimeouts, totalLimited)
	})
}
