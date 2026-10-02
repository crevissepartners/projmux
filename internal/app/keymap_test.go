package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/app/keybinding"
	"slices"
)

func TestRenamePaneLabelCatalogExcludesRetiredTopicAlias(t *testing.T) {
	t.Parallel()

	catalog := keybinding.DefaultKeyBindingCatalog()
	canonical, ok := keybinding.KeyBindingActionByID(catalog, keybinding.PaneRenameActionID)
	if !ok {
		t.Fatalf("catalog missing %s", keybinding.PaneRenameActionID)
	}
	if alias, ok := keybinding.KeyBindingActionByID(catalog, keybinding.RetiredPaneRenameActionID); ok {
		t.Fatalf("retired action %s unexpectedly resolves to %#v", keybinding.RetiredPaneRenameActionID, alias)
	}
	for _, alias := range canonical.Aliases {
		if alias == keybinding.RetiredPaneRenameActionID {
			t.Fatalf("canonical aliases = %#v, did not want retired action %s", canonical.Aliases, keybinding.RetiredPaneRenameActionID)
		}
	}
	if keybinding.KeyBindingDisplayName(canonical) != "Rename Pane" || canonical.ProbeLabel != "Ctrl-Shift-M" {
		t.Fatalf("rename action display/default = (%q, %q), want Rename Pane and unchanged Ctrl-Shift-M", keybinding.KeyBindingDisplayName(canonical), canonical.ProbeLabel)
	}
	// tmux invokes the body only when command-prompt confirms. Esc is therefore
	// a native cancellation with no alternate/direct action body to run.
	// The confirmed body reaches the Registry rename route; the label changes
	// only as that rename's mirror.
	if canonical.TmuxKind != keybinding.TmuxBindingPromptRunProjmux || len(canonical.TmuxBodyAliases) != 0 {
		t.Fatalf("rename action prompt contract = kind %q aliases %#v, want a native command-prompt running the projmux route with no alternate body", canonical.TmuxKind, canonical.TmuxBodyAliases)
	}
	body := keybinding.RenderTmuxBindingBody("/tmp/projmux", canonical)
	if strings.Contains(body, "set-option") {
		t.Fatalf("rename pane binding = %q, writes a tmux option directly instead of renaming through the Registry", body)
	}
	for _, want := range []string{"command-prompt", `-p "pane label:"`, `-I "#{@projmux_pane_label}"`, "internal tmux pane-rename --client #{client_tty} --anchor #{pane_id} --name-stdin"} {
		if !strings.Contains(body, want) {
			t.Fatalf("rename pane binding = %q, want %q", body, want)
		}
	}
	for _, forbidden := range []string{"select-pane -T", aiPaneTopicOption, aiPaneTopicManualOption, "#{pane_title}"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("rename pane binding = %q, forbidden write/initial source %q", body, forbidden)
		}
	}
}

func TestPopupToggleModeCloseKeysUseMappedActionKeymapAndIgnoreDirectCommands(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	keymapPath := filepath.Join(home, ".config", "projmux", "keymap.toml")
	if err := os.MkdirAll(filepath.Dir(keymapPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keymapPath, []byte(`[bindings."RecentWindows:Open"]
keys = ["M-r"]
[bindings.AISplitPickerToggle]
keys = ["M-a"]
[bindings.SettingsToggle]
keys = ["M-s"]
[bindings.ProjectSidebarToggle]
keys = ["M-p"]
[bindings.NotifySidebarToggle]
keys = ["M-n"]
[bindings.ProjectSwitcherToggle]
keys = ["M-j"]
[bindings.SessionPopupToggle]
keys = ["M-u"]
[bindings.new-window]
keys = ["M-t"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	homeDir := func() (string, error) { return home, nil }
	lookupEnv := func(string) string { return "" }
	for _, tc := range []struct {
		mode string
		want string
	}{
		{mode: "sessionizer-sidebar", want: "alt-p"},
		{mode: "notify-sidebar", want: "alt-n"},
		{mode: "recent-windows", want: "alt-r"},
		{mode: "ai-split-picker-right", want: "alt-a"},
		{mode: "ai-split-picker-down", want: "alt-a"},
		{mode: "ai-split-settings", want: "alt-s"},
		{mode: "sessionizer", want: "alt-j"},
		{mode: "session-popup", want: "alt-u"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			keys := effectivePickerKeysForPopupToggleMode(homeDir, lookupEnv, tc.mode, []string{"esc"})
			if !slices.Contains(keys, "esc") || !slices.Contains(keys, tc.want) {
				t.Fatalf("%s close keys = %#v, want esc and %s", tc.mode, keys, tc.want)
			}
			for _, leaked := range []string{"alt-p", "alt-n", "alt-r", "alt-a", "alt-s", "alt-j", "alt-u", "alt-t"} {
				if leaked == tc.want {
					continue
				}
				if slices.Contains(keys, leaked) {
					t.Fatalf("%s close keys = %#v, did not want leaked key %s", tc.mode, keys, leaked)
				}
			}
		})
	}
}

func TestKeybindingDocsDoNotAdvertiseRetiredDefaults(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"README.md", "docs/keybindings.md", "docs/configuration.md", "docs/statusbar.md", "docs/notify-queue.md"} {
		body := readRepoText(t, path)
		for _, stale := range []string{
			"Alt-6` |",
			"Ctrl-N` |",
			"Alt-r` |",
			"can be inspected or rebound in Settings",
			"surfaced in Settings > Keybindings as",
		} {
			if strings.Contains(body, stale) {
				t.Fatalf("%s contains stale keybinding guide %q", path, stale)
			}
		}
	}
}

func readRepoText(t *testing.T, rel string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	raw, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(raw)
}
