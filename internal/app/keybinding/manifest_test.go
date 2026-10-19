package keybinding
import ("strings"; "testing"; "github.com/crevissepartners/projmux/internal/cli")
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

