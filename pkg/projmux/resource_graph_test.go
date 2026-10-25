package projmux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/services/resources"
	runtimeservice "github.com/crevissepartners/projmux/internal/services/runtime"
)

// graphFixtureRegistry is a Registry with a Project, a ControlSession, a shell
// Pane and a process-hosted Agent Pane whose host is absent (unknown).
func graphFixtureRegistry(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../internal/core/metadata/testdata/registry-v5-process.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var reg metadata.Registry
	if err := json.Unmarshal(raw, &reg); err != nil {
		t.Fatal(err)
	}
	reg.ControlSessions = append(reg.ControlSessions, metadata.ControlSession{
		APIVersion: reg.APIVersion, Kind: metadata.KindControlSession,
		Metadata: metadata.ObjectMeta{UID: "ctl-01", Name: "home"}, Spec: metadata.ControlSessionSpec{Session: "home"},
	})
	reg.NameReservations = append(reg.NameReservations, metadata.NameReservation{Kind: metadata.KindControlSession, Name: "home", UID: "ctl-01"})
	out, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// graphFixtureInventory claims window-01 from two live windows (a duplicate
// claim), binds the Project and shell Pane, and carries a control session and
// an unattributed session.
func graphFixtureInventory(transport resourcegraph.Transport) resourcegraph.Inventory {
	return resourcegraph.Inventory{
		Transport: transport,
		HostMode:  resourcegraph.HostModeAppOwned,
		Sessions: []resourcegraph.Session{
			{ID: "$1", Name: "alpha", ProjectUID: "project-01", ProjectName: "alpha", Root: "/srv/alpha"},
			{ID: "$2", Name: "home", Role: resourcegraph.ControlSessionRole},
			{ID: "$3", Name: "scratch"},
		},
		Windows: []resourcegraph.Window{
			{ID: "@1", SessionID: "$1", Index: "0", UID: "window-01", Active: true},
			{ID: "@2", SessionID: "$1", Index: "1", UID: "window-01"},
			{ID: "@3", SessionID: "$2", Index: "0"},
		},
		Panes: []resourcegraph.Pane{
			{ID: "%1", WindowID: "@1", UID: "pane-01"},
			{ID: "%3", WindowID: "@3"},
		},
	}
}

func graphFixtureClient(t *testing.T, raw []byte) (*Client, config.Paths) {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if raw != nil {
		path := store.PathFor(state)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	c, err := NewClient(Options{ConfigDir: filepath.Join(root, "config"), StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	return c, c.paths
}

// useGraphReader replaces the graph reader for one test. The fixture tmux
// inventory replaces only tmux observation; Registry, identity and process
// observation stay the production ones unless wrap changes them.
func useGraphReader(t *testing.T, wrap func(runtimeservice.Reader) runtimeservice.Reader) {
	t.Helper()
	previous := newResourceGraphReader
	newResourceGraphReader = func(paths config.Paths) runtimeservice.Reader {
		return wrap(previous(paths))
	}
	t.Cleanup(func() { newResourceGraphReader = previous })
}

func fixtureObserve(reader runtimeservice.Reader) runtimeservice.Reader {
	reader.Observe = func(_ context.Context, transport resourcegraph.Transport) resourcegraph.Inventory {
		return graphFixtureInventory(transport)
	}
	return reader
}

func TestObserveResourceGraphMatchesCoreGraph(t *testing.T) {
	ctx := context.Background()
	c, paths := graphFixtureClient(t, graphFixtureRegistry(t))
	socket := filepath.Join(t.TempDir(), "absent.sock")
	useGraphReader(t, fixtureObserve)
	got, err := c.ObserveResourceGraph(ctx, ResourceGraphRequest{SocketPath: socket})
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
	inventory := graphFixtureInventory(transport)
	inventory.Processes = runtimeservice.ObserveRegistryProcesses(ctx, registry, store.PathFor(paths.StateDir), nil)
	want, err := json.Marshal(resourcegraph.Resolve(registry, inventory))
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := json.Marshal(got.Graph)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJSON, want) {
		t.Fatalf("public graph JSON differs from resourcegraph.Resolve:\n got %s\nwant %s", gotJSON, want)
	}
	// The fixture must exercise every row family the contract names.
	// A registered ControlSession binds its exact control-role session, so both
	// the Registry row and that runtime session are managed.
	if len(got.Graph.ControlSessions) != 1 || got.Graph.ControlSessions[0].Class != "managed" || got.Graph.ControlSessions[0].Status != "live" {
		t.Fatalf("control session: %+v", got.Graph.ControlSessions)
	}
	control := false
	for _, node := range got.Graph.Runtime {
		control = control || (node.Ref.ID == "$2" && node.Class == "managed" && node.ResourceUID == "ctl-01")
	}
	if !control {
		t.Fatalf("control session not bound: %+v", got.Graph.Runtime)
	}
	statuses := map[string]string{}
	for _, pane := range got.Graph.Panes {
		statuses[pane.Pane.Metadata.UID] = pane.Status
	}
	if statuses["pane-01"] == "" || statuses["pane-02"] != "unknown" {
		t.Fatalf("pane statuses (process pane must be unknown): %v", statuses)
	}
	conflict := false
	for _, c := range got.Graph.Conflicts {
		conflict = conflict || (c.UID == "window-01" && c.Reason == "duplicate-runtime-claim" && len(c.Targets) == 2)
	}
	if !conflict {
		t.Fatalf("duplicate claim conflict missing: %+v", got.Graph.Conflicts)
	}
	if got.RegistryIdentity == nil {
		t.Fatal("unchanged Registry read has no identity")
	}

}

// Without a server, the same rows remain and are reported unknown, as the
// CLI's graph reader reports them.
func TestObserveResourceGraphWithoutServerMatchesCoreReader(t *testing.T) {
	ctx := context.Background()
	c, paths := graphFixtureClient(t, graphFixtureRegistry(t))
	got, err := c.ObserveResourceGraph(ctx, ResourceGraphRequest{})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := runtimeservice.NewGraphReader(paths).Resolve(ctx, resourcegraph.Transport{Kind: resourcegraph.TransportNone, Source: resourcegraph.TransportSourceNone})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(graph)
	gotJSON, _ := json.Marshal(got.Graph)
	if !bytes.Equal(gotJSON, want) || len(got.Graph.Unavailable) == 0 || got.Graph.Projects[0].Status != "unknown" {
		t.Fatalf("serverless graph:\n got %s\nwant %s", gotJSON, want)
	}
}

func TestObserveResourceGraphReadsRegistryOnce(t *testing.T) {
	c, paths := graphFixtureClient(t, graphFixtureRegistry(t))
	var loads, stats, observes, processes int
	var loaded metadata.Registry
	var atLoad store.RegistryFileIdentity
	useGraphReader(t, func(reader runtimeservice.Reader) runtimeservice.Reader {
		reader = fixtureObserve(reader)
		load, identity, observe, observeProcesses := reader.LoadRegistry, reader.Identity, reader.Observe, reader.Processes
		reader.LoadRegistry = func() (metadata.Registry, error) {
			loads++
			var err error
			if atLoad, err = store.NewDefaultStore(paths).RegistryFileIdentity(); err != nil {
				t.Fatal(err)
			}
			loaded, err = load()
			return loaded, err
		}
		reader.Identity = func() (store.RegistryFileIdentity, error) { stats++; return identity() }
		reader.Observe = func(ctx context.Context, transport resourcegraph.Transport) resourcegraph.Inventory {
			observes++
			return observe(ctx, transport)
		}
		reader.Processes = func(ctx context.Context, registry metadata.Registry) resourcegraph.ProcessInventory {
			processes++
			if !registryRowsEqual(registry, loaded) {
				t.Error("process observation read a different Registry value")
			}
			return observeProcesses(ctx, registry)
		}
		return reader
	})
	got, err := c.ObserveResourceGraph(context.Background(), ResourceGraphRequest{SocketPath: filepath.Join(t.TempDir(), "absent.sock")})
	if err != nil {
		t.Fatal(err)
	}
	if loads != 1 || stats != 2 || observes != 1 || processes != 1 {
		t.Fatalf("loads=%d stats=%d observes=%d processes=%d", loads, stats, observes, processes)
	}
	if got.RegistryIdentity == nil || *got.RegistryIdentity != publicRegistryFileIdentity(atLoad) {
		t.Fatalf("identity %+v is not the loaded file's %+v", got.RegistryIdentity, atLoad)
	}
	if len(got.Graph.Agents) != len(loaded.Agents) || got.Graph.Agents[0].Agent.Metadata.UID != loaded.Agents[0].Metadata.UID ||
		len(got.Graph.Panes) != len(loaded.Panes) || len(got.Graph.Projects) != len(loaded.Projects) {
		t.Fatalf("graph rows are not the loaded Registry's rows: %+v", got.Graph)
	}
}

func registryRowsEqual(a, b metadata.Registry) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func TestObserveResourceGraphOmitsIdentityWhenRegistryChanges(t *testing.T) {
	raw := graphFixtureRegistry(t)
	c, paths := graphFixtureClient(t, raw)
	path := store.PathFor(paths.StateDir)
	useGraphReader(t, func(reader runtimeservice.Reader) runtimeservice.Reader {
		load := reader.LoadRegistry
		reader.LoadRegistry = func() (metadata.Registry, error) {
			registry, err := load()
			// Replace the file atomically between the two stats, as writers do.
			next := path + ".next"
			if err := os.WriteFile(next, raw, 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(next, path); err != nil {
				t.Fatal(err)
			}
			return registry, err
		}
		return reader
	})
	got, err := c.ObserveResourceGraph(context.Background(), ResourceGraphRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got.RegistryIdentity != nil {
		t.Fatalf("identity kept across a Registry replacement: %+v", got.RegistryIdentity)
	}
	if len(got.Graph.Projects) != 1 || len(got.Graph.Panes) != 2 {
		t.Fatalf("graph not returned with the read rows: %+v", got.Graph)
	}

	// A Registry file that cannot be stat'ed has no identity, and the graph is
	// still the (empty) read.
	empty, _ := graphFixtureClient(t, nil)
	got, err = empty.ObserveResourceGraph(context.Background(), ResourceGraphRequest{})
	if err != nil || got.RegistryIdentity != nil {
		t.Fatalf("missing Registry: %+v %v", got.RegistryIdentity, err)
	}
}

func TestReadRegistryFileIdentityMatchesGraphIdentity(t *testing.T) {
	ctx := context.Background()
	c, paths := graphFixtureClient(t, graphFixtureRegistry(t))
	got, err := c.ObserveResourceGraph(ctx, ResourceGraphRequest{})
	if err != nil || got.RegistryIdentity == nil {
		t.Fatalf("%+v %v", got.RegistryIdentity, err)
	}
	identity, err := c.ReadRegistryFileIdentity(ctx)
	if err != nil || identity != *got.RegistryIdentity {
		t.Fatalf("stat identity %+v differs from graph identity %+v: %v", identity, *got.RegistryIdentity, err)
	}
	internal, err := store.NewDefaultStore(paths).RegistryFileIdentity()
	if err != nil || publicRegistryFileIdentity(internal) != identity {
		t.Fatalf("%+v %+v %v", internal, identity, err)
	}
	if err := os.Remove(store.PathFor(paths.StateDir)); err != nil {
		t.Fatal(err)
	}
	_, err = c.ReadRegistryFileIdentity(ctx)
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeRegistryUnavailable {
		t.Fatalf("missing Registry identity: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.ReadRegistryFileIdentity(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := c.ObserveResourceGraph(canceled, ResourceGraphRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestObserveResourceGraphRequestValidation(t *testing.T) {
	c, _ := graphFixtureClient(t, nil)
	for _, tc := range []struct {
		request ResourceGraphRequest
		message string
	}{
		{ResourceGraphRequest{SocketPath: "relative"}, `ResourceGraphRequest.SocketPath must be absolute, got "relative"`},
		{ResourceGraphRequest{SocketName: "a", SocketPath: "/b"}, "ResourceGraphRequest.SocketName and SocketPath are mutually exclusive"},
	} {
		_, err := c.ObserveResourceGraph(context.Background(), tc.request)
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Code != CodeRuntimeRequestInvalid || err.Error() != tc.message {
			t.Fatalf("%+v: %v", tc.request, err)
		}
	}
}
