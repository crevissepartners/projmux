package app

import (
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/theme"
)

// withNativeUITheme applies the given effective theme to the native-UI role
// escapes, runs fn, then restores the built-in fallback palette — all under
// nativeUIThemeMu so it does not race the (test-gated) config-read apply path.
// It lets a test exercise the explicit-theme repaint deterministically.
func withNativeUITheme(effective theme.EffectiveTheme, fn func()) {
	nativeUIThemeMu.Lock()
	defer nativeUIThemeMu.Unlock()
	applyNativeUIThemeLocked(effective)
	defer resetNativeUIThemeLocked()
	fn()
}

// TestNativeUIThemeRepaintsSettingsRowOnExplicitTheme proves that an explicit
// global theme repaints the app-package native-UI surfaces: a settings row built
// while an explicit accent is applied carries the derived truecolor escape, not
// the historical fallback literal. withNativeUITheme restores the fallback
// palette afterwards so other tests are unaffected.
//
// NOTE: this test is intentionally NOT t.Parallel(). It mutates the shared
// native-UI role vars under nativeUIThemeMu; keeping it serial avoids contending
// with parallel tests that read those vars without the lock.
func TestNativeUIThemeRepaintsSettingsRowOnExplicitTheme(t *testing.T) {
	explicit := theme.ResolveTheme(theme.ThemeConfig{Accent: "#00ddaa"})
	derivedAccent := "\x1b[38;2;0;221;170m"

	withNativeUITheme(explicit, func() {
		if settingsColorAdd != derivedAccent {
			t.Fatalf("settingsColorAdd = %q, want derived accent %q", settingsColorAdd, derivedAccent)
		}
		label := settingsLabel(settingsGlyphAdd, settingsColorAdd, "Add Project...", "scan filesystem roots")
		if !strings.Contains(label, derivedAccent) {
			t.Fatalf("settings row = %q, want derived accent escape %q", label, derivedAccent)
		}
		if strings.Contains(label, theme.ANSIAccentActionStart) {
			t.Fatalf("settings row = %q, still contains fallback accent literal %q", label, theme.ANSIAccentActionStart)
		}
	})

	// Restored to the fallback literal after the scope.
	if settingsColorAdd != theme.ANSIAccentActionStart {
		t.Fatalf("settingsColorAdd not restored: %q, want %q", settingsColorAdd, theme.ANSIAccentActionStart)
	}
}
