package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestKeyBindingCatalogGuaranteedLaunchDefaultsAreOnlyAltOneThroughFive(t *testing.T) {
	t.Parallel()

	want := map[string]string{
		"ProjectSidebarToggle": "M-1",
		"NotifySidebarToggle":  "M-2",
		"RecentWindows:Open":   "M-3",
		"AISplitPickerToggle":  "M-4",
		"SettingsToggle":       "M-5",
	}
	got := map[string]string{}
	for _, action := range defaultKeyBindingCatalog() {
		if action.Tier == keyBindingTierGuaranteedLaunchDefault {
			got[action.ID] = firstNonEmptyString(keyBindingEffectivePlainChords(action))
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

	for _, id := range []string{"SessionPopupToggle", "ProjectSwitcherToggle", "new-window", "previous-window", "next-window", "rename-window", "rename-pane-topic"} {
		action, ok := keyBindingActionByID(defaultKeyBindingCatalog(), id)
		if !ok {
			t.Fatalf("missing action %s", id)
		}
		if action.Tier == keyBindingTierGuaranteedLaunchDefault {
			t.Fatalf("%s tier = guaranteed launch default, want non-guaranteed tier", id)
		}
	}
}

func TestRecentWindowsOpenOwnsAltThreeDefault(t *testing.T) {
	t.Parallel()

	catalog := defaultKeyBindingCatalog()
	recent, ok := keyBindingActionByID(catalog, "RecentWindows:Open")
	if !ok {
		t.Fatalf("catalog missing RecentWindows:Open")
	}
	if got, want := keyBindingDisplayName(recent), "Recent Windows"; got != want {
		t.Fatalf("RecentWindows:Open display name = %q, want %q", got, want)
	}
	if got, want := keyBindingEffectivePlainChords(recent), []string{"M-3"}; !equalStrings(got, want) {
		t.Fatalf("RecentWindows:Open keys = %#v, want %#v", got, want)
	}
	if recent.Kind != keyBindingActionTogglePopup || !recent.Toggleable {
		t.Fatalf("RecentWindows:Open kind/toggleable = (%s, %v), want toggle-popup true", recent.Kind, recent.Toggleable)
	}
	if recent.TmuxKind != tmuxBindingPopupToggle || recent.TmuxBody != "recent-windows" {
		t.Fatalf("RecentWindows:Open tmux binding = (%s, %q), want popup-toggle recent-windows", recent.TmuxKind, recent.TmuxBody)
	}
	for _, want := range []string{"Recent windows queue", "last-pane", "existing-session popup"} {
		if !strings.Contains(recent.Description, want) {
			t.Fatalf("RecentWindows:Open description = %q, want %q", recent.Description, want)
		}
	}

	sessionPopup, ok := keyBindingActionByID(catalog, "SessionPopupToggle")
	if !ok {
		t.Fatalf("catalog missing SessionPopupToggle")
	}
	if sessionPopup.Tier == keyBindingTierGuaranteedLaunchDefault {
		t.Fatalf("SessionPopupToggle tier = guaranteed launch default, want configurable non-guaranteed action")
	}
	if got := keyBindingEffectivePlainChords(sessionPopup); len(got) != 0 {
		t.Fatalf("SessionPopupToggle keys = %#v, want no guaranteed M-3 default", got)
	}
}

func TestKeymapKeysOverrideEmitsOneTmuxBindPerAlias(t *testing.T) {
	t.Parallel()

	parsed, err := parseKeymapFile("/tmp/keymap.toml", `[bindings.ProjectSidebarToggle]
keys = ["M-1", "M-a"]
`)
	if err != nil {
		t.Fatalf("parseKeymapFile() error = %v", err)
	}
	merged, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), parsed)
	if err != nil {
		t.Fatalf("mergeKeymapOverrides() error = %v", err)
	}
	lines := strings.Join(tmuxBindLines("/bin/projmux", keyBindingCatalogForScopeFrom(merged, keyBindingScopeStandalone)), "\n")
	for _, want := range []string{"bind-key -n M-1 run-shell", "bind-key -n M-a run-shell"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("tmux bind lines =\n%s\nwant %q", lines, want)
		}
	}
}

func TestKeymapTransportAliasesKeepDefaultTransportChord(t *testing.T) {
	t.Parallel()

	parsed, err := parseKeymapFile("/tmp/keymap.toml", `[bindings.previous-window]
keys = ["M-["]
`)
	if err != nil {
		t.Fatalf("parseKeymapFile() error = %v", err)
	}
	merged, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), parsed)
	if err != nil {
		t.Fatalf("mergeKeymapOverrides() error = %v", err)
	}
	action, ok := keyBindingActionByID(merged, "previous-window")
	if !ok {
		t.Fatalf("missing previous-window")
	}
	if got, want := keyBindingEffectivePlainChords(action), []string{"M-S-Left", "M-["}; !equalStrings(got, want) {
		t.Fatalf("previous-window keys = %#v, want %#v", got, want)
	}
	lines := strings.Join(tmuxBindLines("/bin/projmux", keyBindingCatalogForScopeFrom(merged, keyBindingScopeApp)), "\n")
	for _, want := range []string{"bind-key -n M-S-Left previous-window", "bind-key -n M-[ previous-window"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("tmux bind lines =\n%s\nwant %q", lines, want)
		}
	}
}

func TestKeymapTransportAliasesRejectDefaultTransportChord(t *testing.T) {
	t.Parallel()

	parsed, err := parseKeymapFile("/tmp/keymap.toml", `[bindings.previous-window]
keys = ["M-S-Left"]
`)
	if err != nil {
		t.Fatalf("parseKeymapFile() error = %v", err)
	}
	if _, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), parsed); err == nil {
		t.Fatalf("mergeKeymapOverrides() = nil, want transport default rejected as plain alias")
	}
}

func TestKeymapEmptyKeysExplicitlyUnbindsTransportDefault(t *testing.T) {
	t.Parallel()

	parsed, err := parseKeymapFile("/tmp/keymap.toml", `[bindings.previous-window]
keys = []
`)
	if err != nil {
		t.Fatalf("parseKeymapFile() error = %v", err)
	}
	merged, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), parsed)
	if err != nil {
		t.Fatalf("mergeKeymapOverrides() error = %v", err)
	}
	action, ok := keyBindingActionByID(merged, "previous-window")
	if !ok {
		t.Fatalf("missing previous-window")
	}
	if got := keyBindingEffectivePlainChords(action); len(got) != 0 {
		t.Fatalf("previous-window keys = %#v, want explicitly unbound", got)
	}
	lines := strings.Join(tmuxBindLines("/bin/projmux", keyBindingCatalogForScopeFrom(merged, keyBindingScopeApp)), "\n")
	if strings.Contains(lines, "bind-key -n M-S-Left previous-window") {
		t.Fatalf("tmux bind lines =\n%s\ndid not want transport default bind after keys = []", lines)
	}
}

func TestKeyBindingCatalogPhase0UserBindableCoverage(t *testing.T) {
	t.Parallel()

	catalog := defaultKeyBindingCatalog()
	cases := map[string]string{
		"last-pane":             "last-pane",
		"ai-split-codex-right":  "ai split --agent codex right",
		"ai-split-codex-down":   "ai split --agent codex down",
		"ai-split-claude-right": "ai split --agent claude right",
		"ai-split-claude-down":  "ai split --agent claude down",
		"ai-split-shell-right":  "ai split --agent shell right",
		"ai-split-shell-down":   "ai split --agent shell down",
	}
	for id, body := range cases {
		action, ok := keyBindingActionByID(catalog, id)
		if !ok {
			t.Fatalf("catalog missing %q", id)
		}
		if !keyBindingEditable(action) {
			t.Fatalf("%s is not editable", id)
		}
		if got := firstNonEmptyString(keyBindingEffectivePlainChords(action)); got != "" {
			t.Fatalf("%s default key = %q, want no-bind default", id, got)
		}
		if action.TmuxBody != body {
			t.Fatalf("%s TmuxBody = %q, want %q", id, action.TmuxBody, body)
		}
	}
}

func TestKeymapQuotedInternalIDMergesAndDroppedLegacyIDIgnored(t *testing.T) {
	t.Parallel()

	// session-popup is a hard-dropped legacy id (Phase 4): it must be
	// silently ignored rather than merged onto SessionPopupToggle. The
	// real quoted internal id (Sidebar:PinProject) still merges.
	parsed, err := parseKeymapFile("/tmp/keymap.toml", `[bindings.session-popup]
keys = ["M-s"]

[bindings."Sidebar:PinProject"]
keys = ["p"]
`)
	if err != nil {
		t.Fatalf("parseKeymapFile() error = %v", err)
	}
	merged, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), parsed)
	if err != nil {
		t.Fatalf("mergeKeymapOverrides() error = %v", err)
	}
	sessionPopup, ok := keyBindingActionByID(merged, "SessionPopupToggle")
	if !ok {
		t.Fatalf("missing canonical SessionPopupToggle")
	}
	if got := keyBindingEffectivePlainChords(sessionPopup); len(got) != 0 {
		t.Fatalf("SessionPopupToggle keys = %#v, want default (dropped legacy id ignored)", got)
	}
	if _, ok := keyBindingActionByID(merged, "session-popup"); ok {
		t.Fatalf("dropped legacy id session-popup should not resolve to any action")
	}
	pinProject, ok := keyBindingActionByID(merged, "Sidebar:PinProject")
	if !ok {
		t.Fatalf("missing Sidebar:PinProject")
	}
	if got, want := keyBindingEffectivePlainChords(pinProject), []string{"p"}; !equalStrings(got, want) {
		t.Fatalf("Sidebar:PinProject keys = %#v, want %#v", got, want)
	}
}

func TestKeyBindingCatalogDropsLegacyIDsAndPrefixRemnants(t *testing.T) {
	t.Parallel()

	catalog := defaultKeyBindingCatalog()

	// The 6 hard-dropped legacy ids must no longer resolve to any action.
	for _, legacy := range []string{
		"sessionizer-sidebar",
		"notify-sidebar",
		"session-popup",
		"ai-split-picker-right",
		"ai-split-settings",
		"sessionizer",
	} {
		if _, ok := keyBindingActionByID(catalog, legacy); ok {
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
		action, ok := keyBindingActionByID(catalog, id)
		if !ok {
			t.Fatalf("missing action %q", id)
		}
		if action.PrefixChord != "" {
			t.Fatalf("action %q PrefixChord = %q, want empty", id, action.PrefixChord)
		}
	}

	// ProjectSidebarToggle intentionally retains its prefix binding.
	sidebar, ok := keyBindingActionByID(catalog, "ProjectSidebarToggle")
	if !ok {
		t.Fatalf("missing ProjectSidebarToggle")
	}
	if sidebar.PrefixChord != "F" {
		t.Fatalf("ProjectSidebarToggle PrefixChord = %q, want %q", sidebar.PrefixChord, "F")
	}
}

func TestRenderKeymapFilePreservesLegacyPrefixEntries(t *testing.T) {
	t.Parallel()

	parsed, err := parseKeymapFile("/tmp/keymap.toml", `[bindings.ProjectSidebarToggle]
prefix = "F"
`)
	if err != nil {
		t.Fatalf("parseKeymapFile() error = %v", err)
	}
	rendered := renderKeymapFile(parsed)
	if !strings.Contains(rendered, "[bindings.ProjectSidebarToggle]\nprefix = \"F\"\n") {
		t.Fatalf("renderKeymapFile() = %q, want legacy prefix preserved", rendered)
	}
}

func TestRenderKeymapFileQuotesInternalActionIDs(t *testing.T) {
	t.Parallel()

	rendered := renderKeymapFile(keymapFile{Bindings: map[string]keymapOverride{
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
		{name: "csi u", body: `[bindings.ProjectSidebarToggle]
keys = ["[9005u"]
`},
		{name: "raw escape", body: "[bindings.ProjectSidebarToggle]\nkeys = [\"\x1b1\"]\n"},
		{name: "send input", body: `[bindings.ProjectSidebarToggle]
keys = ["sendInput"]
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := parseKeymapFile("/tmp/keymap.toml", tc.body); err == nil {
				t.Fatalf("parseKeymapFile() = nil, want %s rejected", tc.name)
			}
		})
	}
}

func TestKeymapConflictDomains(t *testing.T) {
	t.Parallel()

	globalDuplicate := []keyBindingAction{
		{ID: "one", Kind: keyBindingActionCommand, PlainChord: "M-a"},
		{ID: "two", Kind: keyBindingActionCommand, PlainChord: "M-a"},
	}
	if err := validateKeymapConflicts(globalDuplicate); err == nil {
		t.Fatalf("validateKeymapConflicts(global duplicate) = nil, want conflict")
	}

	crossSurfaceDuplicate := []keyBindingAction{
		{ID: "Sidebar:PinProject", Kind: keyBindingActionPickerInternal, Surface: "Sidebar", PlainChord: "x"},
		{ID: "NotifySidebar:ClearNonCritical", Kind: keyBindingActionPickerInternal, Surface: "NotifySidebar", PlainChord: "x"},
	}
	if err := validateKeymapConflicts(crossSurfaceDuplicate); err != nil {
		t.Fatalf("validateKeymapConflicts(cross surface duplicate) error = %v, want nil", err)
	}

	sameSurfaceDuplicate := []keyBindingAction{
		{ID: "Sidebar:PinProject", Kind: keyBindingActionPickerInternal, Surface: "Sidebar", PlainChord: "x"},
		{ID: "Sidebar:KillSession", Kind: keyBindingActionPickerInternal, Surface: "Sidebar", PlainChord: "x"},
	}
	if err := validateKeymapConflicts(sameSurfaceDuplicate); err == nil {
		t.Fatalf("validateKeymapConflicts(same surface duplicate) = nil, want conflict")
	}
}

func TestNormalizeKeymapTypedChordRejectsTransportPayloads(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"\x1b[9005u", "[9005u", "csi:9005u", `\u001b[9005u`, `\x1b[9005u`, `sendInput("\u001b1")`, "User4"} {
		if got, err := normalizeKeymapTypedChord(input); err == nil {
			t.Fatalf("normalizeKeymapTypedChord(%q) = %q, nil; want rejection", input, got)
		}
	}
	for _, input := range []string{"C-r", "M-a", "M-S-Left", "C-Space"} {
		got, err := normalizeKeymapTypedChord(input)
		if err != nil {
			t.Fatalf("normalizeKeymapTypedChord(%q) error = %v", input, err)
		}
		if got != input {
			t.Fatalf("normalizeKeymapTypedChord(%q) = %q, want same", input, got)
		}
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
