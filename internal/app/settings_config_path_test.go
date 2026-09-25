package app

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// TestSettingsShowsResolvedConfigFilePaths renders the locale source and AI
// providers rows and checks they name the files projmux actually reads: under
// XDG_CONFIG_HOME when it is set, and the unchanged ~/.config spelling with the
// default environment.
func TestSettingsShowsResolvedConfigFilePaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// xdg returns XDG_CONFIG_HOME for a test home and an outside directory.
		xdg        func(home, outside string) string
		wantPrefix func(home, outside string) string
	}{
		{
			name:       "default env",
			xdg:        func(string, string) string { return "" },
			wantPrefix: func(string, string) string { return "~/.config/projmux/" },
		},
		{
			name:       "blank XDG_CONFIG_HOME",
			xdg:        func(string, string) string { return " " },
			wantPrefix: func(string, string) string { return "~/.config/projmux/" },
		},
		{
			name:       "XDG_CONFIG_HOME outside home",
			xdg:        func(_, outside string) string { return outside },
			wantPrefix: func(_, outside string) string { return outside + "/projmux/" },
		},
		{
			name:       "XDG_CONFIG_HOME under home",
			xdg:        func(home, _ string) string { return filepath.Join(home, "xdg") + "/" },
			wantPrefix: func(string, string) string { return "~/xdg/projmux/" },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			outside := t.TempDir()
			xdg := tt.xdg(home, outside)
			configHome := filepath.Join(home, ".config")
			if strings.TrimSpace(xdg) != "" {
				configHome = xdg
			}
			writeFile(t, filepath.Join(configHome, "projmux", "config.toml"), "[ui]\nlocale = \"en-US\"\n")
			cmd := &settingsCommand{
				homeDir: func() (string, error) { return home, nil },
				lookupEnv: func(name string) string {
					if name == "XDG_CONFIG_HOME" {
						return xdg
					}
					return ""
				},
			}
			prefix := tt.wantPrefix(home, outside)

			current := settingsEntryLabelContaining(t, cmd.localeEntries(), "Current")
			if want := prefix + "config.toml"; !strings.Contains(current, want) {
				t.Fatalf("locale Current row = %q, want config source %q", current, want)
			}
			providers := cmd.aiEnabledAgentEntries()[1].Label
			if want := prefix + config.AIEnabledAgentsFileName; !strings.Contains(providers, want) {
				t.Fatalf("AI providers row = %q, want %q", providers, want)
			}
			if strings.HasPrefix(prefix, "~/.config/") {
				return
			}
			for _, label := range []string{current, providers} {
				if strings.Contains(label, "~/.config/projmux") {
					t.Fatalf("row = %q names ~/.config/projmux while XDG_CONFIG_HOME=%q", label, xdg)
				}
			}
		})
	}
}

// TestSettingsUnsupportedLocaleWarningNamesResolvedConfigFile covers the other
// place the config source is shown: the unsupported-locale warning.
func TestSettingsUnsupportedLocaleWarningNamesResolvedConfigFile(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	xdg := t.TempDir()
	writeFile(t, filepath.Join(xdg, "projmux", "config.toml"), "[ui]\nlocale = \"ja-JP\"\n")
	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "XDG_CONFIG_HOME" {
				return xdg
			}
			return ""
		},
	}

	warning := settingsEntryLabelContaining(t, cmd.localeEntries(), "Warning")
	if want := filepath.Join(xdg, "projmux", "config.toml"); !strings.Contains(warning, want) {
		t.Fatalf("unsupported locale warning = %q, want source %q", warning, want)
	}
}

func TestConfigFileDisplayPathWithoutAHome(t *testing.T) {
	t.Parallel()

	noHome := func() (string, error) { return "", errors.New("no home") }
	env := func(xdg string) func(string) string {
		return func(name string) string {
			if name == "XDG_CONFIG_HOME" {
				return xdg
			}
			return ""
		}
	}
	if got, want := configFileDisplayPath(noHome, env(""), "config.toml"), "~/.config/projmux/config.toml"; got != want {
		t.Fatalf("no home, no XDG = %q, want default spelling %q", got, want)
	}
	if got, want := configFileDisplayPath(noHome, env("/x"), "config.toml"), "/x/projmux/config.toml"; got != want {
		t.Fatalf("no home, XDG=/x = %q, want %q", got, want)
	}
}

func settingsEntryLabelContaining(t *testing.T, entries []intpickercompat.Entry, needle string) string {
	t.Helper()
	for _, entry := range entries {
		if strings.Contains(entry.Label, needle) {
			return entry.Label
		}
	}
	t.Fatalf("no entry label contains %q in %#v", needle, entries)
	return ""
}
