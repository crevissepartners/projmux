package app

import (
	"path/filepath"
	"testing"
)

// TestRelativeXDGHomesCountAsUnsetAtEverySite pins the sites that resolved an
// XDG base directory by hand before config.Resolve*Home took over: a blank or
// relative value falls back under HOME instead of landing relative to the
// working directory.
func TestRelativeXDGHomesCountAsUnsetAtEverySite(t *testing.T) {
	home := t.TempDir()
	homeDir := func() (string, error) { return home, nil }

	for _, value := range []string{" ", "rel", "./rel", "~/x"} {
		lookup := func(key string) string {
			switch key {
			case "XDG_STATE_HOME", "XDG_DATA_HOME":
				return value
			}
			return ""
		}

		hint, err := nativeKeysConsentHintPath(lookup, homeDir)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(home, ".local", "state", "projmux", nativeKeysConsentStateFileName); hint != want {
			t.Errorf("XDG_STATE_HOME=%q: native keys hint path = %q, want %q", value, hint, want)
		}

		welcome, err := (&welcomeCommand{homeDir: homeDir, lookupEnv: lookup}).welcomeStatePath("1.2.3")
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(home, ".local", "state", "projmux", welcomeStateFileName("1.2.3")); welcome != want {
			t.Errorf("XDG_STATE_HOME=%q: welcome state path = %q, want %q", value, welcome, want)
		}

		icons := (&aiCommand{homeDir: homeDir, lookupEnv: lookup}).notificationIconDir()
		if want := filepath.Join(home, ".local", "share", "projmux", "icons"); icons != want {
			t.Errorf("XDG_DATA_HOME=%q: notification icon dir = %q, want %q", value, icons, want)
		}

		t.Setenv("HOME", home)
		t.Setenv("XDG_CACHE_HOME", value)
		cache, err := defaultUpdateCacheDir()
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(home, ".cache", "projmux"); cache != want {
			t.Errorf("XDG_CACHE_HOME=%q: update cache dir = %q, want %q", value, cache, want)
		}
	}
}
