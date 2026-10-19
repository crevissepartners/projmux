package projmux_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/pkg/projmux"
)

func TestSettingsDirectoriesInputs(t *testing.T) {
	for _, tt := range []struct {
		name, input, profiles, personas string
	}{
		{"absolute", "/config/projmux", "/config/projmux/profiles", "/config/projmux/personas"},
		{"empty", "", "profiles", "personas"},
		{"relative", "config/projmux", "config/projmux/profiles", "config/projmux/personas"},
		{"trailing separator", "/config/projmux/", "/config/projmux/profiles", "/config/projmux/personas"},
		{"lexical cleaning", "config/old/../projmux//", "config/projmux/profiles", "config/projmux/personas"},
		{"literal environment", "$HOME/~/projmux", "$HOME/~/projmux/profiles", "$HOME/~/projmux/personas"},
		{"spaces preserved", " config ", " config /profiles", " config /personas"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			profiles, personas := projmux.SettingsDirectories(tt.input)
			if profiles != tt.profiles || personas != tt.personas {
				t.Fatalf("SettingsDirectories(%q) = (%q, %q), want (%q, %q)", tt.input, profiles, personas, tt.profiles, tt.personas)
			}
		})
	}
}

func TestSettingsDirectoriesNoSideEffects(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "missing", "projmux")
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "ignored"))
	before := centralTree(t, root)
	environment := os.Environ()
	var reads int
	restore := config.ObserveFrontReads(func(config.FrontRead) { reads++ })
	defer restore()
	config.NoteFrontRead(config.SettingConfigTheme, filepath.Join(root, "theme"))
	if reads != 1 {
		t.Fatal("front read observer positive control failed")
	}
	reads = 0
	for range 2 {
		profiles, personas := projmux.SettingsDirectories(configDir)
		if profiles != filepath.Join(configDir, "profiles") || personas != filepath.Join(configDir, "personas") {
			t.Fatalf("unexpected paths: %q, %q", profiles, personas)
		}
	}
	if reads != 0 || !reflect.DeepEqual(environment, os.Environ()) || !reflect.DeepEqual(before, centralTree(t, root)) {
		t.Fatal("directory calculation changed environment/filesystem or observed front reads")
	}
	// A file in place of a directory must not affect this lexical operation.
	if err := os.WriteFile(filepath.Dir(configDir), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	profiles, personas := projmux.SettingsDirectories(configDir)
	if profiles != filepath.Join(configDir, "profiles") || personas != filepath.Join(configDir, "personas") {
		t.Fatalf("filesystem contents affected paths: %q, %q", profiles, personas)
	}
}
