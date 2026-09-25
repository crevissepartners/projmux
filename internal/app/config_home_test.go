package app

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/hooks"
)

// TestBlankXDGConfigHomeSharesOneDirectory pins that a whitespace-only
// XDG_CONFIG_HOME counts as unset at every config-home site, so pins, the AI
// split default, and the generated tmux config all land in $HOME/.config.
func TestBlankXDGConfigHomeSharesOneDirectory(t *testing.T) {
	t.Parallel()

	home := "/home/tester"
	homeDir := func() (string, error) { return home, nil }
	lookupEnv := func(name string) string {
		if name == "XDG_CONFIG_HOME" {
			return " "
		}
		return ""
	}
	want := filepath.Join(home, ".config", "projmux")

	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		t.Fatalf("configPaths() error = %v", err)
	}
	aiPaths, err := (&aiCommand{homeDir: homeDir, lookupEnv: lookupEnv}).aiConfigPaths()
	if err != nil {
		t.Fatalf("aiConfigPaths() error = %v", err)
	}
	doctorPath, err := doctorGeneratedConfigPath(lookupEnv, homeDir)
	if err != nil {
		t.Fatalf("doctorGeneratedConfigPath() error = %v", err)
	}
	appConfigPath, err := generatedAppConfigDefaultPath(homeDir, lookupEnv)
	if err != nil {
		t.Fatalf("generatedAppConfigDefaultPath() error = %v", err)
	}
	hooksPath, err := hooks.GlobalConfigPath(lookupEnv, homeDir)
	if err != nil {
		t.Fatalf("hooks.GlobalConfigPath() error = %v", err)
	}
	for name, got := range map[string]string{
		"pins":               filepath.Dir(paths.PinFile()),
		"AI split mode":      filepath.Dir((&aiCommand{homeDir: homeDir, lookupEnv: lookupEnv}).configFile()),
		"AI config":          aiPaths.ConfigDir,
		"doctor tmux.conf":   filepath.Dir(doctorPath),
		"applied tmux.conf":  filepath.Dir(appConfigPath),
		"shell tmux.conf":    filepath.Dir((&shellCommand{homeDir: homeDir, lookupEnv: lookupEnv}).defaultConfigPath()),
		"global hook config": filepath.Dir(hooksPath),
	} {
		if got != want {
			t.Errorf("%s dir = %q, want %q", name, got, want)
		}
	}
}

// TestConfigHomeSitesKeepTheirNoHomeFallback pins what each site does when
// neither XDG_CONFIG_HOME nor a home directory is available.
func TestConfigHomeSitesKeepTheirNoHomeFallback(t *testing.T) {
	t.Parallel()

	blank := func(string) string { return " " }
	for _, tc := range []struct {
		name    string
		homeDir func() (string, error)
	}{
		{name: "home error", homeDir: func() (string, error) { return "", errors.New("no home") }},
		{name: "empty home", homeDir: func() (string, error) { return "", nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, want := (&shellCommand{homeDir: tc.homeDir, lookupEnv: blank}).defaultConfigPath(), filepath.Join(".config", "projmux", "tmux.conf"); got != want {
				t.Errorf("shell defaultConfigPath() = %q, want %q", got, want)
			}
			if got, err := doctorGeneratedConfigPath(blank, tc.homeDir); err == nil || err.Error() != "resolve generated config home" {
				t.Errorf("doctorGeneratedConfigPath() = %q, %v; want resolve generated config home", got, err)
			}
			paths, err := (&aiCommand{homeDir: tc.homeDir, lookupEnv: blank}).aiConfigPaths()
			if tc.name == "home error" {
				if err == nil || err.Error() != "resolve home directory: no home" {
					t.Errorf("aiConfigPaths() = %+v, %v; want resolve home directory error", paths, err)
				}
				return
			}
			if err != nil || paths.ConfigDir != filepath.Join(".config", "projmux") {
				t.Errorf("aiConfigPaths() = %+v, %v; want relative .config/projmux", paths, err)
			}
		})
	}
}
