package keybinding

import (
	"github.com/crevissepartners/projmux/internal/cli"
	"strings"
	"testing"
)

func TestGeneratedSplitBindingsNeverInvokeRetiredAIRoot(t *testing.T) {
	t.Parallel()

	for _, action := range DefaultKeyBindingCatalog() {
		if !strings.HasPrefix(action.ID, "ai-split-") || action.Kind != keyBindingActionCommand {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(action.TmuxBody), "ai ") {
			t.Errorf("split action %q invokes retired route: %q", action.ID, action.TmuxBody)
		}
		if _, _, ok := cli.Resolve(strings.Fields(action.TmuxBody)); !ok {
			t.Errorf("split action %q does not resolve through the command manifest: %q", action.ID, action.TmuxBody)
		}
	}
}

func TestKeyBindingSurfaceLabelFollowsReplacedNavigationOrder(t *testing.T) {
	original := KeyBindingSurfaceOrder
	if len(original) == 0 {
		t.Fatal("surface navigation order is empty")
	}
	t.Cleanup(func() { KeyBindingSurfaceOrder = original })

	// Keep this test serial: lookup must follow the app-owned mutable variable.
	KeyBindingSurfaceOrder = []struct {
		ID    string
		Label string
	}{
		{ID: "rebound-surface", Label: "Rebound surface"},
	}
	if label, ok := KeyBindingSurfaceLabel("rebound-surface"); !ok || label != "Rebound surface" {
		t.Fatalf("surface label after order replacement = (%q, %v), want (%q, true)", label, ok, "Rebound surface")
	}
	if label, ok := KeyBindingSurfaceLabel(original[0].ID); ok {
		t.Fatalf("surface label retained the prior order entry (%q, %v)", label, ok)
	}
}
