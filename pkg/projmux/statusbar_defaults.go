package projmux

import (
	"github.com/crevissepartners/projmux/internal/services/settings"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// GetCentralStatusbarDefaults reads every central status bar visibility leaf
// the way the legacy receiver-less readers do: Source central with the stored
// value, else Source default with the built-in default (on, a usage window's
// capability default; Codex 5h is off). A path, read or parse error of the
// central file returns those defaults instead of an error; use
// GetConfig(ConfigStatusbarVisibility) for the strict read. It reads only the
// captured ConfigDir's central file, never a TUI file, and has no other
// effect. This synchronous opt-in read takes no context and observes no
// cancellation. Leaves are ordered by Key; the slice is new on every call.
func (c *Client) GetCentralStatusbarDefaults() []core.ConfigVisibilityState {
	states := settings.CentralStatusbarDefaults(c.instructions.Paths)
	out := make([]core.ConfigVisibilityState, len(states))
	for i, state := range states {
		out[i] = core.ConfigVisibilityState(state)
	}
	return out
}
