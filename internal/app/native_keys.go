package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
)

const (
	nativeKeysEnvName              = "PROJMUX_NATIVE_KEYS"
	nativeKeysConsentStateFileName = "native-keys-consent-v1"
)

func nativeKeysEnabled(lookupEnv func(string) string, homeDir func() (string, error)) bool {
	return nativeKeysEnvEnabled(lookupEnv) && nativeKeysSettingEnabled(lookupEnv, homeDir)
}

func nativeKeysEnvEnabled(lookupEnv func(string) string) bool {
	if lookupEnv == nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(lookupEnv(nativeKeysEnvName))) {
	case "0", "false":
		return false
	default:
		return true
	}
}

func nativeKeysSettingEnabled(lookupEnv func(string) string, homeDir func() (string, error)) bool {
	path, err := hooks.GlobalConfigPath(lookupEnv, homeDir)
	if err != nil {
		return true
	}
	cfg, err := hooks.LoadGlobalConfig(path)
	if err != nil {
		// Do not enter a permission-requesting path when the saved policy
		// cannot be read. A missing file is already handled as an empty config.
		return false
	}
	if cfg.UI.NativeKeys == nil {
		return true
	}
	return *cfg.UI.NativeKeys
}

func (c *settingsCommand) currentNativeKeysSetting() (bool, error) {
	path, err := c.globalConfigPath()
	if err != nil {
		return true, err
	}
	cfg, err := hooks.LoadGlobalConfig(path)
	if err != nil {
		return true, err
	}
	if cfg.UI.NativeKeys == nil {
		return true, nil
	}
	return *cfg.UI.NativeKeys, nil
}

func (c *settingsCommand) setNativeKeysSetting(enabled bool) error {
	path, err := c.globalConfigPath()
	if err != nil {
		return err
	}
	_, err = hooks.UpdateGlobalConfig(path, func(cfg *hooks.ProjectConfig) error {
		value := enabled
		cfg.UI.NativeKeys = &value
		return nil
	})
	return err
}

func nativeKeysConsentHintPath(lookupEnv func(string) string, homeDir func() (string, error)) (string, error) {
	if homeDir == nil {
		return "", errors.New("native key consent home directory resolver is not configured")
	}
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	stateHome := ""
	if lookupEnv != nil {
		stateHome = strings.TrimRight(lookupEnv("XDG_STATE_HOME"), string(os.PathSeparator))
	}
	paths, err := (config.Homes{HomeDir: home, StateHome: stateHome}).Paths()
	if err != nil {
		return "", err
	}
	return filepath.Join(paths.StateDir, nativeKeysConsentStateFileName), nil
}

func showNativeKeysConsentHint(
	stderr io.Writer,
	lookupEnv func(string) string,
	homeDir func() (string, error),
	readFile func(string) ([]byte, error),
	writeFile func(string, []byte, os.FileMode) error,
) {
	if stderr == nil {
		return
	}
	path, pathErr := nativeKeysConsentHintPath(lookupEnv, homeDir)
	if pathErr == nil {
		if readFile == nil {
			readFile = os.ReadFile
		}
		if _, err := readFile(path); err == nil {
			return
		}
	}

	locale := appLocale(homeDir, lookupEnv)
	fmt.Fprintln(stderr, localizeText(locale, i18n.KeyNativeKeysConsentHint,
		"projmux native macOS keybindings capture modified chords only (never plain-text typing) so physical Option works across terminals. Processing and tmux injection stay local. Disable in Settings > Keybindings or set PROJMUX_NATIVE_KEYS=0."))

	if pathErr != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if writeFile == nil {
		writeFile = os.WriteFile
	}
	_ = writeFile(path, []byte("shown\n"), 0o600)
}
