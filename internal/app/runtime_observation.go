package app

import (
	"context"
	"sync"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

// The live-tmux observation the Window and Pane status read is derived from.
//
// Window and Pane status used to be inherited from the owning Project's stored
// `status.session.live` bool. That bool is written by the reconciler, the
// reconciler runs only on the mutation routes, and the read verbs load the
// registry read-only -- so a Project imported once with a live session reported
// its Windows and Panes live forever, against a machine where nothing of the
// sort was running. Status is an observation; it must be observed.
//
// Four decisions shape this seam, and each one is a decision rather than an
// implementation detail:
//
//  1. The observation is taken at the command entrypoint, once per process
//     invocation, and status is derived from it at read time. It is not a
//     per-resource query -- that would scale tmux calls with the size of the
//     result -- and it is not a per-route reconcile, which would break the
//     read-only contract documented on resourceStore: a read that resolves
//     nothing must never materialize <state>/projmux/metadata/.
//  2. Nothing is cached and nothing is persisted. A TTL would directly defeat
//     the property that makes this worth doing: closing a pane must make the
//     *next* query report it offline, not the next query after the TTL expires.
//     One snapshot per process is the shortest lifetime that still answers a
//     whole invocation consistently.
//  3. The budget is two tmux queries -- `list-panes -a` and `list-windows -a`.
//     Both are lazy: a route that never renders status never pays for them.
//  4. A failed query yields an empty observation, not an inherited stored
//     value. Empty can only downgrade a resource to offline; it can never
//     invent a live one, so a tmux server that is down or unreachable reports
//     "nothing is live", which is what is actually true of a machine with no
//     tmux server.

// liveRuntimeInventory is the mirrored-uid inventory seam of the observation.
// It is the read half of the existing tmux identity mirror; nothing here
// writes, re-mirrors, or adopts a uid onto a live tmux object.
type liveRuntimeInventory interface {
	LiveWindowUIDs(ctx context.Context) (map[string]bool, error)
	LivePaneUIDs(ctx context.Context) (map[string]bool, error)
}

// runtimeLookup returns the invocation's live-tmux observation. It is memoized,
// so a route that resolves several times -- delete's per-ref unmatched probe,
// for one -- still observes the machine exactly once and judges every match
// against the same snapshot.
type runtimeLookup func() coremetadata.RuntimeObservation

// resourceReadSnapshot is the one exact-host observation shared by selector
// status and human context for one read invocation.
type resourceReadSnapshot struct {
	runtime  coremetadata.RuntimeObservation
	contexts registryview.Projector
	// navigation reuses the same resolved graph, including control subtree actions.
	navigation map[string]registryview.Row
}

type resourceReadLookup func(coremetadata.Registry) resourceReadSnapshot

func runtimeResourceReadLookup(reader *runtimeDiagnosticsReader) resourceReadLookup {
	return func(registry coremetadata.Registry) resourceReadSnapshot {
		fallback := func() resourceReadSnapshot {
			return resourceReadSnapshot{contexts: registryview.NewContextProjector(registry), navigation: resourceNavigationRows(resourcegraph.Resolve(registry, resourcegraph.Inventory{}))}
		}
		if reader == nil || reader.observe == nil {
			return fallback()
		}
		transport, err := reader.transport(runtimeTransportRequest{})
		if err != nil {
			return fallback()
		}
		inventory := reader.observe(context.Background(), transport)
		graph := resourcegraph.Resolve(registry, inventory)
		windows := make(map[string]bool)
		for _, node := range graph.Windows {
			if node.Status == resourcegraph.StatusLive {
				windows[node.Window.Metadata.UID] = true
			}
		}
		panes := make(map[string]bool)
		for _, node := range graph.Panes {
			if node.Status == resourcegraph.StatusLive {
				panes[node.Pane.Metadata.UID] = true
			}
		}
		return resourceReadSnapshot{
			runtime:    coremetadata.RuntimeObservation{Windows: windows, Panes: panes},
			contexts:   registryview.NewObservedContextProjector(graph),
			navigation: resourceNavigationRows(graph),
		}
	}
}

// resourceNavigationRows indexes the existing action projector without changing
// its row set/order or re-deriving policy from selector status.
func resourceNavigationRows(graph resourcegraph.Graph) map[string]registryview.Row {
	rows := map[string]registryview.Row{}
	for _, row := range registryview.Build(registryview.Input{Graph: graph}).Rows {
		if row.UID != "" {
			rows[row.UID] = row
		}
	}
	return rows
}

// defaultRuntimeLookup is the production seam: the same tmux identity mirror
// the active-target fallback and the dead-pane sweep already read through.
//
// It shells out as bare `tmux`, exactly like every other mirror read, so inside
// a tmux client $TMUX selects the projmux socket. Introducing a second socket
// convention for this one query would make the observation disagree with the
// mirror writes it is diffed against.
func defaultRuntimeLookup() runtimeLookup {
	return tmuxRuntimeLookup(intmetadata.NewMirror(inttmux.ExecRunner{}))
}

// tmuxRuntimeLookup builds a memoized lookup over an injectable inventory.
func tmuxRuntimeLookup(inventory liveRuntimeInventory) runtimeLookup {
	return sync.OnceValue(func() coremetadata.RuntimeObservation {
		return observeRuntime(context.Background(), inventory)
	})
}

// observeRuntime takes one live-tmux observation.
//
// The two reads are independent: a failure of one leaves that half empty rather
// than discarding the other, because a Pane whose tmux pane is provably gone is
// still offline even if the window inventory could not be read.
func observeRuntime(ctx context.Context, inventory liveRuntimeInventory) coremetadata.RuntimeObservation {
	if inventory == nil {
		return coremetadata.RuntimeObservation{}
	}
	windows, err := inventory.LiveWindowUIDs(ctx)
	if err != nil {
		windows = nil
	}
	panes, err := inventory.LivePaneUIDs(ctx)
	if err != nil {
		panes = nil
	}
	return coremetadata.RuntimeObservation{Windows: windows, Panes: panes}
}

// observation resolves a possibly-nil lookup into a snapshot. A nil lookup is
// the empty observation, which is the same fail-closed reading a failed query
// gets: everything with a runtime object of its own reports offline.
func (l runtimeLookup) observation() coremetadata.RuntimeObservation {
	if l == nil {
		return coremetadata.RuntimeObservation{}
	}
	return l()
}
