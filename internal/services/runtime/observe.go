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
type Reader struct {
	LoadRegistry func() (metadata.Registry, error)
	Observe      func(context.Context, resourcegraph.Transport) resourcegraph.Inventory
	Processes    func(context.Context, metadata.Registry) resourcegraph.ProcessInventory
}

func (r Reader) Resolve(ctx context.Context, transport resourcegraph.Transport) (resourcegraph.Graph, error) {
	if err := ctx.Err(); err != nil {
		return resourcegraph.Graph{}, err
	}
	if r.LoadRegistry == nil {
		return resourcegraph.Graph{}, errors.New("runtime diagnostics registry reader is not configured")
	}
	registry, err := r.LoadRegistry()
	if err != nil {
		return resourcegraph.Graph{}, err
	}
	if err := ctx.Err(); err != nil {
		return resourcegraph.Graph{}, err
	}
	if r.Observe == nil {
		return resourcegraph.Graph{}, errors.New("runtime diagnostics observer is not configured")
	}
	inventory := r.Observe(ctx, transport)
	if err := ctx.Err(); err != nil {
		return resourcegraph.Graph{}, err
	}
	if r.Processes != nil {
		inventory.Processes = r.Processes(ctx, registry).Clone()
	}
	if err := ctx.Err(); err != nil {
		return resourcegraph.Graph{}, err
	}
	return resourcegraph.Resolve(registry, inventory), nil
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
