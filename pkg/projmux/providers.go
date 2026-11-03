package projmux

import (
	"strings"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// ListSettingsProviders returns the providers a settings screen offers, in the
// order the CLI and picker settings list them. The caller owns the result.
func ListSettingsProviders() []core.SettingsProvider {
	visible := aiprovider.SettingsVisible()
	out := make([]core.SettingsProvider, 0, len(visible))
	for _, provider := range visible {
		out = append(out, settingsProvider(provider))
	}
	return out
}

// FindSettingsProvider returns the settings provider an id names, ignoring
// case and surrounding space the way the enabled-agents file is read. An empty
// or unknown id, or a provider settings do not offer, reports false.
func FindSettingsProvider(id string) (core.SettingsProvider, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, provider := range ListSettingsProviders() {
		if provider.ID == id {
			return provider, true
		}
	}
	return core.SettingsProvider{}, false
}

func settingsProvider(provider aiprovider.Metadata) core.SettingsProvider {
	return core.SettingsProvider{
		ID:                 string(provider.ID),
		DisplayName:        provider.DisplayName,
		SettingsVisible:    provider.SettingsVisible,
		IntegrationCommand: provider.Integrate.Command,
	}
}
