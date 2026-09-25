package pickercompat_test

import (
	"testing"

	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/ui/picker"
	"github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func TestPickerCommandFromBindingRoundTripsSessionPopupCycleCommand(t *testing.T) {
	t.Parallel()

	for _, binaryPath := range []string{"/tmp/a)b/projmux", "/tmp/it's a (dir)/projmux"} {
		for _, subcommand := range []string{"cycle-window", "cycle-pane"} {
			cmd, err := inttmux.BuildSessionPopupCycleCommand(binaryPath, subcommand, "next")
			if err != nil {
				t.Fatalf("BuildSessionPopupCycleCommand(%q, %q): %v", binaryPath, subcommand, err)
			}

			if got := pickercompat.PickerCommandFromBinding("execute-silent(" + cmd + ")+refresh-preview"); got != cmd {
				t.Fatalf("PickerCommandFromBinding() = %q, want %q", got, cmd)
			}

			options := pickercompat.PickerOptions(pickercompat.Options{
				Bindings: []string{"left:execute-silent(" + cmd + ")+refresh-preview"},
			})
			if len(options.Actions) != 1 {
				t.Fatalf("Actions = %#v, want one action", options.Actions)
			}
			action := options.Actions[0]
			if action.Key != "left" || action.Intent != picker.ActionCustom || !action.Refresh {
				t.Fatalf("action = %#v, want left custom refresh action", action)
			}
			if action.Command != cmd {
				t.Fatalf("action.Command = %q, want %q", action.Command, cmd)
			}
		}
	}
}
