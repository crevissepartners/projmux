package projmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestCreateProcessUnsupportedBeforeEffects(t *testing.T) {
	root := t.TempDir()
	c, err := NewClient(Options{HomeDir: root, ConfigDir: filepath.Join(root, "settings"), StateDir: filepath.Join(root, "state"), ProjmuxExecutable: "/missing"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []core.CreateAgentRequest{
		{Host: "process"},
		{Host: "process", Detach: true, Pane: "uid:anchor"},
		{Host: "process", Detach: true, Placement: "down"},
		{Host: "process", Detach: true, CwdFrom: "pane"},
		{Host: "process", Detach: true, Provider: "antigravity"},
		{Host: "process", Detach: true, InteractiveOnly: true},
		{Host: "process", Detach: true, DialogueReplyOnly: true},
		{Detach: true},
		{Host: "unknown"},
	}
	for _, req := range cases {
		_, err = c.CreateAgent(context.Background(), req)
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Code != CodeCreateRequestInvalid {
			t.Fatalf("%+v: %T %v", req, err, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.CreateAgent(ctx, core.CreateAgentRequest{Host: "process", Detach: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, dir := range []string{"settings", "state"} {
		if _, err := os.Stat(filepath.Join(root, dir)); !os.IsNotExist(err) {
			t.Fatal("preflight created root", err)
		}
	}
}
