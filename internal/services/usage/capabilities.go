package usage

import (
	"strings"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/usage"
)

// HUDWindowCapability is one canonical account window the ambient usage HUD
// can project for a provider. It is deliberately closed over the explicit HUD
// projection below; opaque quota buckets never become Settings rows.
type HUDWindowCapability struct {
	Window            usage.Window
	Key               string
	Label             string
	DefaultVisibility config.StatusbarVisibility
}

// HUDProviderCapability is the Settings/render contract for one provider.
// Providers are returned in aiprovider.UsageSupported declared order.
type HUDProviderCapability struct {
	ID          aiprovider.ID
	Model       string
	DisplayName string
	Windows     []HUDWindowCapability
}

// HUDProviderCapabilities is the single explicit capability seam shared by
// ambient projection, Settings and the central status bar defaults. Provider
// inventory/order comes from the usage-supported catalog; window inventory is
// intentionally enumerated here because it cannot be inferred from opaque
// account snapshots. Every call returns new slices.
func HUDProviderCapabilities() []HUDProviderCapability {
	providers := aiprovider.UsageSupported()
	out := make([]HUDProviderCapability, 0, len(providers))
	for _, provider := range providers {
		capability := HUDProviderCapability{
			ID:          provider.ID,
			Model:       provider.UsageModel,
			DisplayName: provider.DisplayName,
		}
		switch provider.ID {
		case aiprovider.Claude:
			capability.Windows = []HUDWindowCapability{
				{Window: usage.Window5h, Key: "5h", Label: "5h", DefaultVisibility: config.StatusbarVisibilityOn},
				{Window: usage.WindowWeekly, Key: "weekly", Label: "Weekly", DefaultVisibility: config.StatusbarVisibilityOn},
			}
		case aiprovider.Codex:
			capability.Windows = []HUDWindowCapability{
				{Window: usage.Window5h, Key: "5h", Label: "5h", DefaultVisibility: config.StatusbarVisibilityOff},
				{Window: usage.WindowWeekly, Key: "weekly", Label: "Weekly", DefaultVisibility: config.StatusbarVisibilityOn},
			}
		}
		out = append(out, capability)
	}
	return out
}

// ModelDisplayLabel maps a model name to its capitalized HUD label.
func ModelDisplayLabel(model string) string {
	switch strings.ToLower(model) {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	default:
		if model == "" {
			return "?"
		}
		// Title-case the first rune; rest stays lowercase.
		rs := []rune(strings.ToLower(model))
		rs[0] = []rune(strings.ToUpper(string(rs[0])))[0]
		return string(rs)
	}
}
