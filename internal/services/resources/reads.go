// Package resources owns the read-only resource selection and projection shared
// by the CLI and the experimental SDK. It never persists Registry state.
package resources

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/core/selector"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	tmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

// Failure classifies a read without changing its CLI message or usage status.
type Failure struct {
	Code  string
	Cause error
}

func (e *Failure) Error() string            { return e.Cause.Error() }
func (e *Failure) Unwrap() error            { return e.Cause }
func (e *Failure) MetadataUsageError() bool { return metadata.IsUsageError(e.Cause) }
func Classify(err error) error {
	if err == nil {
		return nil
	}
	var f *Failure
	if errors.As(err, &f) {
		return err
	}
	code := "registry-unavailable"
	var s *selector.SelectorError
	if errors.As(err, &s) {
		code = "selector-invalid"
		if s.Want != "" {
			if s.Got == 0 {
				code = "resource-not-found"
			} else {
				code = "selector-ambiguous"
			}
		}
	}
	return &Failure{Code: code, Cause: err}
}
func Load(paths config.Paths) (metadata.Registry, error) {
	r, e := store.NewDefaultStore(paths).LoadDegradedReadOnly()
	return r, Classify(e)
}

// Snapshot binds selection and ephemeral context to one observation.
type Snapshot struct {
	Runtime    metadata.RuntimeObservation
	Contexts   registryview.Projector
	Navigation map[string]registryview.Row
}

func BuildSnapshot(registry metadata.Registry, inventory resourcegraph.Inventory) Snapshot {
	graph := resourcegraph.Resolve(registry, inventory)
	observed := metadata.RuntimeObservation{ProcessPanes: map[string]bool{}, Windows: map[string]bool{}, Panes: map[string]bool{}, Sessions: map[string]bool{}, SessionsObserved: true, Unobserved: map[string]bool{}}
	mark := func(set map[string]bool, uid string, status resourcegraph.Status) {
		switch status {
		case resourcegraph.StatusLive:
			set[uid] = true
		case resourcegraph.StatusUnknown:
			observed.Unobserved[uid] = true
		}
	}
	for _, n := range graph.Projects {
		mark(observed.Sessions, n.Project.Metadata.UID, n.Status)
	}
	for _, n := range graph.Windows {
		mark(observed.Windows, n.Window.Metadata.UID, n.Status)
	}
	for _, n := range graph.Panes {
		mark(observed.Panes, n.Pane.Metadata.UID, n.Status)
		if n.Process != nil {
			observed.ProcessPanes[n.Pane.Metadata.UID] = true
		}
	}
	for _, n := range graph.Agents {
		if n.Status == resourcegraph.StatusUnknown {
			observed.Unobserved[n.Agent.Metadata.UID] = true
		}
	}
	rows := map[string]registryview.Row{}
	for _, row := range registryview.Build(registryview.Input{Graph: graph}).Rows {
		if row.UID != "" {
			rows[row.UID] = row
		}
	}
	return Snapshot{Runtime: observed, Contexts: registryview.NewObservedContextProjector(graph), Navigation: rows}
}

// Observe captures only the tmux data used by resource JSON context. Process
// control and navigation diagnostics remain with their CLI entrypoints.
func Observe(ctx context.Context, registry metadata.Registry, transport resourcegraph.Transport) Snapshot {
	inventory := store.NewInventoryObserver(tmux.ExecRunner{}, transport).Observe(ctx)
	return BuildSnapshot(registry, inventory)
}
func Resolve(registry metadata.Registry, runtime metadata.RuntimeObservation, contexts *registryview.Projector, kind metadata.Kind, query selector.Query) (selector.Resolution, error) {
	resolver := selector.NewObserved(registry, runtime)
	if contexts != nil {
		resolver = selector.NewObservedWithContext(registry, runtime, *contexts)
	}
	var r selector.Resolution
	var e error
	switch kind {
	case metadata.KindProject:
		r, e = resolver.ResolveProjects(query)
	case metadata.KindWindow:
		r, e = resolver.ResolveWindows(query)
	case metadata.KindPane:
		r, e = resolver.ResolvePanes(query)
	case metadata.KindAgent:
		r, e = resolver.ResolveAgents(query)
	default:
		e = fmt.Errorf("no resolution pipeline for kind %q", kind)
	}
	return r, Classify(e)
}
func Enforce(target selector.Target, query selector.Query, result selector.Resolution) error {
	return Classify(selector.Enforce(target, selector.DescribeSelector(query), result))
}

// ResourceFor preserves the CLI's Agent workspace, interaction, and activation
// defaults. Every returned value owns its maps, slices, and pointer members.
func ResourceFor(registry metadata.Registry, kind metadata.Kind, uid string) (any, metadata.ObjectMeta, bool) {
	switch kind {
	case metadata.KindProject:
		if p, ok := registry.Project(uid); ok {
			v := p.Clone()
			return v, v.Metadata, true
		}
	case metadata.KindWindow:
		if p, ok := registry.Window(uid); ok {
			v := p.Clone()
			return v, v.Metadata, true
		}
	case metadata.KindPane:
		if p, ok := registry.Pane(uid); ok {
			v := p.Clone()
			return v, v.Metadata, true
		}
	case metadata.KindAgent:
		if a, ok := registry.Agent(uid); ok {
			v := a.Clone()
			if v.Spec.Workspace.IsZero() {
				if w, ok := registry.Window(v.Metadata.OwnerUID()); ok {
					if p, ok := registry.Project(w.Metadata.OwnerUID()); ok {
						v.Spec.Workspace.CWD = p.Spec.Root
					}
				}
			}
			v.Status.Interaction = v.EffectiveInteraction(time.Now())
			if v.Status.Activation.State == "" {
				v.Status.Activation.State = metadata.ActivationNotRequested
			}
			return v, v.Metadata, true
		}
	}
	return nil, metadata.ObjectMeta{}, false
}
