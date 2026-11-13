package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// A tmux-anchored Window that holds a process Pane is no longer refused as a
// mix of runtimes: the executor stops the provider before it deletes. Without a
// tmux transport the live half cannot run, so the request is refused for the
// missing transport and nothing is changed.
func TestSDKDeleteWindowHoldingProcessPaneNeedsTransport(t *testing.T) {
	reg, agent, _ := processDeleteFixture(t, aiModeClaude)
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
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeDeleteTransportRequired || result.Committed {
		t.Fatalf("Window without transport: %+v %v", result, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("refusal changed the Registry: %v", err)
	}
}
