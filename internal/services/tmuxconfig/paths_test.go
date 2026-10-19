package tmuxconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/services/keybinding"
)

// Captured ConfigDir is a directory value, not an XDG home. Every generation
// loader must consume that value without consulting a live home/environment.
func TestGenerationConsumesExactCapturedPaths(t *testing.T) {
	root := t.TempDir()
	paths := config.Paths{ConfigDir: filepath.Join(root, "arbitrary-settings-basename"), StateDir: filepath.Join(root, "saved-state")}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(paths.ConfigDir, 0700))
	must(config.SaveAIBadgeStyleFile(paths.AIBadgeStyleFile(), config.AIBadgeStyleEmoji))
	must(config.SaveStatusbarDecorationFile(paths.StatusbarDecorationFile(), config.StatusbarDecorationEmoji))
	must(config.SaveDesktopNotifyModeFile(paths.DesktopNotifyModeFile(), config.DesktopNotifyModeNotify))
	must(config.SaveStatusbarVisibilityFile(paths.StatusbarGitVisibilityFile(), config.StatusbarVisibilityOff))
	catalog := keybinding.DefaultKeyBindingCatalog()
	action, ok := keybinding.KeyBindingActionByID(catalog, "ProjectSidebarToggle")
	if !ok {
		t.Fatal("missing project sidebar action")
	}
	keymap := keybinding.KeymapFile{SchemaVersion: keybinding.KeymapSchemaVersion, Bindings: map[string]keybinding.KeymapOverride{action.CanonicalID: {Keys: []string{"M-z"}, KeysSet: true}}}
	must(os.WriteFile(paths.KeymapFile(), []byte(keybinding.RenderKeymapFile(keymap)), 0600))
	home := func() (string, error) { t.Fatal("generation reconstructed a captured home"); return "", nil }
	env := func(key string) string {
		if key != "SHELL" {
			t.Fatalf("generation consulted ambient path input %q", key)
		}
		return "/bin/zsh"
	}
	calls := 0
	resolve := func(func() (string, error), func(string) string) (config.Paths, error) { calls++; return paths, nil }
	if got := LoadAIBadgeStyle(home, env, resolve); got != config.AIBadgeStyleEmoji {
		t.Fatalf("badge=%q", got)
	}
	if got := LoadStatusbarDecorationSet(home, env, resolve); got.Cwd != config.StatusbarDecorationEmoji {
		t.Fatalf("decoration=%+v", got)
	}
	if got := LoadStatusbarRowOneVisibilitySet(home, env, resolve); got.Git != config.StatusbarVisibilityOff {
		t.Fatalf("row one=%+v", got)
	}
	generation := Generation{HomeDir: home, LookupEnv: env, ResolvePaths: resolve, ReadFile: os.ReadFile, KeymapPaths: func() (string, error) { return paths.KeymapFile(), nil }}
	tmuxPath, shellPath := filepath.Join(root, "tmux.conf"), filepath.Join(root, "shell.conf")
	_, err := WriteTmuxAppConfig(tmuxPath, "/fixed/projmux", generation, os.WriteFile)
	must(err)
	must(WriteShellAppConfig(shellPath, "/fixed/projmux", generation, os.WriteFile))
	tmuxBytes, err := os.ReadFile(tmuxPath)
	must(err)
	shellBytes, err := os.ReadFile(shellPath)
	must(err)
	if string(tmuxBytes) != string(shellBytes) {
		t.Fatal("writer surfaces generated different bytes for captured inputs")
	}
	for _, literal := range []string{"set -g @projmux_ai_badge_style emoji", "bind-key -n M-z", "set -g default-shell \"/bin/zsh\""} {
		if !strings.Contains(string(tmuxBytes), literal) {
			t.Fatalf("captured generation missing %q", literal)
		}
	}
	if calls == 0 {
		t.Fatal("captured paths resolver was unused")
	}
}
