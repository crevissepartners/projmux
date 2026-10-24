package tmuxconfig

import (
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
)

// tmux runs pane-focus-in and pane-focus-out only while the server option
// focus-events is on, and it defaults to off. The generated configs install
// those hooks to consume the response-complete badge, so both must turn the
// option on before the hooks, or a window switch never clears the badge.
func TestGeneratedConfigsTurnOnFocusEventsBeforeFocusHooks(t *testing.T) {
	t.Parallel()

	const want = "set -s focus-events on"
	for name, output := range map[string]string{
		"standalone": TmuxStandaloneConfig("/tmp/proj mux/bin/projmux", config.StatusbarDecorationOff),
		"app":        TmuxAppConfig("/tmp/proj mux/bin/projmux", "/bin/sh", config.StatusbarDecorationOff),
	} {
		lines := strings.Split(output, "\n")
		focusEvents := slices.Index(lines, want)
		if focusEvents < 0 {
			t.Fatalf("%s config has no %q line:\n%s", name, want, output)
		}
		if count := strings.Count(output, "focus-events"); count != 1 {
			t.Fatalf("%s config sets focus-events %d times, want once", name, count)
		}
		for _, hook := range []string{"set-hook -g pane-focus-in ", "set-hook -g pane-focus-out "} {
			at := slices.IndexFunc(lines, func(line string) bool { return strings.HasPrefix(line, hook) })
			if at < 0 {
				t.Fatalf("%s config has no %q hook", name, hook)
			}
			if at < focusEvents {
				t.Fatalf("%s config installs %q at line %d before %q at line %d", name, hook, at, want, focusEvents)
			}
		}
	}
}
