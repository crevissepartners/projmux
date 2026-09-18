package usagecmd

import (
	"strings"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/core/usage"
)

// HUDSnapshotsWithVisibility is HUDSnapshots under a visibility the caller
// supplies instead of the one saved in the TUI Settings files. visible is
// asked once per provider with window "" and once per window with the
// window's key, both as HUDProviderCapabilities names them. A provider
// answered off hides all of its windows, exactly as HUDSnapshots does.
//
// The web status bar uses it to render with the web settings layer; the TUI
// and `internal status usage` keep HUDSnapshots and never see that layer.
func HUDSnapshotsWithVisibility(snaps []usage.Snapshot, visible func(provider aiprovider.ID, window string) bool) []usage.Snapshot {
	prefs := hudVisibilityPreferences{
		providers: make(map[string]bool),
		windows:   make(map[string]map[usage.Window]bool),
	}
	for _, provider := range HUDProviderCapabilities() {
		model := strings.ToLower(strings.TrimSpace(provider.Model))
		prefs.providers[model] = visible(provider.ID, "")
		prefs.windows[model] = make(map[usage.Window]bool, len(provider.Windows))
		for _, window := range provider.Windows {
			prefs.windows[model][window.Window] = visible(provider.ID, window.Key)
		}
	}
	return hudSnapshotsUnder(snaps, prefs)
}
