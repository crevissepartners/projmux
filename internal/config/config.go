package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	AppName                = "projmux"
	PinsFileName           = "pins"
	TagsFileName           = "tags"
	PreviewStateFileName   = "preview-state"
	ProjdirFileName        = "projdir"
	HooksDirName           = "hooks"
	PostCreateHookFileName = "post-create"
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

// TagFile returns the default file used for persistent tagged-session state.
func (p Paths) TagFile() string {
	return filepath.Join(p.ConfigDir, TagsFileName)
}

// PreviewStateFile returns the default file used for persistent preview state.
func (p Paths) PreviewStateFile() string {
	return filepath.Join(p.StateDir, PreviewStateFileName)
}

// ProjdirFile returns the default file used for the persisted PROJDIR value.
func (p Paths) ProjdirFile() string {
	return filepath.Join(p.ConfigDir, ProjdirFileName)
}

// PostCreateHookPath returns the default location for the optional
// post-create hook script.
func (p Paths) PostCreateHookPath() string {
	return filepath.Join(p.ConfigDir, HooksDirName, PostCreateHookFileName)
}

// ProjdirFile returns the path to the persisted PROJDIR file rooted at the
// supplied home directory. An empty homeDir yields an empty string.
func ProjdirFile(homeDir string) string {
	if homeDir == "" {
		return ""
	}
	return filepath.Join(homeDir, ".config", AppName, ProjdirFileName)
}

// LoadProjdir returns the trimmed first line of the persisted PROJDIR file
// rooted at homeDir. A missing file or empty content yields ("", nil).
func LoadProjdir(homeDir string) (string, error) {
	path := ProjdirFile(homeDir)
	if path == "" {
		return "", nil
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("open projdir file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	if scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("read projdir file: %w", err)
		}
		return line, nil
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read projdir file: %w", err)
	}
	return "", nil
}

// SaveProjdir persists value to the PROJDIR file rooted at homeDir using an
// atomic rename. An empty value removes the file. The parent directory is
// created with 0o755 if missing.
func SaveProjdir(homeDir, value string) error {
	path := ProjdirFile(homeDir)
	if path == "" {
		return ErrHomeDirRequired
	}

	value = strings.TrimSpace(value)
	if value == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove projdir file: %w", err)
		}
		return nil
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create projdir directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ProjdirFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("create projdir temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.WriteString(value + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("write projdir temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close projdir temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod projdir temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename projdir temp file: %w", err)
	}
	return nil
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
