package app

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/theme"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

func TestRenderThemeSourceFeedsPickerAndTmuxFromSameEffectiveTheme(t *testing.T) {
	t.Parallel()

	globalCfg := theme.ThemeConfig{
		Background:    "#010203",
		SurfaceActive: "#040506",
		Foreground:    "#aabbcc",
	}
	source := newRenderThemeSource(theme.ResolveTheme(globalCfg))

	pickerOptions := source.pickerOptions(intpicker.Options{Title: "Projects"})
	if pickerOptions.Theme == nil {
		t.Fatal("pickerOptions.Theme = nil, want shared effective theme")
	}
	var frame bytes.Buffer
	projmuxpicker.NewRenderer(projmuxpicker.ThemeFromEffective(*pickerOptions.Theme)).
		RenderFrameWithTitle(&frame, "api", pickerOptions.Title, projmuxpicker.Layout{Rows: 5, Cols: 20})
	renderedPicker := frame.String()
	for _, want := range []string{"\x1b[48;2;1;2;3m", "\x1b[38;2;170;187;204m"} {
		if !strings.Contains(renderedPicker, want) {
			t.Fatalf("picker frame = %q, want global token %q", renderedPicker, want)
		}
	}

	tmuxTokens := theme.TmuxRenderTokensFromEffective(*pickerOptions.Theme)
	tmuxConfig := source.tmuxAppConfig("/tmp/projmux", "/bin/sh", statusbarDecorationSetFromGlobal(config.StatusbarDecorationOff), defaultKeyBindingCatalog(), false)
	for _, want := range []string{
		"set -g status-style \"bg=" + tmuxTokens.StatusBg + ",fg=" + tmuxTokens.StatusFg + "\"",
		"#[fg=" + tmuxTokens.WindowInactiveFg + ",bg=" + tmuxTokens.WindowInactiveBg + "] #('/tmp/projmux' attention window #{window_id} #{@projmux_ai_badge_style})",
		"#[bold,fg=" + tmuxTokens.WindowActiveFg + ",bg=" + tmuxTokens.WindowActiveBg + "] #('/tmp/projmux' attention window #{window_id} #{@projmux_ai_badge_style})",
	} {
		if !strings.Contains(tmuxConfig, want) {
			t.Fatalf("tmux config missing shared theme token %q\n%s", want, tmuxConfig)
		}
	}
}

func TestRenderThemeSourceFallbackMatchesCurrentProductionOutput(t *testing.T) {
	t.Parallel()

	source := fallbackRenderThemeSource()
	got := source.tmuxStandaloneConfig("/tmp/projmux", statusbarDecorationSetFromGlobal(config.StatusbarDecorationOff), defaultKeyBindingCatalog(), false)
	want := tmuxStandaloneConfigWithKeymapTheme("/tmp/projmux", statusbarDecorationSetFromGlobal(config.StatusbarDecorationOff), defaultKeyBindingCatalog(), false, theme.ResolveTheme(theme.ThemeConfig{}))
	if got != want {
		t.Fatalf("fallback render source standalone config changed\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestConfigRenderThemeSourceIgnoresProjectTheme(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	project := filepath.Join(home, "source", "repos", "app")
	mkdirAll(t, filepath.Join(project, ".git"))
	writeFile(t, filepath.Join(home, ".config", "projmux", "config.toml"), `
[theme]
background = "#ff0000"
foreground = "#00ff00"
`)
	// A project-local [theme] is deprecated migration data: it must not bleed
	// into the effective theme. The global theme alone decides the result.
	writeFile(t, filepath.Join(project, ".projmux", "config.toml"), `
[theme]
preset = "forest"
background = "#010203"
foreground = "#aabbcc"
`)

	source, err := configRenderThemeSource(
		func() (string, error) { return home, nil },
		func(string) string { return "" },
		filepath.Join(project, "subdir"),
	)
	if err != nil {
		t.Fatalf("configRenderThemeSource() error = %v", err)
	}
	options := source.pickerOptions(intpicker.Options{Title: "Projects"})
	if options.Theme == nil {
		t.Fatal("picker options Theme = nil")
	}
	if got, want := options.Theme.Background.Value.Hex, "#ff0000"; got != want {
		t.Fatalf("picker theme background = %q, want global %q (project [theme] ignored)", got, want)
	}
	if got, want := options.Theme.Background.Source, theme.SourceGlobal; got != want {
		t.Fatalf("picker theme background source = %q, want %q", got, want)
	}

	var frame bytes.Buffer
	projmuxpicker.NewRenderer(projmuxpicker.ThemeFromEffective(*options.Theme)).
		RenderFrameWithTitle(&frame, "api", options.Title, projmuxpicker.Layout{Rows: 5, Cols: 20})
	rendered := frame.String()
	if !strings.Contains(rendered, "\x1b[48;2;255;0;0m") || !strings.Contains(rendered, "\x1b[38;2;0;255;0m") {
		t.Fatalf("popup frame = %q, want global theme background/chrome_foreground SGR", rendered)
	}
	if strings.Contains(rendered, "\x1b[48;2;1;2;3m") || strings.Contains(rendered, "\x1b[38;2;170;187;204m") {
		t.Fatalf("popup frame = %q, leaked ignored project theme", rendered)
	}
}
