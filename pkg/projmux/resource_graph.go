package projmux

import (
	"context"
	"encoding/json"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/services/resources"
	runtimeservice "github.com/crevissepartners/projmux/internal/services/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// ResourceGraphRequest selects at most one exact tmux server, exactly as
// RuntimeRequest does: SocketName corresponds to --socket and SocketPath must
// be absolute. Empty route fields use an explicitly configured
// Options.SocketPath, otherwise no server.
type ResourceGraphRequest struct {
	SocketName string
	SocketPath string
}

// newResourceGraphReader is replaced only by package tests.
var newResourceGraphReader = runtimeservice.NewGraphReader

// ObserveResourceGraph takes one coherent observation: it stats the Registry
// file, reads the Registry once, stats it again, then observes the selected
// tmux server once and the process hosts the Registry declares once, and
// resolves them with the same graph rules the CLI uses. Graph JSON is the CLI's
// internal graph JSON byte for byte.
//
// RegistryIdentity is set only when both stats succeed and agree; a rewrite in
// between leaves it nil and the graph is still returned. Without a server, or
// when the server cannot be read, the rows tmux would judge have Status
// "unknown" and Graph.Unavailable names the tmux scope; a process host that
// cannot be read leaves its process Pane "unknown" with no Unavailable entry.
// Neither is an error. Nothing is written to tmux, the Registry, provider
// hosts or the caller's stderr, and no process admission or control authority
// is returned.
//
// Cancellation is checked between the reads. Registry failures retain the
// registry-unavailable refusal of the other resource reads. Results own their
// nested values; concurrent calls are supported.
func (c *Client) ObserveResourceGraph(ctx context.Context, input ResourceGraphRequest) (core.ResourceGraph, error) {
	if err := ctx.Err(); err != nil {
		return core.ResourceGraph{}, err
	}
	transport, err := c.runtimeTransport("ResourceGraphRequest", input.SocketName, input.SocketPath)
	if err != nil {
		return core.ResourceGraph{}, err
	}
	if c.pathsErr != nil {
		return core.ResourceGraph{}, resourceRefusal(c.pathsErr)
	}
	graph, identity, err := newResourceGraphReader(c.paths).ResolveIdentified(ctx, transport)
	if err != nil {
		if ctx.Err() != nil {
			return core.ResourceGraph{}, ctx.Err()
		}
		return core.ResourceGraph{}, resourceRefusal(err)
	}
	out, err := publicResourceGraph(graph, identity)
	if err != nil {
		return core.ResourceGraph{}, err
	}
	if err := ctx.Err(); err != nil {
		return core.ResourceGraph{}, err
	}
	return out, nil
}

// ReadRegistryFileIdentity stats the Registry file without reading, locking,
// or repairing it, and without runtime or provider contact. For the same file
// state it equals ObserveResourceGraph's RegistryIdentity, so a caller can
// decide cheaply whether a held graph's Registry read is still current. A
// missing or unreadable file is a registry-unavailable refusal, never a zero
// identity.
func (c *Client) ReadRegistryFileIdentity(ctx context.Context) (core.RegistryFileIdentity, error) {
	if err := ctx.Err(); err != nil {
		return core.RegistryFileIdentity{}, err
	}
	if c.pathsErr != nil {
		return core.RegistryFileIdentity{}, resourceRefusal(c.pathsErr)
	}
	identity, err := store.NewDefaultStore(c.paths).RegistryFileIdentity()
	if err != nil {
		return core.RegistryFileIdentity{}, resourceRefusal(&resources.Failure{Code: string(CodeRegistryUnavailable), Cause: err})
	}
	if err := ctx.Err(); err != nil {
		return core.RegistryFileIdentity{}, err
	}
	return publicRegistryFileIdentity(identity), nil
}

// Explicit definitions rather than aliases keep internal types out of the
// public API. JSON conversion also isolates every nested map/slice/pointer.
func publicResourceGraph(graph resourcegraph.Graph, identity *store.RegistryFileIdentity) (core.ResourceGraph, error) {
	raw, err := json.Marshal(graph)
	if err != nil {
		return core.ResourceGraph{}, err
	}
	var out core.ResourceGraph
	if err := json.Unmarshal(raw, &out.Graph); err != nil {
		return core.ResourceGraph{}, err
	}
	if identity != nil {
		value := publicRegistryFileIdentity(*identity)
		out.RegistryIdentity = &value
	}
	return out, nil
}

func publicRegistryFileIdentity(identity store.RegistryFileIdentity) core.RegistryFileIdentity {
	return core.RegistryFileIdentity{
		Device: identity.Device, Inode: identity.Inode, Size: identity.Size, ModTimeNanoseconds: identity.ModTimeNanoseconds,
		ChangeTimeSeconds: identity.ChangeTimeSeconds, ChangeTimeNanoseconds: identity.ChangeTimeNanoseconds,
	}
}
