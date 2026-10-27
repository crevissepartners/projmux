package projmux

import (
	"context"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// GraphObservationRequest selects at most one exact tmux server, exactly as
// RuntimeRequest does: SocketName corresponds to --socket and SocketPath must
// be absolute. Empty route fields use an explicitly configured
// Options.SocketPath, otherwise no server.
type GraphObservationRequest struct {
	SocketName string
	SocketPath string
}

// ObserveGraphInventory observes the selected tmux server once and returns
// the observation as a value for ResolveResourceGraphFromPrepared. It reads no
// Registry and observes no process host. Without a server, or when the server
// cannot be read, the inventory names the unreadable scopes in Unavailable;
// that is not an error. Nothing is written to tmux, the Registry or the
// caller's stderr. Routing refusals are CodeRuntimeRequestInvalid, as for
// RuntimeRequest. The result owns its slices. Cancellation is checked on entry
// and before returning success.
func (c *Client) ObserveGraphInventory(ctx context.Context, request GraphObservationRequest) (core.GraphRuntimeInventory, error) {
	if err := ctx.Err(); err != nil {
		return core.GraphRuntimeInventory{}, err
	}
	transport, err := c.runtimeTransport("GraphObservationRequest", request.SocketName, request.SocketPath)
	if err != nil {
		return core.GraphRuntimeInventory{}, err
	}
	observed := newResourceGraphReader(c.paths).Observe(ctx, transport)
	if err := ctx.Err(); err != nil {
		return core.GraphRuntimeInventory{}, err
	}
	return publicGraphRuntimeInventory(observed), nil
}

// PreparedGraphOverlay is the runtime evidence a graph is resolved against:
// one tmux observation, typically from ObserveGraphInventory, and the process
// evidence the caller holds. Both are values; equal values resolve equally.
type PreparedGraphOverlay struct {
	Runtime   core.GraphRuntimeInventory
	Processes core.GraphProcessInventory
}

// ResolveResourceGraphFromPrepared resolves the graph of a prepared Registry
// snapshot against overlay with the graph rules ObserveResourceGraph uses. It
// is a pure computation: it reads no file, stats nothing, observes no tmux
// server or process host and reads no clock. The same handle and equal overlay
// values give an equal graph: order, empty slices, conflicts and process keys
// included. A changed overlay is resolved anew; the handle's file identity
// never stands in for runtime evidence.
//
// The tmux half is overlay.Runtime as given, including its transport and
// unavailable scopes. A process Pane without a matching declared key and
// observation in overlay.Processes is "unknown", as in
// ObserveResourceGraphFromSnapshot. The result's RegistryIdentity is a copy of
// the handle's identity.
//
// A nil or zero handle, or one prepared by a Client with a different state
// root, is a CodePreparedSnapshotInvalid refusal. Neither input is modified,
// the result owns its nested values, and concurrent calls with one handle are
// supported. Cancellation is checked on entry and before returning success.
func (c *Client) ResolveResourceGraphFromPrepared(ctx context.Context, prepared *PreparedRegistrySnapshot, overlay PreparedGraphOverlay) (core.ResourceGraph, error) {
	if err := ctx.Err(); err != nil {
		return core.ResourceGraph{}, err
	}
	if c.pathsErr != nil {
		return core.ResourceGraph{}, resourceRefusal(c.pathsErr)
	}
	if !prepared.valid() {
		return core.ResourceGraph{}, &Refusal{Code: CodePreparedSnapshotInvalid, message: "prepared Registry snapshot was not prepared by PrepareRegistrySnapshot"}
	}
	if prepared.root != c.preparedRoot() {
		return core.ResourceGraph{}, &Refusal{Code: CodePreparedSnapshotInvalid, message: "prepared Registry snapshot belongs to a different state root"}
	}
	inventory := internalGraphRuntimeInventory(overlay.Runtime)
	inventory.Processes = internalProcessInventory(overlay.Processes)
	// Resolve clones the Registry and inventory, so the shared handle is never
	// written.
	out, err := publicResourceGraph(resourcegraph.Resolve(prepared.registry, inventory), nil)
	if err != nil {
		return core.ResourceGraph{}, err
	}
	out.RegistryIdentity = copyRegistryFileIdentity(prepared.identity)
	if err := ctx.Err(); err != nil {
		return core.ResourceGraph{}, err
	}
	return out, nil
}

// The conversions copy field by field and keep nil and empty slices apart, so
// equal public values are equal internal values and the reverse.

func publicGraphRuntimeInventory(in resourcegraph.Inventory) core.GraphRuntimeInventory {
	out := core.GraphRuntimeInventory{
		Transport: core.RuntimeTransport{Kind: string(in.Transport.Kind), Value: in.Transport.Value, Source: string(in.Transport.Source)},
		HostMode:  string(in.HostMode),
	}
	if in.Sessions != nil {
		out.Sessions = make([]core.GraphRuntimeSession, len(in.Sessions))
		for i, s := range in.Sessions {
			out.Sessions[i] = core.GraphRuntimeSession{ID: s.ID, Name: s.Name, ProjectUID: s.ProjectUID, ProjectName: s.ProjectName, Root: s.Root, Role: s.Role, Ephemeral: s.Ephemeral}
		}
	}
	if in.Windows != nil {
		out.Windows = make([]core.GraphRuntimeWindow, len(in.Windows))
		for i, w := range in.Windows {
			out.Windows[i] = core.GraphRuntimeWindow{ID: w.ID, SessionID: w.SessionID, Index: w.Index, DisplayName: w.DisplayName, UID: w.UID, MirroredName: w.MirroredName, Active: w.Active}
		}
	}
	if in.Panes != nil {
		out.Panes = make([]core.GraphRuntimePane, len(in.Panes))
		for i, p := range in.Panes {
			out.Panes[i] = core.GraphRuntimePane{
				ID: p.ID, WindowID: p.WindowID, UID: p.UID, MirroredName: p.MirroredName, AgentProvider: p.AgentProvider,
				AgentLaunchAuthorship: p.AgentLaunchAuthorship, AgentSessionID: p.AgentSessionID, AgentThreadID: p.AgentThreadID, Title: p.Title,
			}
		}
	}
	if in.Unavailable != nil {
		out.Unavailable = make([]core.RuntimeUnavailability, len(in.Unavailable))
		for i, u := range in.Unavailable {
			out.Unavailable[i] = core.RuntimeUnavailability{Scope: string(u.Scope), Reason: u.Reason}
		}
	}
	return out
}

func internalGraphRuntimeInventory(in core.GraphRuntimeInventory) resourcegraph.Inventory {
	out := resourcegraph.Inventory{
		Transport: resourcegraph.Transport{Kind: resourcegraph.TransportKind(in.Transport.Kind), Value: in.Transport.Value, Source: resourcegraph.TransportSource(in.Transport.Source)},
		HostMode:  resourcegraph.HostMode(in.HostMode),
	}
	if in.Sessions != nil {
		out.Sessions = make([]resourcegraph.Session, len(in.Sessions))
		for i, s := range in.Sessions {
			out.Sessions[i] = resourcegraph.Session{ID: s.ID, Name: s.Name, ProjectUID: s.ProjectUID, ProjectName: s.ProjectName, Root: s.Root, Role: s.Role, Ephemeral: s.Ephemeral}
		}
	}
	if in.Windows != nil {
		out.Windows = make([]resourcegraph.Window, len(in.Windows))
		for i, w := range in.Windows {
			out.Windows[i] = resourcegraph.Window{ID: w.ID, SessionID: w.SessionID, Index: w.Index, DisplayName: w.DisplayName, UID: w.UID, MirroredName: w.MirroredName, Active: w.Active}
		}
	}
	if in.Panes != nil {
		out.Panes = make([]resourcegraph.Pane, len(in.Panes))
		for i, p := range in.Panes {
			out.Panes[i] = resourcegraph.Pane{
				ID: p.ID, WindowID: p.WindowID, UID: p.UID, MirroredName: p.MirroredName, AgentProvider: p.AgentProvider,
				AgentLaunchAuthorship: p.AgentLaunchAuthorship, AgentSessionID: p.AgentSessionID, AgentThreadID: p.AgentThreadID, Title: p.Title,
			}
		}
	}
	if in.Unavailable != nil {
		out.Unavailable = make([]resourcegraph.Unavailability, len(in.Unavailable))
		for i, u := range in.Unavailable {
			out.Unavailable[i] = resourcegraph.Unavailability{Scope: resourcegraph.Scope(u.Scope), Reason: u.Reason}
		}
	}
	return out
}
