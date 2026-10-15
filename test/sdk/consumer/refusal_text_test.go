package consumer_test

import (
	"context"
	"errors"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestExternalCreateNameRefusalText(t *testing.T) {
	c, err := projmux.NewClient(projmux.Options{StateDir: t.TempDir(), ConfigDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	want := `name: name "bad/name" contains an unsupported character "/"`
	calls := map[string]func() error{
		"window": func() error {
			_, err := c.CreateWindow(context.Background(), core.CreateWindowRequest{Project: "sample", Name: "bad/name"})
			return err
		},
		"pane": func() error {
			_, err := c.CreatePane(context.Background(), core.CreatePaneRequest{Project: "sample", Window: "main", Name: "bad/name"})
			return err
		},
		"agent": func() error {
			_, err := c.CreateAgent(context.Background(), core.CreateAgentRequest{Project: "sample", Window: "main", Provider: "claude", Name: "bad/name"})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			var refusal *projmux.Refusal
			if !errors.As(err, &refusal) || refusal.Code != projmux.CodeResourceNameInvalid || err.Error() != want {
				t.Fatalf("%v; want %s (%s)", err, want, projmux.CodeResourceNameInvalid)
			}
		})
	}
}
