package app

import (
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/internal/theme"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// TestRunPickerOptionBackendThemesFromGlobalConfigWhenUnset is the picker-
// theming coverage guard: a picker that does NOT pre-resolve its own theme
// (options.Theme == nil) — sessions, session_state, project_startup, quit, and
// the switch sub-pickers — must still pick up the explicit global `[theme]` at
// the shared choke point, instead of falling back to the built-in default. The
// global config is injected via XDG_CONFIG_HOME so the test is hermetic.
func TestRunPickerOptionBackendThemesFromGlobalConfigWhenUnset(t *testing.T) {
	t.Parallel()

	configHome := t.TempDir()
	writeFile(t, filepath.Join(configHome, "projmux", "config.toml"), `
[theme]
background = "#010203"
surface = "#040506"
foreground = "#aabbcc"
`)
	lookupEnv := func(name string) string {
		if name == "XDG_CONFIG_HOME" {
			return configHome
		}
		return ""
	}

	var gotTheme *theme.EffectiveTheme
	_, err := runPickerOptionBackend(
		func() (string, error) { return configHome, nil },
		lookupEnv,
		pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
			gotTheme = options.Theme
			return intpicker.Result{Key: "enter", Value: "x"}, nil
		}),
		nil,
		intpickercompat.Options{UI: "sessions"},
	)
	if err != nil {
		t.Fatalf("runPickerOptionBackend() error = %v", err)
	}
	if gotTheme == nil {
		t.Fatal("native picker Theme = nil, want global-config effective theme")
	}
	if gotTheme.Background.Source != theme.SourceGlobal || gotTheme.Background.Value.Hex != "#010203" {
		t.Fatalf("Background = %#v, want global #010203 (explicit global [theme] must theme an unset picker)", gotTheme.Background)
	}
	if gotTheme.Surface.Value.Hex != "#040506" {
		t.Fatalf("Surface = %q, want global #040506", gotTheme.Surface.Value.Hex)
	}
}
