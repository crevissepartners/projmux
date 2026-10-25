package consumer_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestExternalResourceGraphConsumer(t *testing.T) {
	root := t.TempDir()
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	var observed core.ResourceGraph
	observed, err = c.ObserveResourceGraph(context.Background(), projmux.ResourceGraphRequest{})
	if err != nil || observed.Graph.Transport.Kind != "none" || len(observed.Graph.Unavailable) == 0 || observed.RegistryIdentity != nil {
		t.Fatalf("%+v %v", observed, err)
	}
	var identity core.RegistryFileIdentity
	identity, err = c.ReadRegistryFileIdentity(context.Background())
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeRegistryUnavailable || identity != (core.RegistryFileIdentity{}) {
		t.Fatalf("%+v %v", identity, err)
	}
	_, err = c.ObserveResourceGraph(context.Background(), projmux.ResourceGraphRequest{SocketPath: "relative"})
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeRuntimeRequestInvalid {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("first-use effects: %v %v", entries, err)
	}
	_ = core.GraphPaneNode{Runtime: &core.GraphRuntimeRef{ID: "%1"}}
	_ = core.GraphControlSessionNode{ControlSession: core.ControlSession{Spec: core.ControlSessionSpec{Session: "home"}}}
	_ = core.GraphConflict{Targets: []string{"@1", "@2"}}
}
