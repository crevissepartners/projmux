package agentguidance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/state"
)

// SettingsFileName stores explicit guidance policy below ConfigDir.
const SettingsFileName = "agent-guidance.json"

// Settings controls all guidance or individual fragment basenames. Its
// presence makes a blank user file an empty addition, rather than legacy off.
// There is deliberately no switch for disabling only the built-in text.
type Settings struct {
	Disabled          bool     `json:"disabled"`
	DisabledFragments []string `json:"disabledFragments"`
}

// SettingsPath returns the policy file path.
func (s Store) SettingsPath() string { return filepath.Join(s.configDir, SettingsFileName) }

// LoadSettings returns explicit policy and whether its file exists. Missing
// policy enables guidance, except for the legacy blank-user-file rule in Load.
// Unknown keys, invalid fragment names and malformed JSON are errors.
func (s Store) LoadSettings() (Settings, bool, error) {
	root, err := os.OpenRoot(s.configDir)
	if errors.Is(err, fs.ErrNotExist) {
		return Settings{}, false, nil
	}
	if err != nil {
		return Settings{}, false, fmt.Errorf("read agent guidance settings: %w", err)
	}
	defer root.Close()
	info, err := root.Lstat(SettingsFileName)
	if errors.Is(err, fs.ErrNotExist) {
		return Settings{}, false, nil
	}
	if err != nil {
		return Settings{}, false, fmt.Errorf("read agent guidance settings: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Settings{}, false, fmt.Errorf("agent guidance settings is not a regular file")
	}
	content, _, err := readGuidancePart(root, SettingsFileName)
	if err != nil {
		return Settings{}, false, err
	}
	settings, err := decodeSettings(content)
	if err != nil {
		return Settings{}, false, fmt.Errorf("read agent guidance settings: %w", err)
	}
	return settings, true, nil
}

func decodeSettings(content []byte) (Settings, error) {
	var settings Settings
	decoder := json.NewDecoder(bytes.NewReader(content))
	token, err := decoder.Token()
	if err != nil {
		return settings, err
	}
	if token != json.Delim('{') {
		return settings, errors.New("expected a JSON object")
	}
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return settings, err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return settings, fmt.Errorf("invalid or duplicate settings key %v", token)
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return settings, err
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return settings, fmt.Errorf("settings key %s cannot be null", key)
		}
		switch key {
		case "disabled":
			err = json.Unmarshal(value, &settings.Disabled)
		case "disabledFragments":
			err = json.Unmarshal(value, &settings.DisabledFragments)
		default:
			return settings, fmt.Errorf("unknown settings key %q", key)
		}
		if err != nil {
			return settings, err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return settings, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return settings, errors.New("unexpected trailing settings data")
	}
	return settings, settings.validate()
}

func (s Settings) validate() error {
	for _, name := range s.DisabledFragments {
		if err := ValidateFragmentName(name); err != nil {
			return err
		}
	}
	return nil
}

// SaveSettings atomically writes private (0600) explicit policy, without
// changing the user file or fragment files. Invalid settings change nothing.
func (s Store) SaveSettings(settings Settings) error {
	if err := settings.validate(); err != nil {
		return err
	}
	return s.withPolicyLock(func() error { return s.saveSettings(settings) })
}

func (s Store) saveSettings(settings Settings) error {
	if err := settings.validate(); err != nil {
		return err
	}
	if settings.DisabledFragments == nil {
		settings.DisabledFragments = []string{}
	}
	content, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("write agent guidance settings: %w", err)
	}
	content = append(content, '\n')
	if len(content) > MaxSize {
		return fmt.Errorf("agent guidance settings exceeds the limit of %d bytes", MaxSize)
	}
	if err := writeAtomic(s.SettingsPath(), content, state.PrivateFileMode); err != nil {
		return fmt.Errorf("write agent guidance settings: %w", err)
	}
	return syncDirectory(s.configDir)
}

// ValidateFragmentName accepts only a direct case-sensitive Markdown basename.
func ValidateFragmentName(name string) error {
	if name == "" || strings.ContainsAny(name, "/\\\x00") || !strings.HasSuffix(name, ".md") {
		return fmt.Errorf("invalid guidance fragment basename %q", name)
	}
	return nil
}
