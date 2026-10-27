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

func TestExternalRegistrySnapshotCachedAndOverlay(t *testing.T) {
	root := t.TempDir()
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var read core.RegistrySnapshotRead
	read, err = c.ReadRegistrySnapshotCached(ctx, nil)
	if err != nil || read.Reused || read.RegistryIdentity != nil || read.Snapshot.ControlSessions == nil || read.Snapshot.NameReservations == nil {
		t.Fatalf("%+v %v", read, err)
	}
	again, err := c.ReadRegistrySnapshotCached(ctx, &read)
	if err != nil || again.Reused {
		t.Fatalf("first use reused: %+v %v", again, err)
	}
	var clone core.RegistrySnapshot = read.Snapshot.Clone()
	if !reflect.DeepEqual(clone, read.Snapshot) {
		t.Fatal("clone differs")
	}
	identity := core.RegistryFileIdentity{Inode: 7}
	var graph core.ResourceGraph
	graph, err = c.ObserveResourceGraphFromSnapshot(ctx, clone, projmux.ResourceGraphOverlayRequest{
		Processes:        core.GraphProcessInventory{Declared: []core.GraphProcessKey{{Host: "h", Pane: "p", Generation: "g"}}, Observed: []core.GraphProcessObservation{{Key: core.GraphProcessKey{Host: "h", Pane: "p", Generation: "g"}, Status: "live"}}},
		RegistryIdentity: &identity,
	})
	if err != nil || graph.Graph.Transport.Kind != "none" || len(graph.Graph.Unavailable) == 0 || graph.RegistryIdentity == nil || *graph.RegistryIdentity != identity {
		t.Fatalf("%+v %v", graph, err)
	}
	var refusal *projmux.Refusal
	_, err = c.ObserveResourceGraphFromSnapshot(ctx, clone, projmux.ResourceGraphOverlayRequest{SocketPath: "relative"})
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeRuntimeRequestInvalid {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("first-use effects: %v %v", entries, err)
	}
	_ = core.NameReservation{Scope: "s", Kind: core.Kind("Window"), Name: "n", UID: "u"}
}
