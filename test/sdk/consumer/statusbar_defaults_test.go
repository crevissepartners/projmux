package consumer_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

var _ func(*projmux.Client) []core.ConfigVisibilityState = (*projmux.Client).GetCentralStatusbarDefaults

// TestExternalCentralStatusbarDefaultsConsumer reads the central defaults
// through the public API alone. The read follows the captured ConfigDir,
// ignores a valid TUI leaf and the environment after NewClient, and changes no
// file, cache or environment and starts no command.
func TestExternalCentralStatusbarDefaultsConsumer(t *testing.T) {
	base := t.TempDir()
	configDir := filepath.Join(base, "arbitrary-settings-basename")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"statusbar-defaults.json":    `{"visibility":{"clock":"off","project":"maybe"}}` + "\n",
		"statusbar-visibility-clock": "on\n",
		"statusbar-visibility-git":   "off\n",
	} {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	binDir := filepath.Join(base, "bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	commandLog := filepath.Join(base, "commands.log")
	for _, name := range []string{"tmux", "projmux", "claude", "codex", "antigravity"} {
		script := "#!/bin/sh\nprintf '%s\\n' " + strconv.Quote(name) + " >> " + strconv.Quote(commandLog) + "\nexit 99\n"
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	c, err := projmux.NewClient(projmux.Options{ConfigDir: configDir, StateDir: filepath.Join(base, "state"), HomeDir: filepath.Join(base, "home")})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("HOME", filepath.Join(base, "poisoned-home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "poisoned-config"))
	t.Setenv("TMUX", "poisoned")
	t.Setenv("TMUX_PANE", "poisoned")
	before := consumerPathTree(t, base)
	environment := os.Environ()

	want := []core.ConfigVisibilityState{}
	for _, key := range []string{
		"agent-usage-hud", "agent-usage-provider-claude", "agent-usage-provider-codex",
		"agent-usage-window-claude-5h", "agent-usage-window-claude-weekly",
		"agent-usage-window-codex-5h", "agent-usage-window-codex-weekly",
		"clock", "git", "notifications-hud", "project", "working-directory",
	} {
		state := core.ConfigVisibilityState{Key: key, Effective: "on", Source: "default"}
		switch key {
		case "agent-usage-window-codex-5h":
			state.Effective = "off"
		case "clock":
			state = core.ConfigVisibilityState{Key: key, Effective: "off", Saved: "off", Source: "central"}
		}
		want = append(want, state)
	}
	got := c.GetCentralStatusbarDefaults()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("defaults =\n%+v\nwant\n%+v", got, want)
	}
	got[0].Effective = "changed"
	if again := c.GetCentralStatusbarDefaults(); !reflect.DeepEqual(again, want) {
		t.Fatalf("a caller's change leaked into the next read: %+v", again)
	}
	if _, err := os.Stat(commandLog); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected provider/tmux execution, log stat: %v", err)
	}
	if !reflect.DeepEqual(before, consumerPathTree(t, base)) || !reflect.DeepEqual(environment, os.Environ()) {
		t.Fatal("central defaults read changed files, caches or environment")
	}
}
