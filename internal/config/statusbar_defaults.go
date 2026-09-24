package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The status bar visibility leaves have a central default layer under the TUI
// files. A TUI leaf keeps its file and still wins when it holds a valid value;
// without one, the central default decides, and without that the built-in
// default does.
//
// The central defaults are one JSON file, not config.toml keys: the config.toml
// parser refuses keys it does not know, so a new key there would make every
// older binary still running (helpers, other installs) fail to read the whole
// file.
//
// `config apply` fills the central file once from the valid TUI values (see
// internal/app), so the central defaults start out equal to what the TUI shows.
// Nothing else writes it yet, and it never follows later TUI edits: the two
// copies may drift apart.
const (
	StatusbarDefaultsFileName = "statusbar-defaults.json"
	// StatusbarDefaultsSeedStateFileName records under StateDir that the
	// central defaults were seeded. It is state, not a setting: deleting it
	// (with the central file) makes the next `config apply` seed again.
	StatusbarDefaultsSeedStateFileName = "statusbar-defaults-seeded"

	StatusbarVisibilitySourceCentral StatusbarVisibilitySource = "central"

	statusbarVisibilityLeafPrefix = "statusbar-visibility-"
)

// statusbarDefaultsDocument is the on-disk shape of StatusbarDefaultsFileName.
// Visibility is keyed by the TUI leaf file name without its
// "statusbar-visibility-" prefix, such as "clock" or
// "agent-usage-window-codex-5h".
type statusbarDefaultsDocument struct {
	Visibility map[string]string `json:"visibility,omitempty"`
}

// StatusbarDefaultsFile returns the central status bar defaults file.
func (p Paths) StatusbarDefaultsFile() string {
	return filepath.Join(p.ConfigDir, StatusbarDefaultsFileName)
}

// StatusbarDefaultsSeedStateFile returns the record that the central status
// bar defaults were seeded.
func (p Paths) StatusbarDefaultsSeedStateFile() string {
	return filepath.Join(p.StateDir, StatusbarDefaultsSeedStateFileName)
}

// StatusbarDefaultKey returns the central defaults key for a TUI visibility
// leaf path. The settings launcher is TUI only and has no central key.
func StatusbarDefaultKey(tuiPath string) (string, bool) {
	base := filepath.Base(strings.TrimSpace(tuiPath))
	key, ok := strings.CutPrefix(base, statusbarVisibilityLeafPrefix)
	if !ok || key == "" || base == StatusbarSettingsLauncherVisibilityFileName {
		return "", false
	}
	return key, true
}

// LoadStatusbarDefaults reads the valid central defaults. A missing file is
// empty; values other than on/off are dropped.
func LoadStatusbarDefaults(path string) (map[string]StatusbarVisibility, error) {
	out := map[string]StatusbarVisibility{}
	if strings.TrimSpace(path) == "" {
		return out, nil
	}
	// #nosec G304 -- path is the resolved projmux configuration file supplied by the caller.
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return out, fmt.Errorf("read status bar defaults: %w", err)
	}
	var doc statusbarDefaultsDocument
	if err := json.Unmarshal(content, &doc); err != nil {
		return out, fmt.Errorf("parse status bar defaults %s: %w", path, err)
	}
	for key, raw := range doc.Visibility {
		switch value := StatusbarVisibility(strings.ToLower(strings.TrimSpace(raw))); value {
		case StatusbarVisibilityOn, StatusbarVisibilityOff:
			out[key] = value
		}
	}
	return out, nil
}

// LoadCentralStatusbarVisibility reads the central default of the TUI leaf at
// tuiPath. Source is central when a valid value is stored, and default (with
// defaultValue) otherwise, so a caller can tell the two apart.
func LoadCentralStatusbarVisibility(defaultsPath, tuiPath string, defaultValue StatusbarVisibility) (StatusbarVisibilityState, error) {
	state := defaultStatusbarVisibilityState(defaultValue)
	key, ok := StatusbarDefaultKey(tuiPath)
	if !ok {
		return state, nil
	}
	values, err := LoadStatusbarDefaults(defaultsPath)
	if err != nil {
		return state, err
	}
	if value, ok := values[key]; ok {
		state.Effective = value
		state.Saved = string(value)
		state.Source = StatusbarVisibilitySourceCentral
	}
	return state, nil
}

// LoadLayeredStatusbarVisibility resolves one leaf through its layers: a valid
// TUI value, then the central default, then defaultValue. An invalid TUI value
// stays reported in Invalid whichever layer decides.
//
// The error is the TUI read's; an unreadable central file only means there is
// no central default.
func LoadLayeredStatusbarVisibility(tuiPath, defaultsPath string, defaultValue StatusbarVisibility) (StatusbarVisibilityState, error) {
	state, err := LoadStatusbarVisibilityFileWithDefault(tuiPath, defaultValue)
	if err == nil && state.Source == StatusbarVisibilitySourceSaved {
		return state, nil
	}
	central, centralErr := LoadCentralStatusbarVisibility(defaultsPath, tuiPath, defaultValue)
	if centralErr == nil && central.Source == StatusbarVisibilitySourceCentral {
		central.Invalid = state.Invalid
		return central, nil
	}
	return state, err
}

// SeedStatusbarDefaults adds values to the central defaults without replacing
// a key that already holds a valid value. It returns the keys it added, sorted,
// and writes nothing when there is nothing to add.
func SeedStatusbarDefaults(path string, values map[string]StatusbarVisibility) ([]string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrHomeDirRequired
	}
	current, err := LoadStatusbarDefaults(path)
	if err != nil {
		return nil, err
	}
	var added []string
	for key, value := range values {
		if _, ok := current[key]; ok {
			continue
		}
		current[key] = NormalizeStatusbarVisibility(string(value))
		added = append(added, key)
	}
	if len(added) == 0 {
		return nil, nil
	}
	slices.Sort(added)
	doc := statusbarDefaultsDocument{Visibility: make(map[string]string, len(current))}
	for key, value := range current {
		doc.Visibility[key] = string(value)
	}
	content, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode status bar defaults: %w", err)
	}
	if err := writeFileAtomic(path, append(content, '\n')); err != nil {
		return nil, fmt.Errorf("write status bar defaults: %w", err)
	}
	return added, nil
}

// writeFileAtomic replaces path with content through a 0600 temp file in the
// same directory.
func writeFileAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
