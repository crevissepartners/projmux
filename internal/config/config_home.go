package config

import (
	"path/filepath"
	"strings"
)

// Names of the XDG base directory variables, as a MissingHomeError names them.
const (
	XDGConfigHomeVar = "XDG_CONFIG_HOME"
	XDGStateHomeVar  = "XDG_STATE_HOME"
	XDGDataHomeVar   = "XDG_DATA_HOME"
	XDGCacheHomeVar  = "XDG_CACHE_HOME"
)

// MissingHomeError is the one reason a config, state, data, or cache path
// cannot be resolved: there is no home directory and Var is not an absolute
// path. Callers do not invent their own text for it: a read of saved state
// keeps its built-in default, a write refuses with this error, and a path
// display shows its text instead of an empty path. It matches
// ErrHomeDirRequired under errors.Is.
type MissingHomeError struct {
	// Var is the XDG variable, such as XDG_CONFIG_HOME, that would have
	// replaced the home directory.
	Var string
}

func (e *MissingHomeError) Error() string {
	return "HOME or an absolute " + e.Var + " is required"
}

// Is reports ErrHomeDirRequired, so existing errors.Is checks keep matching.
func (e *MissingHomeError) Is(target error) bool {
	return target == ErrHomeDirRequired
}

// ResolveConfigHome is the one place the config home is resolved:
// XDG_CONFIG_HOME when it is an absolute path, else homeDir's .config. See
// resolveBaseDir for the rule every XDG base directory shares.
func ResolveConfigHome(homeDir, xdgConfigHome string) (string, error) {
	return resolveBaseDir(homeDir, xdgConfigHome, XDGConfigHomeVar, ".config")
}

// ResolveStateHome resolves the state home: XDG_STATE_HOME when it is an
// absolute path, else homeDir's .local/state.
func ResolveStateHome(homeDir, xdgStateHome string) (string, error) {
	return resolveBaseDir(homeDir, xdgStateHome, XDGStateHomeVar, ".local", "state")
}

// ResolveDataHome resolves the data home: XDG_DATA_HOME when it is an absolute
// path, else homeDir's .local/share.
func ResolveDataHome(homeDir, xdgDataHome string) (string, error) {
	return resolveBaseDir(homeDir, xdgDataHome, XDGDataHomeVar, ".local", "share")
}

// ResolveCacheHome resolves the cache home: XDG_CACHE_HOME when it is an
// absolute path, else homeDir's .cache.
func ResolveCacheHome(homeDir, xdgCacheHome string) (string, error) {
	return resolveBaseDir(homeDir, xdgCacheHome, XDGCacheHomeVar, ".cache")
}

// resolveBaseDir is the XDG Base Directory rule for all four homes. The spec
// makes every path in these variables absolute and says a relative one is
// invalid and must be ignored, so an empty, blank, or relative value (a
// literal "~/x" included) counts as unset and every caller lands in the same
// directory instead of some writing under a path relative to their working
// directory. A blank homeDir counts as no home directory. Without a usable
// value and a home directory it returns a MissingHomeError naming name, never
// an empty path.
func resolveBaseDir(homeDir, value, name string, defaultElems ...string) (string, error) {
	if dir := strings.TrimSpace(value); filepath.IsAbs(dir) {
		return filepath.Clean(dir), nil
	}
	if strings.TrimSpace(homeDir) == "" {
		return "", &MissingHomeError{Var: name}
	}
	return filepath.Join(append([]string{homeDir}, defaultElems...)...), nil
}
