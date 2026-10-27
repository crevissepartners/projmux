package projmux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/services/resources"
	runtimeservice "github.com/crevissepartners/projmux/internal/services/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// liveProcessFixture activates the fixture's process Pane, so a declared and
// observed key can make it live.
func liveProcessFixture(t *testing.T) []byte {
	return mutatedGraphFixture(t, func(r *metadata.Registry) {
		for i := range r.Panes {
			if r.Panes[i].Metadata.UID == "pane-02" {
				r.Panes[i].Status.Activation.Generation = "gen-live"
			}
		}
	})
}

var liveProcessKey = core.GraphProcessKey{Host: "host-one", Pane: "pane-02", Generation: "gen-live"}

func graphPane(t *testing.T, graph core.ResourceGraph, uid string) core.GraphPaneNode {
	t.Helper()
	for _, node := range graph.Graph.Panes {
		if node.Pane.Metadata.UID == uid {
			return node
		}
	}
	t.Fatalf("pane %s missing", uid)
	return core.GraphPaneNode{}
}

// forbidGraphIO fails the test on any Registry stat or load and any runtime
// observation, for the rest of the test.
func forbidGraphIO(t *testing.T) {
	t.Helper()
	load, stat, reader := loadSnapshotRegistry, statSnapshotRegistry, newResourceGraphReader
	loadSnapshotRegistry = func(config.Paths) (metadata.Registry, error) {
		t.Error("pure resolve loaded the Registry")
		return metadata.Registry{}, errors.New("forbidden")
	}
	statSnapshotRegistry = func(config.Paths) (store.RegistryFileIdentity, error) {
		t.Error("pure resolve stat'ed the Registry")
		return store.RegistryFileIdentity{}, errors.New("forbidden")
	}
	newResourceGraphReader = func(config.Paths) runtimeservice.Reader {
		t.Error("pure resolve built a runtime reader")
		return runtimeservice.Reader{}
	}
	t.Cleanup(func() { loadSnapshotRegistry, statSnapshotRegistry, newResourceGraphReader = load, stat, reader })
}

// The same handle and equal overlay values resolve an equal graph with no
// Registry, tmux, process host or file access; the graph is the one the graph
// rules give for that Registry and inventory, and a changed overlay changes it.
func TestPreparedGraphDeterministicOverlay(t *testing.T) {
	ctx := context.Background()
	c, paths := graphFixtureClient(t, liveProcessFixture(t))
	prepared, reuse := mustPrepare(t, c, nil)
	registry, err := resources.Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := resourcegraph.ResolveTransport(resourcegraph.TransportRequest{SocketName: "projmux"})
	if err != nil {
		t.Fatal(err)
	}
	internal := graphFixtureInventory(transport)
	processes := core.GraphProcessInventory{
		Declared: []core.GraphProcessKey{liveProcessKey},
		Observed: []core.GraphProcessObservation{{Key: liveProcessKey, Status: "live"}},
	}
	overlay := PreparedGraphOverlay{Runtime: publicGraphRuntimeInventory(internal), Processes: processes}

	// Nothing below may touch the Registry file, tmux or a process host.
	if err := os.RemoveAll(paths.StateDir); err != nil {
		t.Fatal(err)
	}
	forbidGraphIO(t)

	first, err := c.ResolveResourceGraphFromPrepared(ctx, prepared, overlay)
	if err != nil {
		t.Fatal(err)
	}
	// An equal overlay built separately (fresh slices) resolves equally.
	equal := PreparedGraphOverlay{Runtime: publicGraphRuntimeInventory(graphFixtureInventory(transport)), Processes: core.GraphProcessInventory{
		Declared: []core.GraphProcessKey{liveProcessKey},
		Observed: []core.GraphProcessObservation{{Key: liveProcessKey, Status: "live"}},
	}}
	second, err := c.ResolveResourceGraphFromPrepared(ctx, prepared, equal)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same handle and equal overlay resolved different graphs")
	}
	if first.RegistryIdentity == nil || *first.RegistryIdentity != *reuse.Identity || first.RegistryIdentity == second.RegistryIdentity {
		t.Fatalf("identity: %+v", first.RegistryIdentity)
	}

	// The graph is the core resolver's for the same Registry and inventory,
	// byte for byte, process keys included.
	internal.Processes = internalProcessInventory(processes)
	want := resourcegraph.Resolve(registry, internal)
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := json.Marshal(first.Graph)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("prepared graph differs from resourcegraph.Resolve:\n got %s\nwant %s", gotJSON, wantJSON)
	}
	if pane := graphPane(t, first, "pane-02"); pane.Status != "live" || pane.Process == nil || *pane.Process != liveProcessKey {
		t.Fatalf("live process pane: %+v", pane)
	}
	if len(first.Graph.Conflicts) == 0 || len(first.Graph.Runtime) == 0 {
		t.Fatal("fixture must carry a duplicate claim conflict and runtime rows")
	}

	// Results own their values: changing one leaves the next resolve intact.
	first.Graph.Panes[0].Pane.Metadata.Name = "changed"
	first.Graph.Conflicts[0].Detail = "changed"
	first.Graph.Runtime[0].Ref.ID = "changed"
	if again, err := c.ResolveResourceGraphFromPrepared(ctx, prepared, equal); err != nil || !reflect.DeepEqual(again, second) {
		t.Fatalf("result aliases the handle or overlay: %v", err)
	}
	if !reflect.DeepEqual(equal.Runtime, publicGraphRuntimeInventory(graphFixtureInventory(transport))) {
		t.Fatal("resolve changed the overlay")
	}

	// A changed overlay is resolved anew on the same handle.
	unobserved := equal
	unobserved.Processes = core.GraphProcessInventory{Declared: []core.GraphProcessKey{liveProcessKey}}
	unavailable := equal
	unavailable.Runtime = core.GraphRuntimeInventory{
		Transport:   overlay.Runtime.Transport,
		HostMode:    "unknown",
		Unavailable: []core.RuntimeUnavailability{{Scope: "sessions", Reason: "no server"}, {Scope: "windows", Reason: "no server"}, {Scope: "panes", Reason: "no server"}},
	}
	for name, changed := range map[string]PreparedGraphOverlay{"process-unobserved": unobserved, "transport-unavailable": unavailable} {
		graph, err := c.ResolveResourceGraphFromPrepared(ctx, prepared, changed)
		if err != nil {
			t.Fatal(err)
		}
		if reflect.DeepEqual(graph, second) {
			t.Fatalf("%s: changed overlay returned the previous graph", name)
		}
		inventory := internalGraphRuntimeInventory(changed.Runtime)
		inventory.Processes = internalProcessInventory(changed.Processes)
		raw, err := json.Marshal(resourcegraph.Resolve(registry, inventory))
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(graph.Graph)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, raw) {
			t.Fatalf("%s: graph differs from resourcegraph.Resolve", name)
		}
	}
	if pane := graphPane(t, mustResolve(t, c, prepared, unobserved), "pane-02"); pane.Status != "unknown" {
		t.Fatalf("unobserved process pane: %+v", pane)
	}
	if graph := mustResolve(t, c, prepared, unavailable); len(graph.Graph.Unavailable) != 3 || graphPane(t, graph, "pane-01").Status != "unknown" {
		t.Fatalf("unavailable transport: %+v", graph.Graph.Unavailable)
	}

	// Entry cancellation wins over everything, including an invalid handle.
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.ResolveResourceGraphFromPrepared(canceled, nil, overlay); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	cancelAfter := &snapshotCancelContext{Context: ctx}
	if _, err := c.ResolveResourceGraphFromPrepared(cancelAfter, prepared, overlay); !errors.Is(err, context.Canceled) || cancelAfter.calls != 2 {
		t.Fatalf("return cancellation: %v calls %d", err, cancelAfter.calls)
	}
}

func mustResolve(t *testing.T, c *Client, prepared *PreparedRegistrySnapshot, overlay PreparedGraphOverlay) core.ResourceGraph {
	t.Helper()
	graph, err := c.ResolveResourceGraphFromPrepared(context.Background(), prepared, overlay)
	if err != nil {
		t.Fatal(err)
	}
	return graph
}

// The prepared path equals the existing held-snapshot path for the same
// Registry, tmux observation and process evidence, and keeps degraded
// Registries and unknown UIDs as they are.
func TestPreparedGraphMatchesSnapshotOverlay(t *testing.T) {
	ctx := context.Background()
	for name, raw := range map[string][]byte{
		"fixture": graphFixtureRegistry(t),
		"duplicate-and-unknown-owner": mutatedGraphFixture(t, func(r *metadata.Registry) {
			r.Windows = append(r.Windows, r.Windows[0])
			r.Panes[0].Metadata.OwnerRef = &metadata.OwnerRef{Kind: metadata.KindWindow, UID: "window-unknown"}
		}),
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := graphFixtureClient(t, raw)
			useGraphReader(t, fixtureObserve)
			prepared, reuse := mustPrepare(t, c, nil)
			processes := core.GraphProcessInventory{Declared: []core.GraphProcessKey{{Host: "host-one", Pane: "pane-02", Generation: "gen-one"}}}
			held, err := c.ObserveResourceGraphFromSnapshot(ctx, prepared.Snapshot(), ResourceGraphOverlayRequest{SocketName: "projmux", Processes: processes, RegistryIdentity: reuse.Identity})
			if err != nil {
				t.Fatal(err)
			}
			inventory, err := c.ObserveGraphInventory(ctx, GraphObservationRequest{SocketName: "projmux"})
			if err != nil {
				t.Fatal(err)
			}
			got := mustResolve(t, c, prepared, PreparedGraphOverlay{Runtime: inventory, Processes: processes})
			if !reflect.DeepEqual(got, held) {
				t.Fatalf("prepared graph differs from ObserveResourceGraphFromSnapshot:\n got %+v\nwant %+v", got.Graph, held.Graph)
			}
		})
	}
}

// ObserveGraphInventory routes exactly as RuntimeRequest, observes the server
// once, reads no Registry or process host, and returns the observation
// unchanged. The public inventory types carry every internal field.
func TestObserveGraphInventoryRouteAndValue(t *testing.T) {
	ctx := context.Background()
	c, _ := graphFixtureClient(t, nil)
	for _, tc := range []struct {
		request GraphObservationRequest
		message string
	}{
		{GraphObservationRequest{SocketPath: "relative"}, `GraphObservationRequest.SocketPath must be absolute, got "relative"`},
		{GraphObservationRequest{SocketName: "a", SocketPath: "/b"}, "GraphObservationRequest.SocketName and SocketPath are mutually exclusive"},
	} {
		_, err := c.ObserveGraphInventory(ctx, tc.request)
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Code != CodeRuntimeRequestInvalid || err.Error() != tc.message {
			t.Fatalf("%+v: %v", tc.request, err)
		}
	}

	counts := countSnapshotReads(t, nil)
	observed := 0
	var routes []resourcegraph.Transport
	useGraphReader(t, func(reader runtimeservice.Reader) runtimeservice.Reader {
		reader.LoadRegistry = func() (metadata.Registry, error) {
			t.Error("inventory observation loaded the Registry")
			return metadata.Registry{}, nil
		}
		reader.Processes = func(context.Context, metadata.Registry) resourcegraph.ProcessInventory {
			t.Error("inventory observation observed process hosts")
			return resourcegraph.ProcessInventory{}
		}
		reader.Observe = func(_ context.Context, transport resourcegraph.Transport) resourcegraph.Inventory {
			observed++
			routes = append(routes, transport)
			return graphFixtureInventory(transport)
		}
		return reader
	})
	socket := filepath.Join(t.TempDir(), "absent.sock")
	got, err := c.ObserveGraphInventory(ctx, GraphObservationRequest{SocketPath: socket})
	if err != nil {
		t.Fatal(err)
	}
	transport, err := resourcegraph.ResolveTransport(resourcegraph.TransportRequest{SocketPath: socket})
	if err != nil {
		t.Fatal(err)
	}
	if observed != 1 || counts.loads != 0 || counts.stats != 0 || !reflect.DeepEqual(routes, []resourcegraph.Transport{transport}) {
		t.Fatalf("observation: %d %+v %+v", observed, counts, routes)
	}
	if want := graphFixtureInventory(transport); !reflect.DeepEqual(internalGraphRuntimeInventory(got), want) {
		t.Fatalf("inventory changed in conversion:\n got %+v\nwant %+v", internalGraphRuntimeInventory(got), want)
	}
	// Without a route and without Options.SocketPath, no server is selected.
	if _, err := c.ObserveGraphInventory(ctx, GraphObservationRequest{}); err != nil || routes[1].Kind != resourcegraph.TransportNone {
		t.Fatalf("empty route: %v %+v", err, routes)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.ObserveGraphInventory(canceled, GraphObservationRequest{SocketName: "projmux"}); !errors.Is(err, context.Canceled) || observed != 2 {
		t.Fatalf("entry cancellation: %v %d", err, observed)
	}

	// Every internal field is carried: the public JSON is the internal JSON.
	full := resourcegraph.Inventory{
		Transport: transport, HostMode: resourcegraph.HostModeAppOwned,
		Sessions:    []resourcegraph.Session{{ID: "$1", Name: "n", ProjectUID: "p", ProjectName: "pn", Root: "/r", Role: "control", Ephemeral: true}},
		Windows:     []resourcegraph.Window{{ID: "@1", SessionID: "$1", Index: "2", DisplayName: "d", UID: "w", MirroredName: "m", Active: true}},
		Panes:       []resourcegraph.Pane{{ID: "%1", WindowID: "@1", UID: "u", MirroredName: "m", AgentProvider: "codex", AgentLaunchAuthorship: "a", AgentSessionID: "s", AgentThreadID: "t", Title: "ti"}},
		Unavailable: []resourcegraph.Unavailability{{Scope: resourcegraph.ScopePanes, Reason: "r"}},
	}
	public := publicGraphRuntimeInventory(full)
	publicJSON, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	internalJSON, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(publicJSON, internalJSON) || !reflect.DeepEqual(internalGraphRuntimeInventory(public), full) {
		t.Fatalf("inventory conversion lost a field:\n got %s\nwant %s", publicJSON, internalJSON)
	}
	for _, pair := range [][2]any{
		{core.GraphRuntimeSession{}, resourcegraph.Session{}}, {core.GraphRuntimeWindow{}, resourcegraph.Window{}}, {core.GraphRuntimePane{}, resourcegraph.Pane{}},
	} {
		if a, b := reflect.TypeOf(pair[0]).NumField(), reflect.TypeOf(pair[1]).NumField(); a != b {
			t.Fatalf("%T has %d fields, %T has %d", pair[0], a, pair[1], b)
		}
	}
	// Nil and empty slices stay apart both ways.
	empty := resourcegraph.Inventory{Sessions: []resourcegraph.Session{}}
	if back := internalGraphRuntimeInventory(publicGraphRuntimeInventory(empty)); back.Sessions == nil || back.Windows != nil {
		t.Fatalf("nil/empty not kept: %+v", back)
	}
}
