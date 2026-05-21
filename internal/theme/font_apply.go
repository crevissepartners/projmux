package theme

import (
	"strconv"
	"strings"
)

// FontApplyStatus describes whether projmux changed the terminal font.
// Phase 3 intentionally has no success state: desired font values can be
// stored and resolved, but tmux/ANSI cannot apply them universally.
type FontApplyStatus string

const (
	FontApplyNotRequested FontApplyStatus = "not requested"
	FontApplyNotApplied   FontApplyStatus = "not applied"
)

// FontCapability describes what the current terminal path can do. An empty
// capability means no terminal-specific adapter is available.
type FontCapability struct {
	Adapter        string
	SupportsFamily bool
	SupportsSize   bool
	Reason         string
}

// FontApplication is the read-only status shown by Settings/doctor surfaces.
type FontApplication struct {
	Family  string          `json:"family,omitempty"`
	Size    int             `json:"size,omitempty"`
	Status  FontApplyStatus `json:"status"`
	Adapter string          `json:"adapter,omitempty"`
	Reason  string          `json:"reason,omitempty"`
}

// NoFontCapability returns the default Phase 3 terminal capability: there is
// no adapter that can apply font family or size through tmux/ANSI rendering.
func NoFontCapability() FontCapability {
	return FontCapability{
		Reason: "tmux/ANSI cannot set terminal font family or size; no supported font adapter is active",
	}
}

// EvaluateFontApplication reports the desired font values and whether they
// were applied. Unsupported or missing-adapter environments are always reported
// as not applied, never as success.
func EvaluateFontApplication(effective EffectiveTheme, capability FontCapability) FontApplication {
	result := FontApplication{
		Family: strings.TrimSpace(effective.FontFamily.Value),
		Size:   effective.FontSize.Value,
	}
	if result.Family == "" && result.Size == 0 {
		result.Status = FontApplyNotRequested
		result.Reason = "no desired font_family or font_size is configured"
		return result
	}

	result.Status = FontApplyNotApplied
	result.Adapter = strings.TrimSpace(capability.Adapter)
	if result.Adapter == "" || fontCapabilityMissingDesired(result, capability) {
		result.Reason = strings.TrimSpace(capability.Reason)
		if result.Reason == "" {
			result.Reason = NoFontCapability().Reason
		}
		return result
	}

	result.Reason = "font capability is present, but applying terminal profile changes is outside this phase"
	return result
}

func fontCapabilityMissingDesired(desired FontApplication, capability FontCapability) bool {
	return (desired.Family != "" && !capability.SupportsFamily) ||
		(desired.Size != 0 && !capability.SupportsSize)
}

// Desired returns a compact human-readable desired font summary.
func (f FontApplication) Desired() string {
	var parts []string
	if strings.TrimSpace(f.Family) != "" {
		parts = append(parts, strings.TrimSpace(f.Family))
	}
	if f.Size != 0 {
		parts = append(parts, strconv.Itoa(f.Size))
	}
	if len(parts) == 0 {
		return "(unset)"
	}
	return strings.Join(parts, " ")
}

// Summary returns a compact status string for read-only UX rows.
func (f FontApplication) Summary() string {
	summary := string(f.Status)
	if f.Reason != "" {
		summary += " - " + f.Reason
	}
	return summary
}
