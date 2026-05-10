package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// GlobalConfigRelativePath is the path inside the projmux config directory at
// which global lifecycle declarations live. The same TOML schema as the
// project-local config.toml is reused so the parser can be shared.
const GlobalConfigRelativePath = "projmux/config.toml"

// resolveGlobalConfigDir returns the directory that contains the global
// config.toml file. It prefers XDG_CONFIG_HOME (consistent with the rest of
// projmux) and falls back to $HOME/.config.
func resolveGlobalConfigDir(getenv func(string) string, homeDir func() (string, error)) (string, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	configHome := strings.TrimSpace(getenv("XDG_CONFIG_HOME"))
	if configHome == "" {
		home, err := homeDir()
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(home) == "" {
			return "", errors.New("home directory is required to resolve global config path")
		}
		configHome = filepath.Join(home, ".config")
	}
	return configHome, nil
}

// GlobalConfigPath returns the absolute path to the global projmux config.toml
// file. The file may or may not exist; callers should treat ENOENT as an empty
// configuration.
func GlobalConfigPath(getenv func(string) string, homeDir func() (string, error)) (string, error) {
	dir, err := resolveGlobalConfigDir(getenv, homeDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, GlobalConfigRelativePath), nil
}

// LoadGlobalConfig reads the global config.toml at path. A missing file
// returns a zero-value ProjectConfig and no error. Parse errors propagate.
func LoadGlobalConfig(path string) (ProjectConfig, error) {
	if strings.TrimSpace(path) == "" {
		return ProjectConfig{}, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ProjectConfig{}, nil
		}
		return ProjectConfig{}, err
	}
	return ParseProjectConfig(string(content))
}

// UpdateGlobalConfig loads the global config.toml at path, applies update,
// validates the result, and writes it back atomically. A nil update is
// treated as a no-op load + normalize + rewrite, which is handy for tests.
// Unlike UpdateProjectConfig, the result is not trusted because global hooks
// are always considered authoritative (they live alongside the projmux
// binary's own config dir).
func UpdateGlobalConfig(path string, update func(*ProjectConfig) error) (ProjectConfig, error) {
	if strings.TrimSpace(path) == "" {
		return ProjectConfig{}, errors.New("global config path is required")
	}
	cfg, err := LoadGlobalConfig(path)
	if err != nil {
		return ProjectConfig{}, err
	}
	if cfg.Hooks == nil {
		cfg.Hooks = map[Event]string{}
	}
	if cfg.Env == nil {
		cfg.Env = map[string]string{}
	}
	if update != nil {
		if err := update(&cfg); err != nil {
			return ProjectConfig{}, err
		}
	}
	if err := validateProjectConfig(cfg); err != nil {
		return ProjectConfig{}, err
	}
	normalizeProjectConfig(&cfg)
	if err := writeProjectConfigFile(path, cfg); err != nil {
		return ProjectConfig{}, err
	}
	return cfg, nil
}
