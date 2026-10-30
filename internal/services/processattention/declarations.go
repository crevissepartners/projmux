package processattention

import (
	"sort"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	runtimeservice "github.com/crevissepartners/projmux/internal/services/runtime"
)

// Declared is the current set of process declarations of one Registry. It
// reflects declarations only: no runtime discovery, lock or generation
// authority, and unavailable hosts stay included.
type Declared struct {
	// Bindings are sorted by Pane.
	Bindings []processhost.Binding
	// Providers maps a Pane UID to its Agent provider.
	Providers map[string]string
	// ProcessOnly is true when the Registry has process Panes and no tmux Pane.
	ProcessOnly bool
}

// Declarations resolves the process declarations of reg.
func Declarations(reg coremetadata.Registry) Declared {
	d := Declared{Providers: map[string]string{}}
	hasProcess, hasTmux := false, false
	for _, pane := range reg.Panes {
		if pane.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess {
			hasTmux = true
			continue
		}
		hasProcess = true
		a := pane.Status.Activation.Process
		if a == nil {
			agent, ok := reg.Agent(pane.Metadata.OwnerUID())
			if !ok || agent.Status.PaneRef != pane.Metadata.UID || (agent.Spec.Provider != "claude" && agent.Spec.Provider != "codex") {
				continue
			}
			window, ok := reg.Window(agent.Metadata.OwnerUID())
			if !ok {
				continue
			}
			binding := processhost.Binding{Project: window.Metadata.OwnerUID(), Window: window.Metadata.UID, Agent: agent.Metadata.UID, Pane: pane.Metadata.UID}
			d.Bindings = append(d.Bindings, binding)
			d.Providers[binding.Pane] = agent.Spec.Provider
			continue
		}
		binding := runtimeservice.ProcessSchemaBinding(a.Binding)
		agent, ok := reg.Agent(binding.Agent)
		if !ok || !runtimeservice.ProcessObservationOwnership(reg, binding, runtimeservice.ProcessHostObservation{Binding: binding, Host: a.HostProcess, Child: a.Child, Provider: agent.Spec.Provider}) {
			continue
		}
		d.Bindings = append(d.Bindings, binding)
		d.Providers[binding.Pane] = agent.Spec.Provider
	}
	sort.Slice(d.Bindings, func(i, j int) bool { return d.Bindings[i].Pane < d.Bindings[j].Pane })
	d.ProcessOnly = hasProcess && !hasTmux
	return d
}
