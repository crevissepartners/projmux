package settings

import (
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/services/usage"
)

// The central status bar defaults sit under the TUI visibility files (see
// internal/config/statusbar_defaults.go). The readers below take the central
// layer alone and never read a TUI file, so a surface other than the TUI can
// fall back to the central default instead of the TUI's value. Unlike the
// strict statusbar.visibility facade, they never fail: a path, read or parse
// error returns the built-in (usage window: capability) default with Source
// default, as the receiver-less app readers always have.

// centralStatusbarLeaf is one TUI visibility leaf with a central default.
type centralStatusbarLeaf struct {
	path         func(config.Paths) string
	defaultValue config.StatusbarVisibility
}

// centralStatusbarLeaves lists the leaves in seed order: both HUDs, every
// usage provider and window of the HUD capability table, and the four row one
// segments. The settings launcher is TUI only.
func centralStatusbarLeaves() []centralStatusbarLeaf {
	leaves := []centralStatusbarLeaf{
		{path: config.Paths.StatusbarNotificationsHUDVisibilityFile, defaultValue: config.StatusbarVisibilityOn},
		{path: config.Paths.StatusbarAgentUsageHUDVisibilityFile, defaultValue: config.StatusbarVisibilityOn},
	}
	for _, capability := range usage.HUDProviderCapabilities() {
		provider := string(capability.ID)
		leaves = append(leaves, centralStatusbarLeaf{
			path:         func(p config.Paths) string { return p.StatusbarAgentUsageProviderVisibilityFile(provider) },
			defaultValue: config.StatusbarVisibilityOn,
		})
		for _, window := range capability.Windows {
			key := window.Key
			leaves = append(leaves, centralStatusbarLeaf{
				path:         func(p config.Paths) string { return p.StatusbarAgentUsageWindowVisibilityFile(provider, key) },
				defaultValue: config.NormalizeStatusbarVisibility(string(window.DefaultVisibility)),
			})
		}
	}
	return append(leaves,
		centralStatusbarLeaf{path: config.Paths.StatusbarProjectVisibilityFile, defaultValue: config.StatusbarVisibilityOn},
		centralStatusbarLeaf{path: config.Paths.StatusbarWorkingDirectoryVisibilityFile, defaultValue: config.StatusbarVisibilityOn},
		centralStatusbarLeaf{path: config.Paths.StatusbarGitVisibilityFile, defaultValue: config.StatusbarVisibilityOn},
		centralStatusbarLeaf{path: config.Paths.StatusbarClockVisibilityFile, defaultValue: config.StatusbarVisibilityOn},
	)
}

// CentralStatusbarSeedLeaves are the TUI visibility files whose valid values
// seed the central defaults, in seed order.
func CentralStatusbarSeedLeaves(paths config.Paths) []string {
	leaves := centralStatusbarLeaves()
	out := make([]string, 0, len(leaves))
	for _, leaf := range leaves {
		out = append(out, leaf.path(paths))
	}
	return out
}

// CentralStatusbarLeaf reads one leaf from the central defaults alone: Source
// central with the stored value, else Source default with defaultValue. A
// read or parse error of the central file counts as nothing stored.
func CentralStatusbarLeaf(paths config.Paths, tuiPath string, defaultValue config.StatusbarVisibility) config.StatusbarVisibilityState {
	state, err := config.LoadCentralStatusbarVisibility(paths.StatusbarDefaultsFile(), tuiPath, defaultValue)
	if err != nil {
		state = config.DefaultStatusbarVisibilityState()
		state.Effective = config.NormalizeStatusbarVisibility(string(defaultValue))
	}
	return state
}

// CentralStatusbarVisibility is the receiver-less central reader of one leaf.
// A path resolution error or a leaf without a file returns defaultValue with
// Source default; otherwise CentralStatusbarLeaf decides.
func CentralStatusbarVisibility(resolve func() (config.Paths, error), leaf func(config.Paths) (string, bool), defaultValue config.StatusbarVisibility) config.StatusbarVisibilityState {
	defaultState := config.DefaultStatusbarVisibilityState()
	defaultState.Effective = config.NormalizeStatusbarVisibility(string(defaultValue))
	paths, err := resolve()
	if err != nil {
		return defaultState
	}
	path, ok := leaf(paths)
	if !ok {
		return defaultState
	}
	return CentralStatusbarLeaf(paths, path, defaultValue)
}

// CentralStatusbarDefaults reads every central leaf through
// CentralStatusbarVisibility, ordered by canonical key. It returns a new slice
// on every call and never fails.
func CentralStatusbarDefaults(resolve func() (config.Paths, error)) []VisibilityState {
	leaves := centralStatusbarLeaves()
	out := make([]VisibilityState, 0, len(leaves))
	for _, leaf := range leaves {
		key, ok := config.StatusbarDefaultKey(leaf.path(config.Paths{}))
		if !ok {
			continue
		}
		v := CentralStatusbarVisibility(resolve, func(p config.Paths) (string, bool) { return leaf.path(p), true }, leaf.defaultValue)
		out = append(out, VisibilityState{Key: key, Effective: string(v.Effective), Saved: v.Saved, Source: string(v.Source), Invalid: v.Invalid})
	}
	slices.SortFunc(out, func(a, b VisibilityState) int { return strings.Compare(a.Key, b.Key) })
	return out
}
