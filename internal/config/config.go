package config

import "path/filepath"

const (
	AppName = "projmux"
)

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
