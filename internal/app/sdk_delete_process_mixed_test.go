package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// A tmux-anchored Window that holds a process Pane mixes two runtimes, which one
// Delete cannot stop and remove together. The refusal names both resources.
func TestSDKDeleteRefusesMixedTmuxAndProcessWindow(t *testing.T) {
	reg, agent, pane := processDeleteFixture(t, aiModeClaude)
	a, _ := reg.Agent(agent)
	window := a.Metadata.OwnerUID()
	state, _, client := renameState(t, *reg)
	path := store.PathFor(state)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Delete(context.Background(), core.DeleteRequest{Kind: core.KindWindow, Ref: "uid:" + window, Confirm: true})
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeDeleteProcessUnsupported || result.Committed {
		t.Fatalf("mixed Window: %+v %v", result, err)
	}
	if !strings.Contains(err.Error(), "uid:"+window) || !strings.Contains(err.Error(), "uid:"+pane) || !strings.Contains(err.Error(), "mixes") {
		t.Fatalf("refusal does not name the resources: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("mixed refusal changed the Registry: %v", err)
	}
}
