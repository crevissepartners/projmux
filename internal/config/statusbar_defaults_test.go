package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeStatusbarTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestLoadLayeredStatusbarVisibilityResolvesTUIThenCentralThenDefault holds
// the order one leaf resolves in: a valid TUI value, then the central default,
// then the caller's default.
func TestLoadLayeredStatusbarVisibilityResolvesTUIThenCentralThenDefault(t *testing.T) {
	for _, tc := range []struct {
		name        string
		tui         *string
		central     string
		def         StatusbarVisibility
		want        StatusbarVisibility
		wantSource  StatusbarVisibilitySource
		wantInvalid string
	}{
		{name: "tui wins over central", tui: ptr("off\n"), central: `{"visibility":{"clock":"on"}}`, def: StatusbarVisibilityOn, want: StatusbarVisibilityOff, wantSource: StatusbarVisibilitySourceSaved},
		{name: "central when tui missing", central: `{"visibility":{"clock":"off"}}`, def: StatusbarVisibilityOn, want: StatusbarVisibilityOff, wantSource: StatusbarVisibilitySourceCentral},
		{name: "central when tui empty", tui: ptr(""), central: `{"visibility":{"clock":"off"}}`, def: StatusbarVisibilityOn, want: StatusbarVisibilityOff, wantSource: StatusbarVisibilitySourceCentral},
		{name: "central when tui invalid keeps invalid", tui: ptr("maybe"), central: `{"visibility":{"clock":"off"}}`, def: StatusbarVisibilityOn, want: StatusbarVisibilityOff, wantSource: StatusbarVisibilitySourceCentral, wantInvalid: "maybe"},
		{name: "default when both missing", def: StatusbarVisibilityOn, want: StatusbarVisibilityOn, wantSource: StatusbarVisibilitySourceDefault},
		{name: "caller default when both missing", def: StatusbarVisibilityOff, want: StatusbarVisibilityOff, wantSource: StatusbarVisibilitySourceDefault},
		{name: "invalid central value ignored", central: `{"visibility":{"clock":"maybe"}}`, def: StatusbarVisibilityOn, want: StatusbarVisibilityOn, wantSource: StatusbarVisibilitySourceDefault},
		{name: "malformed central file ignored", central: `{`, def: StatusbarVisibilityOff, want: StatusbarVisibilityOff, wantSource: StatusbarVisibilitySourceDefault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := DefaultPaths(filepath.Join(t.TempDir(), "config"), filepath.Join(t.TempDir(), "state"))
			if tc.tui != nil {
				writeStatusbarTestFile(t, paths.StatusbarClockVisibilityFile(), *tc.tui)
			}
			if tc.central != "" {
				writeStatusbarTestFile(t, paths.StatusbarDefaultsFile(), tc.central)
			}
			got, err := LoadLayeredStatusbarVisibility(paths.StatusbarClockVisibilityFile(), paths.StatusbarDefaultsFile(), tc.def)
			if err != nil {
				t.Fatal(err)
			}
			if got.Effective != tc.want || got.Source != tc.wantSource || got.Invalid != tc.wantInvalid {
				t.Fatalf("got %+v, want effective %s source %s invalid %q", got, tc.want, tc.wantSource, tc.wantInvalid)
			}
		})
	}
}

func ptr(s string) *string { return &s }

// TestSettingsLauncherHasNoCentralDefault keeps the settings launcher TUI only:
// a central value under its key never decides it.
func TestSettingsLauncherHasNoCentralDefault(t *testing.T) {
	paths := DefaultPaths(filepath.Join(t.TempDir(), "config"), "")
	if _, ok := StatusbarDefaultKey(paths.StatusbarSettingsLauncherVisibilityFile()); ok {
		t.Fatal("the settings launcher has a central key")
	}
	writeStatusbarTestFile(t, paths.StatusbarDefaultsFile(), `{"visibility":{"settings-launcher":"off"}}`)
	got, err := LoadLayeredStatusbarVisibility(paths.StatusbarSettingsLauncherVisibilityFile(), paths.StatusbarDefaultsFile(), StatusbarVisibilityOn)
	if err != nil || got.Effective != StatusbarVisibilityOn || got.Source != StatusbarVisibilitySourceDefault {
		t.Fatalf("settings launcher = %+v, %v; want the built-in default", got, err)
	}
	for path, want := range map[string]string{
		paths.StatusbarNotificationsHUDVisibilityFile():              "notifications-hud",
		paths.StatusbarAgentUsageHUDVisibilityFile():                 "agent-usage-hud",
		paths.StatusbarAgentUsageProviderVisibilityFile("codex"):     "agent-usage-provider-codex",
		paths.StatusbarAgentUsageWindowVisibilityFile("codex", "5h"): "agent-usage-window-codex-5h",
		paths.StatusbarProjectVisibilityFile():                       "project",
		paths.StatusbarWorkingDirectoryVisibilityFile():              "working-directory",
		paths.StatusbarGitVisibilityFile():                           "git",
		paths.StatusbarClockVisibilityFile():                         "clock",
		filepath.Join(paths.ConfigDir, StatusbarDecorationFileName):  "",
		filepath.Join(paths.ConfigDir, "statusbar-visibility-"):      "",
	} {
		got, ok := StatusbarDefaultKey(path)
		if got != want || ok != (want != "") {
			t.Errorf("StatusbarDefaultKey(%s) = %q, %v; want %q", filepath.Base(path), got, ok, want)
		}
	}
}

// TestSeedStatusbarDefaultsAddsOnlyMissingKeys holds the copy to per-key,
// never-overwriting writes, and to no write at all when nothing is new.
func TestSeedStatusbarDefaultsAddsOnlyMissingKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", StatusbarDefaultsFileName)
	writeStatusbarTestFile(t, path, `{"visibility":{"clock":"on","git":"bogus"}}`)

	added, err := SeedStatusbarDefaults(path, map[string]StatusbarVisibility{
		"clock":   StatusbarVisibilityOff,
		"git":     StatusbarVisibilityOff,
		"project": StatusbarVisibilityOff,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"git", "project"}; !slices.Equal(added, want) {
		t.Fatalf("added = %q, want %q", added, want)
	}
	got, err := LoadStatusbarDefaults(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]StatusbarVisibility{"clock": StatusbarVisibilityOn, "git": StatusbarVisibilityOff, "project": StatusbarVisibilityOff}
	if len(got) != len(want) {
		t.Fatalf("defaults = %v, want %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("defaults = %v, want %v", got, want)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}

	before, _ := os.ReadFile(path)
	added, err = SeedStatusbarDefaults(path, map[string]StatusbarVisibility{"clock": StatusbarVisibilityOff, "project": StatusbarVisibilityOn})
	if err != nil || len(added) != 0 {
		t.Fatalf("second seed added %q, %v; want nothing", added, err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatalf("second seed rewrote the file:\nbefore=%s\nafter=%s", before, after)
	}

	empty := filepath.Join(t.TempDir(), StatusbarDefaultsFileName)
	if added, err := SeedStatusbarDefaults(empty, nil); err != nil || len(added) != 0 {
		t.Fatalf("empty seed = %q, %v", added, err)
	}
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Fatalf("an empty seed created %s (%v)", empty, err)
	}
}

// TestStatusbarDefaultsIsDeclaredCentral ties the new file to the central
// layer and keeps its seed record out of the settings.
func TestStatusbarDefaultsIsDeclaredCentral(t *testing.T) {
	item, ok := SettingForFile(StatusbarDefaultsFileName)
	if !ok || item.Layer != LayerCentral {
		t.Fatalf("SettingForFile(%s) = %+v, %v; want a central setting", StatusbarDefaultsFileName, item, ok)
	}
	paths := DefaultPaths("/x/config", "/x/state")
	if got, want := paths.StatusbarDefaultsFile(), "/x/config/projmux/statusbar-defaults.json"; got != want {
		t.Fatalf("defaults file = %q, want %q", got, want)
	}
	if got, want := paths.StatusbarDefaultsSeedStateFile(), "/x/state/projmux/statusbar-defaults-seeded"; got != want {
		t.Fatalf("seed record = %q, want %q", got, want)
	}
}
