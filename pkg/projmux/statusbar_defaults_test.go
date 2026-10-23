package projmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/services/instructions"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// builtInCentralStatusbarDefaults is the independent expectation with nothing
// stored: every leaf on by Key, except Codex 5h off.
func builtInCentralStatusbarDefaults() []core.ConfigVisibilityState {
	out := []core.ConfigVisibilityState{}
	for _, key := range []string{
		"agent-usage-hud", "agent-usage-provider-claude", "agent-usage-provider-codex",
		"agent-usage-window-claude-5h", "agent-usage-window-claude-weekly",
		"agent-usage-window-codex-5h", "agent-usage-window-codex-weekly",
		"clock", "git", "notifications-hud", "project", "working-directory",
	} {
		value := "on"
		if key == "agent-usage-window-codex-5h" {
			value = "off"
		}
		out = append(out, core.ConfigVisibilityState{Key: key, Effective: value, Source: "default"})
	}
	return out
}

func writeStatusbarDefaultsFile(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, config.StatusbarDefaultsFileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestGetCentralStatusbarDefaultsPathError reads an unresolvable ConfigDir as
// the built-in defaults, where the strict read returns the path error.
func TestGetCentralStatusbarDefaultsPathError(t *testing.T) {
	cause := errors.New("path resolver unavailable")
	c := &Client{instructions: instructions.Service{Paths: func() (config.Paths, error) { return config.Paths{}, cause }}}
	if got, want := c.GetCentralStatusbarDefaults(), builtInCentralStatusbarDefaults(); !reflect.DeepEqual(got, want) {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
	if v, err := c.GetConfig(context.Background(), core.ConfigStatusbarVisibility); !errors.Is(err, cause) || !reflect.DeepEqual(v, core.ConfigValue{}) {
		t.Fatalf("strict read = %+v, %v; want the empty value and the path error", v, err)
	}
}

// TestGetCentralStatusbarDefaultsUsesCapturedPaths reads only the captured
// ConfigDir, whatever its basename and the environment after NewClient.
func TestGetCentralStatusbarDefaultsUsesCapturedPaths(t *testing.T) {
	root := t.TempDir()
	captured := filepath.Join(root, "arbitrary-settings-basename")
	writeStatusbarDefaultsFile(t, captured, `{"visibility":{"clock":"off","agent-usage-window-codex-5h":"on"}}`)
	c, err := NewClient(Options{ConfigDir: captured, StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	poisoned := filepath.Join(root, "poisoned")
	writeStatusbarDefaultsFile(t, filepath.Join(poisoned, "projmux"), `{"visibility":{"clock":"on","git":"off"}}`)
	t.Setenv("HOME", poisoned)
	t.Setenv("XDG_CONFIG_HOME", poisoned)
	want := builtInCentralStatusbarDefaults()
	for i := range want {
		switch want[i].Key {
		case "clock":
			want[i] = core.ConfigVisibilityState{Key: "clock", Effective: "off", Saved: "off", Source: "central"}
		case "agent-usage-window-codex-5h":
			want[i] = core.ConfigVisibilityState{Key: "agent-usage-window-codex-5h", Effective: "on", Saved: "on", Source: "central"}
		}
	}
	if got := c.GetCentralStatusbarDefaults(); !reflect.DeepEqual(got, want) {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
}

// TestGetConfigVisibilityStrictErrorsUnchanged is acceptance 4: with a central
// file that cannot be parsed GetConfig still returns the empty value and the
// parse error, cancellation still refuses before reading, and SetConfig still
// refuses an unknown leaf, while the opt-in read falls back to the defaults.
func TestGetConfigVisibilityStrictErrorsUnchanged(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "config")
	writeStatusbarDefaultsFile(t, dir, `{"visibility":`)
	c, err := NewClient(Options{ConfigDir: dir, StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	v, err := c.GetConfig(context.Background(), core.ConfigStatusbarVisibility)
	var refusal *Refusal
	if err == nil || errors.As(err, &refusal) || !reflect.DeepEqual(v, core.ConfigValue{}) {
		t.Fatalf("strict read = %+v, %v; want the empty value and the parse error", v, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if v, err := c.GetConfig(ctx, core.ConfigStatusbarVisibility); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(v, core.ConfigValue{}) {
		t.Fatalf("cancelled strict read = %+v, %v", v, err)
	}
	if _, err := c.SetConfig(context.Background(), core.ConfigUpdate{Key: core.ConfigStatusbarVisibility, Visibility: map[string]string{"settings-launcher": "off"}}); !errors.As(err, &refusal) || refusal.Code != CodeConfigKeyInvalid {
		t.Fatalf("SetConfig settings-launcher = %v, want %s", err, CodeConfigKeyInvalid)
	}
	if got, want := c.GetCentralStatusbarDefaults(), builtInCentralStatusbarDefaults(); !reflect.DeepEqual(got, want) {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
}
