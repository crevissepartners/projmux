package config

import (
	"path/filepath"
	"strings"
)

// ResolveConfigHome is the one place the config home is resolved:
// XDG_CONFIG_HOME when it is an absolute path, else homeDir's .config. See
// resolveBaseDir for the rule every XDG base directory shares.
func ResolveConfigHome(homeDir, xdgConfigHome string) (string, error) {
	return resolveBaseDir(homeDir, xdgConfigHome, ".config")
}

// ResolveStateHome resolves the state home: XDG_STATE_HOME when it is an
// absolute path, else homeDir's .local/state.
func ResolveStateHome(homeDir, xdgStateHome string) (string, error) {
	return resolveBaseDir(homeDir, xdgStateHome, ".local", "state")
}

// ResolveDataHome resolves the data home: XDG_DATA_HOME when it is an absolute
// path, else homeDir's .local/share.
func ResolveDataHome(homeDir, xdgDataHome string) (string, error) {
	return resolveBaseDir(homeDir, xdgDataHome, ".local", "share")
}

// ResolveCacheHome resolves the cache home: XDG_CACHE_HOME when it is an
// absolute path, else homeDir's .cache.
func ResolveCacheHome(homeDir, xdgCacheHome string) (string, error) {
	return resolveBaseDir(homeDir, xdgCacheHome, ".cache")
}

// resolveBaseDir is the XDG Base Directory rule for all four homes. The spec
// makes every path in these variables absolute and says a relative one is
// invalid and must be ignored, so an empty, blank, or relative value (a
// literal "~/x" included) counts as unset and every caller lands in the same
// directory instead of some writing under a path relative to their working
// directory. Without a usable value and a home directory it returns
// ErrHomeDirRequired; callers that have their own fallback for that case apply
// it themselves.
func resolveBaseDir(homeDir, value string, defaultElems ...string) (string, error) {
	if dir := strings.TrimSpace(value); filepath.IsAbs(dir) {
		return filepath.Clean(dir), nil
	}
	if homeDir == "" {
		return "", ErrHomeDirRequired
	}
	return filepath.Join(append([]string{homeDir}, defaultElems...)...), nil
}
