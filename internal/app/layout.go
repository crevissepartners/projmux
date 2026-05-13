package app

import (
	"fmt"
	"io"

	corelayout "github.com/crevissepartners/projmux/internal/core/layout"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
)

func layoutPresetSource(name string, preset corelayout.Preset) string {
	if preset.Normalize().Mode == corelayout.ModeFreshEachTime {
		return sessionstate.SourceFresh
	}
	return sessionstate.LayoutSource(name)
}

func printSessionStateReplayWarnings(w io.Writer, warnings []sessionstate.ReplayWarning) {
	for _, warning := range warnings {
		switch warning.Scope {
		case "pane":
			fmt.Fprintf(w, "warning: window %d pane %d cwd %s unavailable; using %s\n", warning.WindowIndex, warning.PaneIndex, warning.CWD, warning.FallbackCWD)
		case "agent":
			fmt.Fprintf(w, "warning: window %d pane %d agent replay skipped: %s\n", warning.WindowIndex, warning.PaneIndex, warning.Reason)
		default:
			fmt.Fprintf(w, "warning: session-state replay %s: %s\n", warning.Scope, warning.Reason)
		}
	}
}
