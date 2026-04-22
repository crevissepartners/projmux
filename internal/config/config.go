package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	AppName      = "projmux"
	PinsFileName = "pins"
)

var ErrHomeDirRequired = errors.New("home directory is required when XDG homes are unset")

// Homes captures the environment-derived base directories used to build projmux
// config and state paths.
type Homes struct {
	HomeDir    string
	ConfigHome string
	StateHome  string
}

// Paths holds the default directory layout used by the bootstrap.
type Paths struct {
	ConfigDir string
	StateDir  string
}

// DefaultPaths builds the standard XDG-style projmux directories.
func DefaultPaths(configHome, stateHome string) Paths {
	return Paths{
		ConfigDir: filepath.Join(configHome, AppName),
		StateDir:  filepath.Join(stateHome, AppName),
	}
}

// PinFile returns the default file used for persistent pin state.
func (p Paths) PinFile() string {
	return filepath.Join(p.ConfigDir, PinsFileName)
}

// Paths resolves the effective XDG-style projmux directories from the provided
// home values.
func (h Homes) Paths() (Paths, error) {
	configHome := h.ConfigHome
	stateHome := h.StateHome

	if configHome == "" || stateHome == "" {
		if h.HomeDir == "" {
			return Paths{}, ErrHomeDirRequired
		}
	}
	if configHome == "" {
		configHome = filepath.Join(h.HomeDir, ".config")
	}
	if stateHome == "" {
		stateHome = filepath.Join(h.HomeDir, ".local", "state")
	}

	return DefaultPaths(configHome, stateHome), nil
}

// DefaultPathsFromEnv resolves projmux paths from the process environment using
// XDG variables when present and HOME-backed fallbacks otherwise.
func DefaultPathsFromEnv() (Paths, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve user home: %w", err)
	}

	return Homes{
		HomeDir:    homeDir,
		ConfigHome: os.Getenv("XDG_CONFIG_HOME"),
		StateHome:  os.Getenv("XDG_STATE_HOME"),
	}.Paths()
}
