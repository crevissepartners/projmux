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

// pane programs emit OSC 8 hyperlinks, but tmux forwards them to the outer
// terminal only when the hyperlinks terminal feature is declared. Both
// generated configs must add it right after the existing RGB line, with the
// same guard, and leave the RGB line untouched.
func TestGeneratedConfigsEnableHyperlinksTerminalFeatureAfterRGB(t *testing.T) {
	t.Parallel()

	const rgb = `if-shell 'tmux -S #{q:socket_path} show-options -sv terminal-features | grep -Fxq "xterm*:RGB"' '' 'set -as terminal-features ",xterm*:RGB"'`
	const hyperlinks = `if-shell 'tmux -S #{q:socket_path} show-options -sv terminal-features | grep -Fxq "xterm*:hyperlinks"' '' 'set -as terminal-features ",xterm*:hyperlinks"'`
	for name, output := range map[string]string{
		"standalone": TmuxStandaloneConfig("/tmp/proj mux/bin/projmux", config.StatusbarDecorationOff),
		"app":        TmuxAppConfig("/tmp/proj mux/bin/projmux", "/bin/sh", config.StatusbarDecorationOff),
	} {
		lines := strings.Split(output, "\n")
		if count := strings.Count(output, rgb); count != 1 {
			t.Fatalf("%s config has the RGB line %d times, want once:\n%s", name, count, output)
		}
		if count := strings.Count(output, hyperlinks); count != 1 {
			t.Fatalf("%s config has the hyperlinks line %d times, want once:\n%s", name, count, output)
		}
		at := slices.Index(lines, rgb)
		if at < 0 || at+1 >= len(lines) || lines[at+1] != hyperlinks {
			t.Fatalf("%s config does not place the hyperlinks line directly after the RGB line:\n%s", name, output)
		}
	}
}
