package app

import (
	"path/filepath"
	"testing"

	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// localeLookupEnv builds a lookupEnv that pins the ambient locale env rungs.
// It returns LANG=lang and empty for the higher-priority rungs so the global
// config [ui] locale override is what must win.
func localeLookupEnv(lang string) func(string) string {
	return func(name string) string {
		switch name {
		case "LANG":
			return lang
		case "PROJMUX_LOCALE", "LC_ALL", "LC_MESSAGES", "XDG_CONFIG_HOME":
			return ""
		default:
			return ""
		}
	}
}

// TestLocalizePickerOptionsHonorsConfigLocaleOverEnv reproduces the live bug a
// user hit: global config pins `[ui] locale = "en-US"` while the terminal's
// ambient LANG is ko_KR.UTF-8. The picker chrome (footers/titles) must render
// English, because the config locale outranks LANG. Before the fix the shared
// choke point resolved the locale with a nil homeDir, which skipped the config
// override and fell through to LANG, rendering Korean chrome.
func TestLocalizePickerOptionsHonorsConfigLocaleOverEnv(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "projmux", "config.toml"), `
[ui]
locale = "en-US"
`)
	homeDir := func() (string, error) { return home, nil }

	const englishFooter = "Enter: apply  |  Back row: parent"
	got := localizePickerOptions(homeDir, localeLookupEnv("ko_KR.UTF-8"), intpickercompat.Options{
		Footer: englishFooter,
	})
	if got.Footer != englishFooter {
		t.Fatalf("footer = %q, want English %q (config [ui] locale=en-US must outrank ambient LANG=ko_KR)", got.Footer, englishFooter)
	}
	if got.Locale != "en-US" {
		t.Fatalf("resolved locale = %q, want en-US from global config override", got.Locale)
	}
}

// TestLocalizePickerOptionsLocalizesWhenConfigKorean is the companion control:
// with the config pinned to ko-KR the same registered footer must localize to
// Korean, proving the choke point actually translates (so the test above is
// guarding behavior, not a no-op path).
func TestLocalizePickerOptionsLocalizesWhenConfigKorean(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "projmux", "config.toml"), `
[ui]
locale = "ko-KR"
`)
	homeDir := func() (string, error) { return home, nil }

	const englishFooter = "Enter: apply  |  Back row: parent"
	got := localizePickerOptions(homeDir, localeLookupEnv("en_US.UTF-8"), intpickercompat.Options{
		Footer: englishFooter,
	})
	if got.Locale != "ko-KR" {
		t.Fatalf("resolved locale = %q, want ko-KR from global config override", got.Locale)
	}
	if got.Footer == englishFooter {
		t.Fatalf("footer = %q, want Korean translation (config [ui] locale=ko-KR must localize the registered footer)", got.Footer)
	}
}
