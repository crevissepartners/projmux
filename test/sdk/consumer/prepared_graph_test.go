package consumer_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestExternalPreparedRegistryGraphAndContext(t *testing.T) {
	root := t.TempDir()
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var prepared *projmux.PreparedRegistrySnapshot
	var reuse projmux.SnapshotReuse
	prepared, reuse, err = c.PrepareRegistrySnapshot(ctx, nil)
	if err != nil || prepared == nil || reuse.Reused || reuse.Identity != nil {
		t.Fatalf("%+v %v", reuse, err)
	}
	var snapshot core.RegistrySnapshot = prepared.Snapshot()
	if snapshot.Projects == nil || prepared.IsVirtualWindow("window") {
		t.Fatalf("%+v", snapshot)
	}
	again, againReuse, err := c.PrepareRegistrySnapshot(ctx, prepared)
	if err != nil || again == prepared || againReuse.Reused {
		t.Fatalf("first use reused: %+v %v", againReuse, err)
	}

	var inventory core.GraphRuntimeInventory
	inventory, err = c.ObserveGraphInventory(ctx, projmux.GraphObservationRequest{})
	if err != nil || inventory.Transport.Kind != "none" || len(inventory.Unavailable) == 0 {
		t.Fatalf("%+v %v", inventory, err)
	}
	overlay := projmux.PreparedGraphOverlay{
		Runtime:   inventory,
		Processes: core.GraphProcessInventory{Declared: []core.GraphProcessKey{{Host: "h", Pane: "p", Generation: "g"}}},
	}
	var graph core.ResourceGraph
	graph, err = c.ResolveResourceGraphFromPrepared(ctx, prepared, overlay)
	if err != nil || graph.Graph.Transport.Kind != "none" {
		t.Fatalf("%+v %v", graph, err)
	}
	if same, err := c.ResolveResourceGraphFromPrepared(ctx, prepared, overlay); err != nil || !reflect.DeepEqual(same, graph) {
		t.Fatalf("not deterministic: %v", err)
	}
	var refusal *projmux.Refusal
	if _, err := c.ResolveResourceGraphFromPrepared(ctx, nil, overlay); !errors.As(err, &refusal) || refusal.Code != projmux.CodePreparedSnapshotInvalid {
		t.Fatal(err)
	}
	if _, err := c.ObserveGraphInventory(ctx, projmux.GraphObservationRequest{SocketPath: "relative"}); !errors.As(err, &refusal) || refusal.Code != projmux.CodeRuntimeRequestInvalid {
		t.Fatal(err)
	}

	var projector projmux.ContextProjector = projmux.NewResourceContextProjector(graph)
	var got core.ResourceContext = projector.For(core.KindWindow, "unknown")
	if got != (core.ResourceContext{}) {
		t.Fatalf("%+v", got)
	}
	_ = core.ResourceContext{Value: "v", Source: core.ContextSourceLiveWindowName, Observed: true}
	_ = core.GraphRuntimeInventory{
		Sessions: []core.GraphRuntimeSession{{ID: "$1"}}, Windows: []core.GraphRuntimeWindow{{ID: "@1"}}, Panes: []core.GraphRuntimePane{{ID: "%1"}},
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("first-use effects: %v %v", entries, err)
	}
}
