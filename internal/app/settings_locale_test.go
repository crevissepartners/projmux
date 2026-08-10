package app

import (
	"path/filepath"
	"testing"
)

// TestProjmuxFooterHonorsGlobalConfigLocale reproduces the live bug: a user
// pinned `[ui] locale = "en-US"` in the global config while their terminal's
// ambient LANG is ko_KR.UTF-8. Picker footers (and other package-level eager
// localizations that route through settingsLocale) must render English,
// because the config locale outranks LANG. Before the fix settingsLocale
// resolved with a nil homeDir, which skipped the global config override and
// fell through to LANG, rendering Korean footers even when the resolved app
// locale is en-US.
//
// The config is injected via XDG_CONFIG_HOME so the test does not depend on the
// developer's real ~/.config. t.Setenv forbids t.Parallel here.
func TestProjmuxFooterHonorsGlobalConfigLocale(t *testing.T) {
	configHome := t.TempDir()
	writeFile(t, filepath.Join(configHome, "projmux", "config.toml"), `
[ui]
locale = "en-US"
`)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("LANG", "ko_KR.UTF-8")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("PROJMUX_LOCALE", "")

	const englishFooter = "Enter: apply  |  Back row: parent"
	if got := projmuxFooter(englishFooter); got != englishFooter {
		t.Fatalf("projmuxFooter = %q, want English %q (config [ui] locale=en-US must outrank ambient LANG=ko_KR)", got, englishFooter)
	}
}

// TestProjmuxFooterLocalizesWhenConfigKorean is the companion control: with the
// config pinned to ko-KR the same registered footer must localize to Korean,
// proving settingsLocale actually resolves and translates (so the test above is
// guarding behavior, not a no-op).
func TestProjmuxFooterLocalizesWhenConfigKorean(t *testing.T) {
	configHome := t.TempDir()
	writeFile(t, filepath.Join(configHome, "projmux", "config.toml"), `
[ui]
locale = "ko-KR"
`)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("PROJMUX_LOCALE", "")

	const englishFooter = "Enter: apply  |  Back row: parent"
	if got := projmuxFooter(englishFooter); got == englishFooter {
		t.Fatalf("projmuxFooter = %q, want Korean translation (config [ui] locale=ko-KR must localize the registered footer)", got)
	}
}
