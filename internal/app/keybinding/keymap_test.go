package keybinding

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
)

func TestCurrentProjectSessionQuotesPaneCWDAsOneLiteralShellArgv(t *testing.T) {
	t.Parallel()

	action, ok := KeyBindingActionByID(DefaultKeyBindingCatalog(), "current-project-session")
	if !ok {
		t.Fatal("catalog missing current-project-session")
	}
	if got, want := action.TmuxBody, "switch open #{q:pane_current_path}"; got != want {
		t.Fatalf("tmux body = %q, want %q", got, want)
	}

	root := t.TempDir()
	binDir := filepath.Join(root, "fake bin's")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir fake binary directory: %v", err)
	}
	bin := filepath.Join(binDir, "projmux")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\0' \"$@\" > \"$PROJMUX_CAPTURE\"\n"), 0o700); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	rendered := RenderTmuxBindingBody(bin, action)
	const prefix = `run-shell "`
	const suffix = `"`
	if !strings.HasPrefix(rendered, prefix) || !strings.HasSuffix(rendered, suffix) {
		t.Fatalf("rendered binding = %q, want one run-shell config string", rendered)
	}
	commandTemplate := strings.TrimSuffix(strings.TrimPrefix(rendered, prefix), suffix)
	if got := strings.Count(commandTemplate, "#{q:pane_current_path}"); got != 1 {
		t.Fatalf("rendered command = %q, q-modified cwd tokens = %d, want 1", commandTemplate, got)
	}
	if strings.Contains(commandTemplate, `"#{pane_current_path}"`) {
		t.Fatalf("rendered command = %q, contains unsafe quote-wrapped raw cwd format", commandTemplate)
	}

	paths := []string{
		"/tmp/work tree",
		"/tmp/single'quote",
		`/tmp/double"quote`,
		"/tmp/$(touch PWN_DOLLAR)",
		"/tmp/`touch PWN_TICK`",
		"/tmp/semi; touch PWN_SEMI & echo injected | cat > PWN_REDIRECT",
	}
	for i, cwd := range paths {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			capture := filepath.Join(root, "argv-"+string(rune('a'+i)))
			// tmux's q modifier emits a shell-escaped format value. Substitute
			// the equivalent production quoting here, then execute the exact
			// rendered run-shell command to prove its argv boundary.
			command := strings.Replace(commandTemplate, "#{q:pane_current_path}", TmuxShellQuote(cwd), 1)
			cmd := exec.Command("sh", "-c", command)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PROJMUX_CAPTURE="+capture)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("execute rendered command: %v\n%s", err, output)
			}

			raw, err := os.ReadFile(capture)
			if err != nil {
				t.Fatalf("read captured argv: %v", err)
			}
			got := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
			want := []string{"switch", "open", cwd}
			if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
				t.Fatalf("argv = %#v, want %#v", got, want)
			}
		})
	}

	for _, marker := range []string{"PWN_DOLLAR", "PWN_TICK", "PWN_SEMI", "PWN_REDIRECT"} {
		if _, err := os.Stat(filepath.Join(root, marker)); !os.IsNotExist(err) {
			t.Fatalf("injection marker %s exists (stat error %v)", marker, err)
		}
	}
}

func TestKeyBindingCatalogGuaranteedLaunchDefaultsAreOnlyAltOneThroughFive(t *testing.T) {
	t.Parallel()

	want := map[string]string{
		"ProjectSidebarToggle": "M-1",
		"NotifySidebarToggle":  "M-2",
		"RecentWindows:Open":   "M-3",
		"AIResumePickerToggle": "M-4",
		"SettingsToggle":       "M-5",
	}
	got := map[string]string{}
	for _, action := range DefaultKeyBindingCatalog() {
		if action.Tier == keyBindingTierGuaranteedLaunchDefault {
			got[action.ID] = FirstNonEmptyString(KeyBindingEffectivePlainChords(action))
		}
	}
	if len(got) != len(want) {
		t.Fatalf("guaranteed defaults = %#v, want %#v", got, want)
	}
	for id, chord := range want {
		if got[id] != chord {
			t.Fatalf("guaranteed default %s = %q, want %q; got all %#v", id, got[id], chord, got)
		}
	}

	for _, id := range []string{"AISplitPickerToggle", "SessionPopupToggle", "ProjectSwitcherToggle", "new-window", "previous-window", "next-window", "rename-window", "rename-pane-label"} {
		action, ok := KeyBindingActionByID(DefaultKeyBindingCatalog(), id)
		if !ok {
			t.Fatalf("missing action %s", id)
		}
		if action.Tier == keyBindingTierGuaranteedLaunchDefault {
			t.Fatalf("%s tier = guaranteed launch default, want non-guaranteed tier", id)
		}
	}
}

func TestKeymapRejectsRetiredPaneRenameActionWithExactReplacement(t *testing.T) {
	t.Parallel()

	for _, header := range []string{
		"[bindings.rename-pane-topic]",
		`[bindings."rename-pane-topic"]`,
	} {
		t.Run(header, func(t *testing.T) {
			t.Parallel()

			_, err := ParseKeymapFile("/tmp/keymap.toml", header+"\nkeys = [\"M-r\"]\n")
			if err == nil {
				t.Fatal("parseKeymapFile() = nil, want retired action error")
			}
			for _, want := range []string{
				"/tmp/keymap.toml:1",
				`keybinding action "rename-pane-topic" was removed`,
				"replace [bindings.rename-pane-topic] with [bindings.rename-pane-label]",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("parseKeymapFile() error = %q, want %q", err, want)
				}
			}
		})
	}
}

func TestRecentWindowsOpenOwnsAltThreeDefault(t *testing.T) {
	t.Parallel()

	catalog := DefaultKeyBindingCatalog()
	recent, ok := KeyBindingActionByID(catalog, "RecentWindows:Open")
	if !ok {
		t.Fatalf("catalog missing RecentWindows:Open")
	}
	if got, want := KeyBindingDisplayName(recent), "Open Recent Windows"; got != want {
		t.Fatalf("RecentWindows:Open display name = %q, want %q", got, want)
	}
	if got, want := KeyBindingEffectivePlainChords(recent), []string{"M-3"}; !slices.Equal(got, want) {
		t.Fatalf("RecentWindows:Open keys = %#v, want %#v", got, want)
	}
	if recent.Kind != keyBindingActionTogglePopup || !recent.Toggleable {
		t.Fatalf("RecentWindows:Open kind/toggleable = (%s, %v), want toggle-popup true", recent.Kind, recent.Toggleable)
	}
	if recent.TmuxKind != TmuxBindingPopupToggle || recent.TmuxBody != "recent-windows" {
		t.Fatalf("RecentWindows:Open tmux binding = (%s, %q), want popup-toggle recent-windows", recent.TmuxKind, recent.TmuxBody)
	}
	for _, want := range []string{"Recent windows queue", "last-pane", "existing-session popup"} {
		if !strings.Contains(recent.Description, want) {
			t.Fatalf("RecentWindows:Open description = %q, want %q", recent.Description, want)
		}
	}

	sessionPopup, ok := KeyBindingActionByID(catalog, "SessionPopupToggle")
	if !ok {
		t.Fatalf("catalog missing SessionPopupToggle")
	}
	if sessionPopup.Tier == keyBindingTierGuaranteedLaunchDefault {
		t.Fatalf("SessionPopupToggle tier = guaranteed launch default, want configurable non-guaranteed action")
	}
	if got := KeyBindingEffectivePlainChords(sessionPopup); len(got) != 0 {
		t.Fatalf("SessionPopupToggle keys = %#v, want no guaranteed M-3 default", got)
	}
}

func TestKeymapKeysOverrideEmitsOneTmuxBindPerAlias(t *testing.T) {
	t.Parallel()

	parsed, err := ParseKeymapFile("/tmp/keymap.toml", `[bindings.ProjectSidebarToggle]
keys = ["M-1", "M-a"]
`)
	if err != nil {
		t.Fatalf("parseKeymapFile() error = %v", err)
	}
	merged, err := MergeKeymapOverrides(DefaultKeyBindingCatalog(), parsed)
	if err != nil {
		t.Fatalf("mergeKeymapOverrides() error = %v", err)
	}
	lines := strings.Join(TmuxBindLines("/bin/projmux", KeyBindingCatalogForScopeFrom(merged, KeyBindingScopeStandalone)), "\n")
	for _, want := range []string{"bind-key -n M-1 run-shell", "bind-key -n M-a run-shell"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("tmux bind lines =\n%s\nwant %q", lines, want)
		}
	}
}

func TestKeymapMigratesConflictingLegacyAIPickerDefaultsAndPreservesAliases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		actionID   string
		wantChords []string
	}{
		{
			name: "split picker",
			body: `[bindings.AISplitPickerToggle]
keys = ["M-a", "M-4", "M-7"]
`,
			actionID:   "AISplitPickerToggle",
			wantChords: []string{"M-a", "M-7"},
		},
		{
			name: "resume picker",
			body: `[bindings.AIResumePickerToggle]
keys = ["M-b", "M-7", "M-4"]
`,
			actionID:   "AIResumePickerToggle",
			wantChords: []string{"M-b", "M-4"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			parsed, err := ParseKeymapFile("/tmp/keymap.toml", tc.body)
			if err != nil {
				t.Fatalf("parseKeymapFile() error = %v", err)
			}
			merged, err := MergeKeymapOverrides(DefaultKeyBindingCatalog(), parsed)
			if err != nil {
				t.Fatalf("mergeKeymapOverrides() error = %v", err)
			}
			action, ok := KeyBindingActionByID(merged, tc.actionID)
			if !ok {
				t.Fatalf("catalog missing %s", tc.actionID)
			}
			if got := KeyBindingEffectivePlainChords(action); !slices.Equal(got, tc.wantChords) {
				t.Fatalf("%s keys = %#v, want %#v", tc.actionID, got, tc.wantChords)
			}
		})
	}
}

func TestKeymapMigratesConflictingLegacyAIPickerPlainOverrides(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		actionID string
		want     string
	}{
		{
			name:     "split picker",
			body:     "[bindings.AISplitPickerToggle]\nplain = \"M-4\"\n",
			actionID: "AISplitPickerToggle",
			want:     "M-7",
		},
		{
			name:     "resume picker",
			body:     "[bindings.AIResumePickerToggle]\nplain = \"M-7\"\n",
			actionID: "AIResumePickerToggle",
			want:     "M-4",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			parsed, err := ParseKeymapFile("/tmp/keymap.toml", tc.body)
			if err != nil {
				t.Fatalf("parseKeymapFile() error = %v", err)
			}
			merged, err := MergeKeymapOverrides(DefaultKeyBindingCatalog(), parsed)
			if err != nil {
				t.Fatalf("mergeKeymapOverrides() error = %v", err)
			}
			action, ok := KeyBindingActionByID(merged, tc.actionID)
			if !ok {
				t.Fatalf("catalog missing %s", tc.actionID)
			}
			if got := FirstNonEmptyString(KeyBindingEffectivePlainChords(action)); got != tc.want {
				t.Fatalf("%s key = %q, want %q", tc.actionID, got, tc.want)
			}
		})
	}
}

func TestKeymapPreservesLegacyAIPickerChordWithoutDefaultConflict(t *testing.T) {
	t.Parallel()

	parsed, err := ParseKeymapFile("/tmp/keymap.toml", `[bindings.AISplitPickerToggle]
keys = ["M-4", "M-a"]
[bindings.AIResumePickerToggle]
keys = ["M-r"]
`)
	if err != nil {
		t.Fatalf("parseKeymapFile() error = %v", err)
	}
	merged, err := MergeKeymapOverrides(DefaultKeyBindingCatalog(), parsed)
	if err != nil {
		t.Fatalf("mergeKeymapOverrides() error = %v", err)
	}
	action, ok := KeyBindingActionByID(merged, "AISplitPickerToggle")
	if !ok {
		t.Fatal("catalog missing AISplitPickerToggle")
	}
	if got, want := KeyBindingEffectivePlainChords(action), []string{"M-4", "M-a"}; !slices.Equal(got, want) {
		t.Fatalf("AISplitPickerToggle keys = %#v, want explicit non-conflicting keys %#v", got, want)
	}
}

func TestKeymapDoesNotMigrateConflictBetweenExplicitAIPickerOverrides(t *testing.T) {
	t.Parallel()

	parsed, err := ParseKeymapFile("/tmp/keymap.toml", `[bindings.AISplitPickerToggle]
keys = ["M-4"]
[bindings.AIResumePickerToggle]
keys = ["M-4"]
`)
	if err != nil {
		t.Fatalf("parseKeymapFile() error = %v", err)
	}
	if _, err := MergeKeymapOverrides(DefaultKeyBindingCatalog(), parsed); err == nil {
		t.Fatal("mergeKeymapOverrides() = nil, want conflict between explicit overrides")
	}
}

func TestPopupToggleModeActionMappingCoversCatalog(t *testing.T) {
	t.Parallel()

	want := map[string][]string{
		"ProjectSidebarToggle":  {"sessionizer-sidebar"},
		"NotifySidebarToggle":   {"notify-sidebar"},
		"RecentWindows:Open":    {"recent-windows"},
		"AISplitPickerToggle":   {"ai-split-picker-right", "ai-split-picker-down"},
		"AIResumePickerToggle":  {"ai-split-resume-right", "ai-split-resume-down"},
		"SettingsToggle":        {"ai-split-settings"},
		"ProjectSwitcherToggle": {"sessionizer"},
		"SessionPopupToggle":    {"session-popup"},
		"Resources:Open":        {ResourceInspectorPopupMode},
	}
	catalog := DefaultKeyBindingCatalog()
	var gotIDs []string
	for _, action := range catalog {
		if keyBindingActionIsPopupToggle(action) {
			gotIDs = append(gotIDs, action.ID)
		}
	}
	if got := UniqueNonEmptyStrings(gotIDs); len(got) != len(want) {
		t.Fatalf("popup toggle action ids = %#v, want exactly %#v", got, want)
	}
	for id, modes := range want {
		action, ok := KeyBindingActionByID(catalog, id)
		if !ok {
			t.Fatalf("catalog missing popup toggle action %s", id)
		}
		if !keyBindingActionIsPopupToggle(action) {
			t.Fatalf("%s metadata = kind %s tmux %s toggleable %v, want catalog popup toggle", id, action.Kind, action.TmuxKind, action.Toggleable)
		}
		if got := popupToggleModesForAction(action); !slices.Equal(got, modes) {
			t.Fatalf("%s popup modes = %#v, want %#v", id, got, modes)
		}
		for _, mode := range modes {
			got, ok := PopupToggleActionIDForMode(mode)
			if !ok || got != id {
				t.Fatalf("popupToggleActionIDForMode(%q) = %q, %v; want %s, true", mode, got, ok, id)
			}
		}
	}
}

func TestKeymapPopupToggleAliasesEmitTmuxBindingsForCatalogActions(t *testing.T) {
	t.Parallel()

	parsed, err := ParseKeymapFile("/tmp/keymap.toml", `[bindings.ProjectSidebarToggle]
keys = ["M-a"]
[bindings.NotifySidebarToggle]
keys = ["M-b"]
[bindings."RecentWindows:Open"]
keys = ["M-c"]
[bindings.AISplitPickerToggle]
keys = ["M-d"]
[bindings.SettingsToggle]
keys = ["M-e"]
[bindings.ProjectSwitcherToggle]
keys = ["M-f"]
[bindings.SessionPopupToggle]
keys = ["M-g"]
`)
	if err != nil {
		t.Fatalf("parseKeymapFile() error = %v", err)
	}
	merged, err := MergeKeymapOverrides(DefaultKeyBindingCatalog(), parsed)
	if err != nil {
		t.Fatalf("mergeKeymapOverrides() error = %v", err)
	}
	lines := strings.Join(TmuxBindLines("/bin/projmux", KeyBindingCatalogForScopeFrom(merged, KeyBindingScopeStandalone)), "\n")
	for chord, mode := range map[string]string{
		"M-a": "sessionizer-sidebar",
		"M-b": "notify-sidebar",
		"M-c": "recent-windows",
		"M-d": "ai-split-picker-right",
		"M-e": "ai-split-settings",
		"M-f": "sessionizer",
		"M-g": "session-popup",
	} {
		for _, want := range []string{"bind-key -n " + chord + " run-shell", "tmux popup-toggle --client #{client_tty} --anchor #{pane_id} " + mode} {
			if !strings.Contains(lines, want) {
				t.Fatalf("tmux bind lines =\n%s\nwant %q", lines, want)
			}
		}
	}
}

func TestKeymapTransportAliasesKeepDefaultTransportChord(t *testing.T) {
	t.Parallel()

	parsed, err := ParseKeymapFile("/tmp/keymap.toml", `[bindings.previous-window]
keys = ["M-["]
`)
	if err != nil {
		t.Fatalf("parseKeymapFile() error = %v", err)
	}
	merged, err := MergeKeymapOverrides(DefaultKeyBindingCatalog(), parsed)
	if err != nil {
		t.Fatalf("mergeKeymapOverrides() error = %v", err)
	}
	action, ok := KeyBindingActionByID(merged, "previous-window")
	if !ok {
		t.Fatalf("missing previous-window")
	}
	if got, want := KeyBindingEffectivePlainChords(action), []string{"M-S-Left", "M-["}; !slices.Equal(got, want) {
		t.Fatalf("previous-window keys = %#v, want %#v", got, want)
	}
	lines := strings.Join(TmuxBindLines("/bin/projmux", KeyBindingCatalogForScopeFrom(merged, KeyBindingScopeApp)), "\n")
	for _, want := range []string{"bind-key -n M-S-Left previous-window", "bind-key -n M-[ previous-window"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("tmux bind lines =\n%s\nwant %q", lines, want)
		}
	}
}

func TestKeymapTransportAliasesRejectDefaultTransportChord(t *testing.T) {
	t.Parallel()

	parsed, err := ParseKeymapFile("/tmp/keymap.toml", `[bindings.previous-window]
keys = ["M-S-Left"]
`)
	if err != nil {
		t.Fatalf("parseKeymapFile() error = %v", err)
	}
	if _, err := MergeKeymapOverrides(DefaultKeyBindingCatalog(), parsed); err == nil {
		t.Fatalf("mergeKeymapOverrides() = nil, want transport default rejected as plain alias")
	}
}

func TestKeymapEmptyKeysExplicitlyUnbindsTransportDefault(t *testing.T) {
	t.Parallel()

	parsed, err := ParseKeymapFile("/tmp/keymap.toml", `[bindings.previous-window]
keys = []
`)
	if err != nil {
		t.Fatalf("parseKeymapFile() error = %v", err)
	}
	merged, err := MergeKeymapOverrides(DefaultKeyBindingCatalog(), parsed)
	if err != nil {
		t.Fatalf("mergeKeymapOverrides() error = %v", err)
	}
	action, ok := KeyBindingActionByID(merged, "previous-window")
	if !ok {
		t.Fatalf("missing previous-window")
	}
	if got := KeyBindingEffectivePlainChords(action); len(got) != 0 {
		t.Fatalf("previous-window keys = %#v, want explicitly unbound", got)
	}
	lines := strings.Join(TmuxBindLines("/bin/projmux", KeyBindingCatalogForScopeFrom(merged, KeyBindingScopeApp)), "\n")
	if strings.Contains(lines, "bind-key -n M-S-Left previous-window") {
		t.Fatalf("tmux bind lines =\n%s\ndid not want transport default bind after keys = []", lines)
	}
}

func TestKeyBindingCatalogPhase0UserBindableCoverage(t *testing.T) {
	t.Parallel()

	catalog := DefaultKeyBindingCatalog()
	cases := map[string]string{
		"last-pane":             "last-pane",
		"ai-split-right":        "internal agent-pane launch-default right",
		"ai-split-down":         "internal agent-pane launch-default down",
		"ai-split-codex-right":  "internal agent-pane launch-provider codex right",
		"ai-split-codex-down":   "internal agent-pane launch-provider codex down",
		"ai-split-claude-right": "internal agent-pane launch-provider claude right",
		"ai-split-claude-down":  "internal agent-pane launch-provider claude down",
		"ai-split-shell-right":  "internal agent-pane launch-shell right",
		"ai-split-shell-down":   "internal agent-pane launch-shell down",
	}
	for id, body := range cases {
		action, ok := KeyBindingActionByID(catalog, id)
		if !ok {
			t.Fatalf("catalog missing %q", id)
		}
		if !KeyBindingEditable(action) {
			t.Fatalf("%s is not editable", id)
		}
		if got := FirstNonEmptyString(KeyBindingEffectivePlainChords(action)); got != "" {
			t.Fatalf("%s default key = %q, want no-bind default", id, got)
		}
		if action.TmuxBody != body {
			t.Fatalf("%s TmuxBody = %q, want %q", id, action.TmuxBody, body)
		}
	}
}

func TestGeneratedSplitBindingsNeverInvokeRetiredAIRoot(t *testing.T) {
	t.Parallel()

	for _, action := range DefaultKeyBindingCatalog() {
		if !strings.HasPrefix(action.ID, "ai-split-") || action.Kind != keyBindingActionCommand {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(action.TmuxBody), "ai ") {
			t.Errorf("split action %q invokes retired route: %q", action.ID, action.TmuxBody)
		}
		if _, _, ok := cli.Resolve(strings.Fields(action.TmuxBody)); !ok {
			t.Errorf("split action %q does not resolve through the command manifest: %q", action.ID, action.TmuxBody)
		}
	}
}

func TestKeymapQuotedInternalIDMergesAndDroppedLegacyIDIgnored(t *testing.T) {
	t.Parallel()

	// session-popup is a hard-dropped legacy id (Phase 4): it must be
	// silently ignored rather than merged onto SessionPopupToggle. The
	// real quoted internal id (Sidebar:PinProject) still merges.
	parsed, err := ParseKeymapFile("/tmp/keymap.toml", `[bindings.session-popup]
keys = ["M-s"]

[bindings."Sidebar:PinProject"]
keys = ["p"]
`)
	if err != nil {
		t.Fatalf("parseKeymapFile() error = %v", err)
	}
	merged, err := MergeKeymapOverrides(DefaultKeyBindingCatalog(), parsed)
	if err != nil {
		t.Fatalf("mergeKeymapOverrides() error = %v", err)
	}
	sessionPopup, ok := KeyBindingActionByID(merged, "SessionPopupToggle")
	if !ok {
		t.Fatalf("missing canonical SessionPopupToggle")
	}
	if got := KeyBindingEffectivePlainChords(sessionPopup); len(got) != 0 {
		t.Fatalf("SessionPopupToggle keys = %#v, want default (dropped legacy id ignored)", got)
	}
	if _, ok := KeyBindingActionByID(merged, "session-popup"); ok {
		t.Fatalf("dropped legacy id session-popup should not resolve to any action")
	}
	pinProject, ok := KeyBindingActionByID(merged, "Sidebar:PinProject")
	if !ok {
		t.Fatalf("missing Sidebar:PinProject")
	}
	if got, want := KeyBindingEffectivePlainChords(pinProject), []string{"p"}; !slices.Equal(got, want) {
		t.Fatalf("Sidebar:PinProject keys = %#v, want %#v", got, want)
	}
}

func TestKeyBindingCatalogDropsLegacyIDsAndPrefixRemnants(t *testing.T) {
	t.Parallel()

	catalog := DefaultKeyBindingCatalog()

	// The 6 hard-dropped legacy ids must no longer resolve to any action.
	for _, legacy := range []string{
		"sessionizer-sidebar",
		"notify-sidebar",
		"session-popup",
		"ai-split-picker-right",
		"ai-split-settings",
		"sessionizer",
	} {
		if _, ok := KeyBindingActionByID(catalog, legacy); ok {
			t.Fatalf("dropped legacy id %q should not resolve to any action", legacy)
		}
	}

	// The 7 prefix remnants must have an empty PrefixChord.
	for _, id := range []string{
		"SessionPopupToggle",
		"ProjectSwitcherToggle",
		"rename-window",
		"ai-split-right",
		"ai-split-down",
		"current-project-session",
		"toggle-mouse",
	} {
		action, ok := KeyBindingActionByID(catalog, id)
		if !ok {
			t.Fatalf("missing action %q", id)
		}
		if action.PrefixChord != "" {
			t.Fatalf("action %q PrefixChord = %q, want empty", id, action.PrefixChord)
		}
	}

	// ProjectSidebarToggle intentionally retains its prefix binding.
	sidebar, ok := KeyBindingActionByID(catalog, "ProjectSidebarToggle")
	if !ok {
		t.Fatalf("missing ProjectSidebarToggle")
	}
	if sidebar.PrefixChord != "F" {
		t.Fatalf("ProjectSidebarToggle PrefixChord = %q, want %q", sidebar.PrefixChord, "F")
	}
}

func TestRenderKeymapFilePreservesLegacyPrefixEntries(t *testing.T) {
	t.Parallel()

	parsed, err := ParseKeymapFile("/tmp/keymap.toml", `[bindings.ProjectSidebarToggle]
prefix = "F"
`)
	if err != nil {
		t.Fatalf("parseKeymapFile() error = %v", err)
	}
	rendered := RenderKeymapFile(parsed)
	if !strings.Contains(rendered, "[bindings.ProjectSidebarToggle]\nprefix = \"F\"\n") {
		t.Fatalf("renderKeymapFile() = %q, want legacy prefix preserved", rendered)
	}
}

func TestRenderKeymapFileQuotesInternalActionIDs(t *testing.T) {
	t.Parallel()

	rendered := RenderKeymapFile(KeymapFile{Bindings: map[string]KeymapOverride{
		"Sidebar:PinProject": {KeysSet: true, Keys: []string{"M-p", "p"}},
	}})
	if !strings.Contains(rendered, "[bindings.\"Sidebar:PinProject\"]\nkeys = [\"M-p\", \"p\"]\n") {
		t.Fatalf("renderKeymapFile() = %q, want quoted internal action table", rendered)
	}
}

func TestKeymapKeysRejectTransportPayloadAliases(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "user fallback", body: `[bindings.ProjectSidebarToggle]
keys = ["User4"]
`},
		{name: "user key", body: `[bindings.ProjectSidebarToggle]
keys = ["UserKey4"]
`},
		{name: "user sequence", body: `[bindings.ProjectSidebarToggle]
keys = ["UserSequence4"]
`},
		{name: "csi u", body: `[bindings.ProjectSidebarToggle]
keys = ["[9005u"]
`},
		{name: "xterm modified key", body: `[bindings.ProjectSidebarToggle]
keys = ["[1;4D"]
`},
		{name: "raw escape", body: "[bindings.ProjectSidebarToggle]\nkeys = [\"\x1b1\"]\n"},
		{name: "send input", body: `[bindings.ProjectSidebarToggle]
keys = ["sendInput"]
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := ParseKeymapFile("/tmp/keymap.toml", tc.body); err == nil {
				t.Fatalf("parseKeymapFile() = nil, want %s rejected", tc.name)
			}
		})
	}
}

func TestKeymapConflictDomains(t *testing.T) {
	t.Parallel()

	globalDuplicate := []KeyBindingAction{
		{ID: "one", Kind: keyBindingActionCommand, PlainChord: "M-a"},
		{ID: "two", Kind: keyBindingActionCommand, PlainChord: "M-a"},
	}
	if err := ValidateKeymapConflicts(globalDuplicate); err == nil {
		t.Fatalf("validateKeymapConflicts(global duplicate) = nil, want conflict")
	}

	crossSurfaceDuplicate := []KeyBindingAction{
		{ID: "Sidebar:PinProject", Kind: KeyBindingActionPickerInternal, Surface: "Sidebar", PlainChord: "x"},
		{ID: "NotifySidebar:ClearNonCritical", Kind: KeyBindingActionPickerInternal, Surface: "NotifySidebar", PlainChord: "x"},
	}
	if err := ValidateKeymapConflicts(crossSurfaceDuplicate); err != nil {
		t.Fatalf("validateKeymapConflicts(cross surface duplicate) error = %v, want nil", err)
	}

	sameSurfaceDuplicate := []KeyBindingAction{
		{ID: "Sidebar:PinProject", Kind: KeyBindingActionPickerInternal, Surface: "Sidebar", PlainChord: "x"},
		{ID: "Sidebar:KillSession", Kind: KeyBindingActionPickerInternal, Surface: "Sidebar", PlainChord: "x"},
	}
	if err := ValidateKeymapConflicts(sameSurfaceDuplicate); err == nil {
		t.Fatalf("validateKeymapConflicts(same surface duplicate) = nil, want conflict")
	}
}

func TestNormalizeKeymapTypedChordRejectsTransportPayloads(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"\x1b[9005u", "\x1b[1;4D", "[9005u", "[1;4D", "csi:9005u", `\u001b[9005u`, `\x1b[9005u`, `sendInput("\u001b1")`, "User4", "UserKey4", "UserSequence4"} {
		if got, err := NormalizeKeymapTypedChord(input); err == nil {
			t.Fatalf("normalizeKeymapTypedChord(%q) = %q, nil; want rejection", input, got)
		}
	}
	for _, input := range []string{"C-r", "M-a", "M-S-Left", "C-Space"} {
		got, err := NormalizeKeymapTypedChord(input)
		if err != nil {
			t.Fatalf("normalizeKeymapTypedChord(%q) error = %v", input, err)
		}
		if got != input {
			t.Fatalf("normalizeKeymapTypedChord(%q) = %q, want same", input, got)
		}
	}
}

func TestKeymapPrimaryKeysRemainLogicalAndExcludeDiagnosticPayloads(t *testing.T) {
	t.Parallel()

	for _, action := range DefaultKeyBindingCatalog() {
		for _, key := range KeyBindingEffectivePlainChords(action) {
			for _, forbidden := range []string{"\x1b", "[1;", "[9005u", "User", "CSI-u"} {
				if strings.Contains(key, forbidden) {
					t.Fatalf("%s primary key %q contains diagnostic payload marker %q", action.ID, key, forbidden)
				}
			}
		}
	}
	prev, ok := KeyBindingActionByID(DefaultKeyBindingCatalog(), "previous-window")
	if !ok {
		t.Fatalf("missing previous-window")
	}
	if got, want := KeyBindingEffectivePlainChords(prev), []string{"M-S-Left"}; !slices.Equal(got, want) {
		t.Fatalf("previous-window keys = %#v, want logical key %#v", got, want)
	}
}
