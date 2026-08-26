package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStatusbarVisibilityDefaultSavedInvalidAndRoundTrip(t *testing.T) {
	t.Parallel()

	paths := Paths{ConfigDir: filepath.Join(t.TempDir(), "config", AppName)}
	path := paths.StatusbarNotificationsHUDVisibilityFile()
	if got, want := path, filepath.Join(paths.ConfigDir, StatusbarNotificationsHUDVisibilityFileName); got != want {
		t.Fatalf("notifications HUD path = %q, want %q", got, want)
	}
	if got, want := paths.StatusbarAgentUsageHUDVisibilityFile(), filepath.Join(paths.ConfigDir, StatusbarAgentUsageHUDVisibilityFileName); got != want {
		t.Fatalf("agent usage HUD path = %q, want %q", got, want)
	}
	for got, leaf := range map[string]string{
		paths.StatusbarAgentUsageProviderVisibilityFile("claude"):              "statusbar-visibility-agent-usage-provider-claude",
		paths.StatusbarAgentUsageProviderVisibilityFile("codex"):               "statusbar-visibility-agent-usage-provider-codex",
		paths.StatusbarAgentUsageProviderVisibilityFile("antigravity"):         "statusbar-visibility-agent-usage-provider-antigravity",
		paths.StatusbarAgentUsageWindowVisibilityFile("claude", "5h"):          "statusbar-visibility-agent-usage-window-claude-5h",
		paths.StatusbarAgentUsageWindowVisibilityFile("claude", "weekly"):      "statusbar-visibility-agent-usage-window-claude-weekly",
		paths.StatusbarAgentUsageWindowVisibilityFile("codex", "5h"):           "statusbar-visibility-agent-usage-window-codex-5h",
		paths.StatusbarAgentUsageWindowVisibilityFile("codex", "weekly"):       "statusbar-visibility-agent-usage-window-codex-weekly",
		paths.StatusbarAgentUsageWindowVisibilityFile("antigravity", "weekly"): "statusbar-visibility-agent-usage-window-antigravity-weekly",
	} {
		if want := filepath.Join(paths.ConfigDir, leaf); got != want {
			t.Fatalf("visibility leaf path = %q, want %q", got, want)
		}
	}
	for name, got := range map[string]string{
		StatusbarProjectVisibilityFileName:          paths.StatusbarProjectVisibilityFile(),
		StatusbarWorkingDirectoryVisibilityFileName: paths.StatusbarWorkingDirectoryVisibilityFile(),
		StatusbarGitVisibilityFileName:              paths.StatusbarGitVisibilityFile(),
		StatusbarClockVisibilityFileName:            paths.StatusbarClockVisibilityFile(),
		StatusbarSettingsLauncherVisibilityFileName: paths.StatusbarSettingsLauncherVisibilityFile(),
	} {
		if want := filepath.Join(paths.ConfigDir, name); got != want {
			t.Fatalf("%s path = %q, want %q", name, got, want)
		}
	}

	state, err := LoadStatusbarVisibilityFile(path)
	if err != nil {
		t.Fatalf("LoadStatusbarVisibilityFile(missing) error = %v", err)
	}
	if state.Effective != StatusbarVisibilityOn || state.Source != StatusbarVisibilitySourceDefault || state.Saved != "" || state.Invalid != "" {
		t.Fatalf("missing state = %#v, want on/default with no saved value", state)
	}

	offDefaultPath := paths.StatusbarAgentUsageWindowVisibilityFile("codex", "5h")
	offDefault, err := LoadStatusbarVisibilityFileWithDefault(offDefaultPath, StatusbarVisibilityOff)
	if err != nil || offDefault.Effective != StatusbarVisibilityOff || offDefault.Source != StatusbarVisibilitySourceDefault {
		t.Fatalf("missing off-default state = %#v, %v; want off/default", offDefault, err)
	}
	if err := os.MkdirAll(filepath.Dir(offDefaultPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(offDefaultPath, []byte("broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	offDefault, err = LoadStatusbarVisibilityFileWithDefault(offDefaultPath, StatusbarVisibilityOff)
	if err != nil || offDefault.Effective != StatusbarVisibilityOff || offDefault.Source != StatusbarVisibilitySourceDefault || offDefault.Invalid != "broken" {
		t.Fatalf("invalid off-default state = %#v, %v; want off/default with invalid projection", offDefault, err)
	}
	if err := SaveStatusbarVisibilityFile(offDefaultPath, StatusbarVisibilityOn); err != nil {
		t.Fatal(err)
	}
	offDefault, err = LoadStatusbarVisibilityFileWithDefault(offDefaultPath, StatusbarVisibilityOff)
	if err != nil || offDefault.Effective != StatusbarVisibilityOn || offDefault.Source != StatusbarVisibilitySourceSaved || offDefault.Saved != "on" {
		t.Fatalf("saved on over off-default state = %#v, %v; want on/saved", offDefault, err)
	}

	if err := SaveStatusbarVisibilityFile(path, StatusbarVisibilityOff); err != nil {
		t.Fatalf("SaveStatusbarVisibilityFile(off) error = %v", err)
	}
	state, err = LoadStatusbarVisibilityFile(path)
	if err != nil {
		t.Fatalf("LoadStatusbarVisibilityFile(saved) error = %v", err)
	}
	if state.Effective != StatusbarVisibilityOff || state.Source != StatusbarVisibilitySourceSaved || state.Saved != "off" || state.Invalid != "" {
		t.Fatalf("saved state = %#v, want off/saved", state)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("saved file mode = %v, %v; want 0600", info, err)
	}

	if err := os.WriteFile(path, []byte("sometimes\n"), 0o644); err != nil {
		t.Fatalf("write invalid visibility: %v", err)
	}
	state, err = LoadStatusbarVisibilityFile(path)
	if err != nil {
		t.Fatalf("LoadStatusbarVisibilityFile(invalid) error = %v", err)
	}
	if state.Effective != StatusbarVisibilityOn || state.Source != StatusbarVisibilitySourceDefault || state.Saved != "" || state.Invalid != "sometimes" {
		t.Fatalf("invalid state = %#v, want on/default with invalid projection", state)
	}

	if err := SaveStatusbarVisibilityFile(path, StatusbarVisibility("invalid")); err != nil {
		t.Fatalf("SaveStatusbarVisibilityFile(invalid) error = %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read normalized visibility: %v", err)
	}
	if got, want := string(content), "on\n"; got != want {
		t.Fatalf("normalized saved bytes = %q, want %q", got, want)
	}
}
