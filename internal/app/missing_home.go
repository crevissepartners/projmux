package app

import (
	"errors"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
)

// resolvedHome is the home directory homeDir reports, unchanged, or "" when
// there is no resolver, it fails, or it reports a blank value. config.Resolve*Home turns
// "" into a *config.MissingHomeError unless the XDG variable is absolute, so
// a caller never falls back to a path relative to its working directory.
func resolvedHome(homeDir func() (string, error)) string {
	if homeDir == nil {
		return ""
	}
	home, err := homeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return home
}

// isMissingHome reports whether err is the missing-HOME reason: a read of
// saved state keeps its built-in default on it, and a write refuses with it.
func isMissingHome(err error) bool {
	return errors.Is(err, config.ErrHomeDirRequired)
}

// pathOrReason is what a path display shows: the path, or, when it cannot be
// resolved, the reason in parentheses instead of an empty path.
func pathOrReason(path string, err error) string {
	if err != nil {
		return "(" + err.Error() + ")"
	}
	return path
}

// savedSettingsPaths resolves the config and state paths of saved settings
// under the missing-HOME rule: a failing or blank home directory counts as no
// HOME, absolute XDG homes still resolve without one, and with neither it
// returns a *config.MissingHomeError.
func savedSettingsPaths(homeDir func() (string, error), lookupEnv func(string) string) (config.Paths, error) {
	if lookupEnv == nil {
		lookupEnv = func(string) string { return "" }
	}
	return config.Homes{
		HomeDir:    resolvedHome(homeDir),
		ConfigHome: lookupEnv("XDG_CONFIG_HOME"),
		StateHome:  lookupEnv("XDG_STATE_HOME"),
	}.Paths()
}
