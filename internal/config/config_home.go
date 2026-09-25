package config

import (
	"path/filepath"
	"strings"
)

// ResolveConfigHome is the one place the config home is resolved:
// XDG_CONFIG_HOME when it is set, else homeDir's .config. A blank
// XDG_CONFIG_HOME counts as unset, so every caller lands in the same directory
// instead of some writing under a whitespace-named relative path. Without an
// XDG value and a home directory it returns ErrHomeDirRequired; callers that
// have their own fallback for that case apply it themselves.
func ResolveConfigHome(homeDir, xdgConfigHome string) (string, error) {
	if configHome := strings.TrimSpace(xdgConfigHome); configHome != "" {
		return filepath.Clean(configHome), nil
	}
	if homeDir == "" {
		return "", ErrHomeDirRequired
	}
	return filepath.Join(homeDir, ".config"), nil
}
