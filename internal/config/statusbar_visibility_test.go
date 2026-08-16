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

	state, err := LoadStatusbarVisibilityFile(path)
	if err != nil {
		t.Fatalf("LoadStatusbarVisibilityFile(missing) error = %v", err)
	}
	if state.Effective != StatusbarVisibilityOn || state.Source != StatusbarVisibilitySourceDefault || state.Saved != "" || state.Invalid != "" {
		t.Fatalf("missing state = %#v, want on/default with no saved value", state)
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
