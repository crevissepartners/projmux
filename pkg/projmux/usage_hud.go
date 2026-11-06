package projmux

import (
	"github.com/crevissepartners/projmux/internal/services/usage"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// UsageHUDProviders returns the providers the usage HUD can show, in the order
// the CLI and Settings list them, each with the account windows it can project
// and their built-in default visibility. It reads nothing and needs no Client.
// The caller owns the returned slices; changing them affects no later call.
func UsageHUDProviders() []core.UsageHUDProvider {
	internal := usage.HUDProviderCapabilities()
	out := make([]core.UsageHUDProvider, len(internal))
	for i, provider := range internal {
		windows := make([]core.UsageHUDWindow, len(provider.Windows))
		for j, window := range provider.Windows {
			windows[j] = core.UsageHUDWindow{
				Window:            core.UsageWindow(window.Window),
				Key:               window.Key,
				Label:             window.Label,
				DefaultVisibility: core.StatusbarVisibility(window.DefaultVisibility),
			}
		}
		out[i] = core.UsageHUDProvider{
			ID:          string(provider.ID),
			Model:       provider.Model,
			DisplayName: provider.DisplayName,
			Windows:     windows,
		}
	}
	return out
}

// UsageModelLabel maps a usage model name to the capitalized label the HUD
// shows. Matching of claude and codex ignores case, an empty name is "?", and
// any other name keeps its first rune upper case and the rest lower case. It
// reads nothing and never fails.
func UsageModelLabel(model string) string { return usage.ModelDisplayLabel(model) }
