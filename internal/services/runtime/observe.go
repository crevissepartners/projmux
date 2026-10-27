// Package runtime owns read-only transport resolution, graph joining and runtime
// projection shared by the CLI and SDK. It has no mutation authority.
package runtime

import (
	"context"
	"errors"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/core/runtimediag"
	inventory "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/services/resources"
)

// ResolveTransport validates and resolves one exact routing request.
func ResolveTransport(request resourcegraph.TransportRequest) (resourcegraph.Transport, error) {
	return resourcegraph.ResolveTransport(request)
}

// Reader joins one inventory to a read-only Registry. Callbacks let CLI wiring
// retain its process diagnostics and error presentation without SDK app imports.
// Identity, when set, stats the same Registry file LoadRegistry reads.
type Reader struct {
	LoadRegistry func() (metadata.Registry, error)
	Identity     func() (inventory.RegistryFileIdentity, error)
	Observe      func(context.Context, resourcegraph.Transport) resourcegraph.Inventory
	Processes    func(context.Context, metadata.Registry) resourcegraph.ProcessInventory
}

func (r Reader) Resolve(ctx context.Context, transport resourcegraph.Transport) (resourcegraph.Graph, error) {
	graph, _, err := r.resolve(ctx, transport, false)
	return graph, err
}

// ResolveIdentified resolves one graph from a single Registry load and stats
// the Registry file immediately before and after that load. The identity is
// returned only when both stats succeed and agree; otherwise it is nil and the
// graph is still returned, because a rewrite between the stats leaves the rows
// valid but unattributable to one file state.
func (r Reader) ResolveIdentified(ctx context.Context, transport resourcegraph.Transport) (resourcegraph.Graph, *inventory.RegistryFileIdentity, error) {
	if r.Identity == nil {
		return resourcegraph.Graph{}, nil, errors.New("runtime diagnostics registry identity is not configured")
	}
	return r.resolve(ctx, transport, true)
}

func (r Reader) resolve(ctx context.Context, transport resourcegraph.Transport, identify bool) (resourcegraph.Graph, *inventory.RegistryFileIdentity, error) {
	if err := ctx.Err(); err != nil {
		return resourcegraph.Graph{}, nil, err
	}
	if r.LoadRegistry == nil {
		return resourcegraph.Graph{}, nil, errors.New("runtime diagnostics registry reader is not configured")
	}
	var before inventory.RegistryFileIdentity
	var beforeErr error
	if identify {
		before, beforeErr = r.Identity()
	}
	registry, err := r.LoadRegistry()
	if err != nil {
		return resourcegraph.Graph{}, nil, err
	}
	var identity *inventory.RegistryFileIdentity
	if identify && beforeErr == nil {
		if after, afterErr := r.Identity(); afterErr == nil && after == before {
			identity = &after
		}
	}
	if err := ctx.Err(); err != nil {
		return resourcegraph.Graph{}, nil, err
	}
	if r.Observe == nil {
		return resourcegraph.Graph{}, nil, errors.New("runtime diagnostics observer is not configured")
	}
	observed := r.Observe(ctx, transport)
	if err := ctx.Err(); err != nil {
		return resourcegraph.Graph{}, nil, err
	}
	if r.Processes != nil {
		observed.Processes = r.Processes(ctx, registry).Clone()
	}
	if err := ctx.Err(); err != nil {
		return resourcegraph.Graph{}, nil, err
	}
	return resourcegraph.Resolve(registry, observed), identity, nil
}

// ResolveSupplied resolves one graph from a Registry and process inventory the
// caller already holds. It never calls LoadRegistry, Identity or Processes:
// only the selected tmux server is observed, once, and the graph rules are
// those of Resolve. The supplied values are not modified.
func (r Reader) ResolveSupplied(ctx context.Context, registry metadata.Registry, processes resourcegraph.ProcessInventory, transport resourcegraph.Transport) (resourcegraph.Graph, error) {
	if err := ctx.Err(); err != nil {
		return resourcegraph.Graph{}, err
	}
	if r.Observe == nil {
		return resourcegraph.Graph{}, errors.New("runtime diagnostics observer is not configured")
	}
	observed := r.Observe(ctx, transport)
	if err := ctx.Err(); err != nil {
		return resourcegraph.Graph{}, err
	}
	observed.Processes = processes.Clone()
	return resourcegraph.Resolve(registry, observed), nil
}

// Project preserves the CLI report ordering and omission rules.
func Project(graph resourcegraph.Graph, kind resourcegraph.ObjectKind) runtimediag.Report {
	return runtimediag.Project(graph, kind)
}

// NewReader wires the bounded tmux-only inventory and read-only Registry store.
// Provider probes are not needed to project tmux objects; CLI diagnostics may
// separately supply a Processes callback for its other graph consumers.
func NewReader(paths config.Paths) Reader {
	return Reader{
		LoadRegistry: func() (metadata.Registry, error) { return resources.Load(paths) },
		Observe: func(ctx context.Context, route resourcegraph.Transport) resourcegraph.Inventory {
			return inventory.NewInventoryObserver(tmux.ExecRunner{}, route).Observe(ctx)
		},
	}
}

// NewGraphReader extends NewReader with the Registry file identity and the
// process observation of the same Registry, without a deadline diagnostic.
func NewGraphReader(paths config.Paths) Reader {
	reader := NewReader(paths)
	reader.Identity = inventory.NewDefaultStore(paths).RegistryFileIdentity
	registryPath := inventory.PathFor(paths.StateDir)
	reader.Processes = func(ctx context.Context, registry metadata.Registry) resourcegraph.ProcessInventory {
		return ObserveRegistryProcesses(ctx, registry, registryPath, nil)
	}
	return reader
}
