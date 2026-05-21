package app

import (
	"bytes"
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
		Background:    "#ff0000",
		SurfaceActive: "#0000ff",
		Foreground:    "#00ff00",
	}
	projectCfg := theme.ThemeConfig{
		Background:    "#010203",
		SurfaceActive: "#040506",
		Foreground:    "#aabbcc",
	}
	source := newRenderThemeSource(theme.ResolveTheme(globalCfg, projectCfg))

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
			t.Fatalf("picker frame = %q, want project token %q", renderedPicker, want)
		}
	}
	for _, banned := range []string{"\x1b[48;2;255;0;0m", "\x1b[38;2;0;255;0m"} {
		if strings.Contains(renderedPicker, banned) {
			t.Fatalf("picker frame = %q, leaked global token %q", renderedPicker, banned)
		}
	}

	tmuxTokens := theme.TmuxRenderTokensFromEffective(*pickerOptions.Theme)
	tmuxConfig := source.tmuxAppConfig("/tmp/projmux", "/bin/sh", statusbarDecorationSetFromGlobal(config.StatusbarDecorationOff), defaultKeyBindingCatalog(), false)
	for _, want := range []string{
		"set -g status-style \"bg=" + tmuxTokens.StatusBg + ",fg=" + tmuxTokens.StatusFg + "\"",
		"#[fg=" + tmuxTokens.WindowInactiveFg + ",bg=" + tmuxTokens.WindowInactiveBg + "] #('/tmp/projmux' attention window #{window_id})",
		"#[bold,fg=" + tmuxTokens.WindowActiveFg + ",bg=" + tmuxTokens.WindowActiveBg + "] #('/tmp/projmux' attention window #{window_id})",
	} {
		if !strings.Contains(tmuxConfig, want) {
			t.Fatalf("tmux config missing shared theme token %q\n%s", want, tmuxConfig)
		}
	}

	globalTokens := theme.TmuxRenderTokensFromEffective(theme.ResolveTheme(theme.ThemeConfig{}, globalCfg))
	for _, banned := range []string{
		"bg=" + globalTokens.StatusBg + ",fg=" + globalTokens.StatusFg,
		"fg=" + globalTokens.WindowInactiveFg + ",bg=" + globalTokens.WindowInactiveBg,
		"fg=" + globalTokens.WindowActiveFg + ",bg=" + globalTokens.WindowActiveBg,
	} {
		if strings.Contains(tmuxConfig, banned) {
			t.Fatalf("tmux config leaked global token %q\n%s", banned, tmuxConfig)
		}
	}
}

func TestRenderThemeSourceFallbackMatchesCurrentProductionOutput(t *testing.T) {
	t.Parallel()

	source := fallbackRenderThemeSource()
	fontApplication := theme.EvaluateFontApplication(source.effective, theme.NoFontCapability())
	if fontApplication.Status != theme.FontApplyNotRequested {
		t.Fatalf("fallback font application = %#v, want not requested", fontApplication)
	}
	got := source.tmuxStandaloneConfig("/tmp/projmux", statusbarDecorationSetFromGlobal(config.StatusbarDecorationOff), defaultKeyBindingCatalog(), false)
	want := tmuxStandaloneConfigWithKeymapTheme("/tmp/projmux", statusbarDecorationSetFromGlobal(config.StatusbarDecorationOff), defaultKeyBindingCatalog(), false, theme.ResolveTheme(theme.ThemeConfig{}, theme.ThemeConfig{}))
	if got != want {
		t.Fatalf("fallback render source standalone config changed\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
