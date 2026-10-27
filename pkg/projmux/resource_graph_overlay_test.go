package projmux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/services/resources"
	runtimeservice "github.com/crevissepartners/projmux/internal/services/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// graphReaderCounts counts every callback of the graph reader. The tmux
// observation is the fixture inventory; the Registry loader, its stat and the
// process host observer stay the production ones so a call would be real.
type graphReaderCounts struct{ loads, stats, observes, processes int }

func countGraphReader(t *testing.T) *graphReaderCounts {
	t.Helper()
	counts := &graphReaderCounts{}
	useGraphReader(t, func(reader runtimeservice.Reader) runtimeservice.Reader {
		reader = fixtureObserve(reader)
		load, identity, observe, processes := reader.LoadRegistry, reader.Identity, reader.Observe, reader.Processes
		reader.LoadRegistry = func() (metadata.Registry, error) { counts.loads++; return load() }
		reader.Identity = func() (store.RegistryFileIdentity, error) { counts.stats++; return identity() }
		reader.Observe = func(ctx context.Context, transport resourcegraph.Transport) resourcegraph.Inventory {
			counts.observes++
			return observe(ctx, transport)
		}
		reader.Processes = func(ctx context.Context, registry metadata.Registry) resourcegraph.ProcessInventory {
			counts.processes++
			return processes(ctx, registry)
		}
		return reader
	})
	return counts
}

// The fixture's process Pane pane-02 runs as host-one/gen-one.
var overlayProcessKey = core.GraphProcessKey{Host: "host-one", Pane: "pane-02", Generation: "gen-one"}

func overlayCases() []struct {
	name   string
	input  core.GraphProcessInventory
	status string
} {
	key := overlayProcessKey
	return []struct {
		name   string
		input  core.GraphProcessInventory
		status string
	}{
		{name: "zero", status: "unknown"},
		{name: "declared-unobserved", input: core.GraphProcessInventory{Declared: []core.GraphProcessKey{key}}, status: "unknown"},
		{name: "live", input: core.GraphProcessInventory{Declared: []core.GraphProcessKey{key}, Observed: []core.GraphProcessObservation{{Key: key, Status: "live"}}}, status: "live"},
		{name: "offline", input: core.GraphProcessInventory{Declared: []core.GraphProcessKey{key}, Observed: []core.GraphProcessObservation{{Key: key, Status: "offline"}}}, status: "offline"},
		{name: "unknown-observation", input: core.GraphProcessInventory{Declared: []core.GraphProcessKey{key}, Observed: []core.GraphProcessObservation{{Key: key, Status: "unknown"}}}, status: "unknown"},
		{name: "observed-undeclared", input: core.GraphProcessInventory{Observed: []core.GraphProcessObservation{{Key: key, Status: "live"}}}, status: "unknown"},
		{name: "duplicate-declaration", input: core.GraphProcessInventory{Declared: []core.GraphProcessKey{key, key}, Observed: []core.GraphProcessObservation{{Key: key, Status: "live"}}}, status: "unknown"},
		{name: "other-generation", input: core.GraphProcessInventory{Declared: []core.GraphProcessKey{{Host: "host-one", Pane: "pane-02", Generation: "gen-retired"}}, Observed: []core.GraphProcessObservation{{Key: core.GraphProcessKey{Host: "host-one", Pane: "pane-02", Generation: "gen-retired"}, Status: "live"}}}, status: "unknown"},
	}
}

// With a supplied snapshot and inventory, the graph equals the internal
// resolver's graph for the same Registry, tmux inventory and process
// inventory, and the graph ObserveResourceGraph returns when its process
// observation yields that inventory: process Pane and Agent status, legacy
// tmux rows, Window and Project aggregates and process keys alike. The
// Registry is not read or stat'ed and no process host is observed.
func TestObserveResourceGraphFromSnapshotMatchesResolver(t *testing.T) {
	ctx := context.Background()
	c, paths := graphFixtureClient(t, graphFixtureRegistry(t))
	socket := filepath.Join(t.TempDir(), "absent.sock")
	snapshot, err := c.ReadRegistrySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := resources.Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := resourcegraph.ResolveTransport(resourcegraph.TransportRequest{SocketPath: socket})
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]bool{}
	for _, tc := range overlayCases() {
		t.Run(tc.name, func(t *testing.T) {
			internal := internalProcessInventory(tc.input)
			inventory := graphFixtureInventory(transport)
			inventory.Processes = internal.Clone()
			want := resourcegraph.Resolve(registry, inventory)
			wantJSON, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}

			counts := countGraphReader(t)
			got, err := c.ObserveResourceGraphFromSnapshot(ctx, snapshot, ResourceGraphOverlayRequest{SocketPath: socket, Processes: tc.input})
			if err != nil {
				t.Fatal(err)
			}
			if *counts != (graphReaderCounts{observes: 1}) {
				t.Fatalf("reader calls %+v, want one tmux observation only", *counts)
			}
			gotJSON, err := json.Marshal(got.Graph)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(gotJSON, wantJSON) {
				t.Fatalf("overlay graph differs from resourcegraph.Resolve:\n got %s\nwant %s", gotJSON, wantJSON)
			}
			for i, node := range want.Panes {
				if !reflect.DeepEqual(publicGraphProcessKey(node.Process), got.Graph.Panes[i].Process) {
					t.Fatalf("pane %s key %+v vs %+v", node.Pane.Metadata.UID, node.Process, got.Graph.Panes[i].Process)
				}
			}
			for i, node := range want.Agents {
				if !reflect.DeepEqual(publicGraphProcessKey(node.Process), got.Graph.Agents[i].Process) {
					t.Fatalf("agent %s key %+v vs %+v", node.Agent.Metadata.UID, node.Process, got.Graph.Agents[i].Process)
				}
			}
			// A process Pane is declared whatever the inventory holds.
			var process core.GraphPaneNode
			for _, node := range got.Graph.Panes {
				if node.Pane.Metadata.UID == "pane-02" {
					process = node
				}
			}
			if process.Status != tc.status || process.Process == nil || process.Process.Pane != "pane-02" || process.Process.Generation != "gen-one" {
				t.Fatalf("process pane: %+v key %+v", process, process.Process)
			}
			statuses[process.Status] = true
			if got.RegistryIdentity != nil {
				t.Fatal("identity invented without a caller identity")
			}

			// ObserveResourceGraph resolves the same graph when its own process
			// observation returns this inventory.
			useGraphReader(t, func(reader runtimeservice.Reader) runtimeservice.Reader {
				reader = fixtureObserve(reader)
				reader.Processes = func(context.Context, metadata.Registry) resourcegraph.ProcessInventory { return internal.Clone() }
				return reader
			})
			observed, err := c.ObserveResourceGraph(ctx, ResourceGraphRequest{SocketPath: socket})
			if err != nil {
				t.Fatal(err)
			}
			observedJSON, _ := json.Marshal(observed.Graph)
			if !bytes.Equal(observedJSON, gotJSON) || !reflect.DeepEqual(observed.Graph, got.Graph) {
				t.Fatalf("overlay graph differs from ObserveResourceGraph:\n got %s\nwant %s", gotJSON, observedJSON)
			}
		})
	}
	if !statuses["live"] || !statuses["offline"] || !statuses["unknown"] {
		t.Fatalf("cases must cover live, offline and unknown: %v", statuses)
	}
}

// Without a server, every tmux-judged row is unknown with the tmux scope
// unavailable, as ObserveResourceGraph reports it, and still no Registry or
// process host is touched.
func TestObserveResourceGraphFromSnapshotWithoutServer(t *testing.T) {
	ctx := context.Background()
	c, _ := graphFixtureClient(t, graphFixtureRegistry(t))
	snapshot, err := c.ReadRegistrySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	empty := core.GraphProcessInventory{}
	useGraphReader(t, func(reader runtimeservice.Reader) runtimeservice.Reader {
		reader.Processes = func(context.Context, metadata.Registry) resourcegraph.ProcessInventory {
			return internalProcessInventory(empty)
		}
		return reader
	})
	want, err := c.ObserveResourceGraph(ctx, ResourceGraphRequest{})
	if err != nil {
		t.Fatal(err)
	}
	counts := countGraphReader(t)
	// Keep the production tmux observer: there is no server to reach.
	useGraphReader(t, func(reader runtimeservice.Reader) runtimeservice.Reader {
		reader.Observe = runtimeservice.NewReader(c.paths).Observe
		return reader
	})
	got, err := c.ObserveResourceGraphFromSnapshot(ctx, snapshot, ResourceGraphOverlayRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if counts.loads != 0 || counts.stats != 0 || counts.processes != 0 {
		t.Fatalf("reader calls %+v", *counts)
	}
	wantJSON, _ := json.Marshal(want.Graph)
	gotJSON, _ := json.Marshal(got.Graph)
	if !bytes.Equal(gotJSON, wantJSON) || len(got.Graph.Unavailable) == 0 || got.Graph.Projects[0].Status != "unknown" {
		t.Fatalf("serverless overlay:\n got %s\nwant %s", gotJSON, wantJSON)
	}
}

// Neither input is changed, the result shares no memory with them, and the
// caller's identity is copied, not re-observed.
func TestObserveResourceGraphFromSnapshotOwnership(t *testing.T) {
	ctx := context.Background()
	c, _ := graphFixtureClient(t, graphFixtureRegistry(t))
	read, err := c.ReadRegistrySnapshotCached(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if read.RegistryIdentity == nil {
		t.Fatal("fixture read has no identity")
	}
	direct, err := c.ObserveResourceGraph(ctx, ResourceGraphRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if direct.RegistryIdentity == nil || *direct.RegistryIdentity != *read.RegistryIdentity {
		t.Fatalf("identities differ: %+v vs %+v", direct.RegistryIdentity, read.RegistryIdentity)
	}
	processes := overlayCases()[2].input
	request := ResourceGraphOverlayRequest{Processes: processes, RegistryIdentity: read.RegistryIdentity}
	snapshotBefore, _ := json.Marshal(read.Snapshot)
	requestBefore, _ := json.Marshal(request)
	counts := countGraphReader(t)
	got, err := c.ObserveResourceGraphFromSnapshot(ctx, read.Snapshot, request)
	if err != nil {
		t.Fatal(err)
	}
	if counts.loads != 0 || counts.stats != 0 || counts.processes != 0 || counts.observes != 1 {
		t.Fatalf("reader calls %+v", *counts)
	}
	if got.RegistryIdentity == nil || got.RegistryIdentity == read.RegistryIdentity || *got.RegistryIdentity != *read.RegistryIdentity {
		t.Fatalf("identity not copied: %+v", got.RegistryIdentity)
	}
	got.RegistryIdentity.Inode++
	got.Graph.Panes[0].Pane.Metadata.Name = "changed"
	got.Graph.Agents[0].Agent.Metadata.OwnerRef.UID = "changed"
	for i := range got.Graph.Panes {
		if got.Graph.Panes[i].Process != nil {
			got.Graph.Panes[i].Process.Host = "changed"
		}
	}
	if after, _ := json.Marshal(read.Snapshot); !bytes.Equal(after, snapshotBefore) {
		t.Fatal("snapshot changed")
	}
	if after, _ := json.Marshal(request); !bytes.Equal(after, requestBefore) || processes.Declared[0] != overlayProcessKey {
		t.Fatal("request changed")
	}
}

// Routing refusals and cancellation come before any observation.
func TestObserveResourceGraphFromSnapshotRefusalAndCancellation(t *testing.T) {
	c, _ := graphFixtureClient(t, graphFixtureRegistry(t))
	snapshot, err := c.ReadRegistrySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	counts := countGraphReader(t)
	var refusal *Refusal
	for _, request := range []ResourceGraphOverlayRequest{{SocketPath: "relative"}, {SocketName: "projmux", SocketPath: "/tmp/socket"}} {
		if _, err := c.ObserveResourceGraphFromSnapshot(context.Background(), snapshot, request); !errors.As(err, &refusal) || refusal.Code != CodeRuntimeRequestInvalid {
			t.Fatalf("%+v: %v", request, err)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ObserveResourceGraphFromSnapshot(canceled, snapshot, ResourceGraphOverlayRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx := &snapshotCancelContext{Context: context.Background()}
	if _, err := c.ObserveResourceGraphFromSnapshot(ctx, snapshot, ResourceGraphOverlayRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if *counts != (graphReaderCounts{}) {
		t.Fatalf("reader calls %+v", *counts)
	}
}
