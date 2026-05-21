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
		"SessionPopupToggle":   "M-3",
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

	for _, id := range []string{"ProjectSwitcherToggle", "new-window", "previous-window", "next-window", "rename-window", "rename-pane-topic"} {
		action, ok := keyBindingActionByID(defaultKeyBindingCatalog(), id)
		if !ok {
			t.Fatalf("missing action %s", id)
		}
		if action.Tier == keyBindingTierGuaranteedLaunchDefault {
			t.Fatalf("%s tier = guaranteed launch default, want non-guaranteed tier", id)
		}
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

func TestKeymapLegacyActionIDAndQuotedInternalIDMergeToCanonicalActions(t *testing.T) {
	t.Parallel()

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
	if got, want := keyBindingEffectivePlainChords(sessionPopup), []string{"M-s"}; !equalStrings(got, want) {
		t.Fatalf("SessionPopupToggle keys = %#v, want %#v", got, want)
	}
	pinProject, ok := keyBindingActionByID(merged, "Sidebar:PinProject")
	if !ok {
		t.Fatalf("missing Sidebar:PinProject")
	}
	if got, want := keyBindingEffectivePlainChords(pinProject), []string{"p"}; !equalStrings(got, want) {
		t.Fatalf("Sidebar:PinProject keys = %#v, want %#v", got, want)
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
