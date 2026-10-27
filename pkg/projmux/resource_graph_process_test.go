package projmux

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/services/resources"
	runtimeservice "github.com/crevissepartners/projmux/internal/services/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// The graph's process keys are copied field by field onto the public Pane and
// Agent nodes; the JSON keeps omitting them.
func TestObserveResourceGraphCopiesProcessKeys(t *testing.T) {
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
	want := resourcegraph.Resolve(registry, inventory)
	if len(got.Graph.Panes) != len(want.Panes) || len(got.Graph.Agents) != len(want.Agents) {
		t.Fatalf("node counts differ: %d/%d panes, %d/%d agents", len(got.Graph.Panes), len(want.Panes), len(got.Graph.Agents), len(want.Agents))
	}
	same := func(a *resourcegraph.ProcessKey, b *core.GraphProcessKey) bool {
		if a == nil || b == nil {
			return a == nil && b == nil
		}
		return a.Host == b.Host && a.Pane == b.Pane && a.Generation == b.Generation && a.Valid() == b.Valid()
	}
	processPane := false
	for i, node := range want.Panes {
		public := got.Graph.Panes[i]
		if public.Pane.Metadata.UID != node.Pane.Metadata.UID || !same(node.Process, public.Process) {
			t.Fatalf("pane %s process key: %+v vs %+v", node.Pane.Metadata.UID, node.Process, public.Process)
		}
		if node.Pane.Metadata.UID == "pane-01" && public.Process != nil {
			t.Fatalf("tmux pane carries a process key: %+v", public.Process)
		}
		if node.Pane.Metadata.UID == "pane-02" {
			processPane = public.Process != nil && public.Process.Pane == "pane-02" && public.Process.Generation == "gen-one"
		}
	}
	if !processPane {
		t.Fatalf("process pane key missing: %+v", got.Graph.Panes)
	}
	processAgent := false
	for i, node := range want.Agents {
		public := got.Graph.Agents[i]
		if public.Agent.Metadata.UID != node.Agent.Metadata.UID || !same(node.Process, public.Process) {
			t.Fatalf("agent %s process key: %+v vs %+v", node.Agent.Metadata.UID, node.Process, public.Process)
		}
		processAgent = processAgent || (node.Agent.Metadata.UID == "agent-01" && public.Process != nil && public.Process.Pane == "pane-02")
	}
	if !processAgent {
		t.Fatalf("process agent key missing: %+v", got.Graph.Agents)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := json.Marshal(got.Graph)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJSON, wantJSON) || bytes.Contains(gotJSON, []byte(`"Generation"`)) || bytes.Contains(gotJSON, []byte(`"process":{"Host"`)) {
		t.Fatalf("graph JSON changed:\n got %s\nwant %s", gotJSON, wantJSON)
	}
}

// Copies own their keys and keep the internal Valid rule.
func TestPublicResourceGraphProcessKeyCopy(t *testing.T) {
	keys := []resourcegraph.ProcessKey{
		{Host: "host", Pane: "pane", Generation: "gen"},
		{Pane: "pane", Generation: "gen"},
		{Host: "host", Generation: "gen"},
		{Host: "host", Pane: "pane"},
		{},
	}
	graph := resourcegraph.Graph{}
	for i := range keys {
		graph.Panes = append(graph.Panes, resourcegraph.PaneNode{Process: &keys[i]})
		graph.Agents = append(graph.Agents, resourcegraph.AgentNode{Process: &keys[i]})
	}
	graph.Panes = append(graph.Panes, resourcegraph.PaneNode{})
	graph.Agents = append(graph.Agents, resourcegraph.AgentNode{})
	out, err := publicResourceGraph(graph, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, key := range keys {
		for _, public := range []*core.GraphProcessKey{out.Graph.Panes[i].Process, out.Graph.Agents[i].Process} {
			if public == nil || *public != (core.GraphProcessKey{Host: key.Host, Pane: key.Pane, Generation: key.Generation}) || public.Valid() != key.Valid() {
				t.Fatalf("key %d: %+v vs %+v", i, public, key)
			}
		}
		if out.Graph.Panes[i].Process == out.Graph.Agents[i].Process {
			t.Fatalf("key %d shares one public pointer between nodes", i)
		}
	}
	if out.Graph.Panes[len(keys)].Process != nil || out.Graph.Agents[len(keys)].Process != nil {
		t.Fatal("absent key became non-nil")
	}
	out.Graph.Panes[0].Process.Host = "changed"
	out.Graph.Agents[0].Process.Pane = "changed"
	if keys[0] != (resourcegraph.ProcessKey{Host: "host", Pane: "pane", Generation: "gen"}) {
		t.Fatal("public key aliases the internal key")
	}
}
