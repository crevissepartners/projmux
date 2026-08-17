package app

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func TestReservedKeymapAuthoringPolicyAliasesAndModifiers(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"Enter": "Enter", "Return": "Enter", "C-Enter": "Enter", "M-C-m": "Enter",
		"Escape": "Escape", "Esc": "Escape", "S-Esc": "Escape", "M-C-[": "Escape",
		"Tab": "Tab", "BTab": "Tab", "M-Tab": "Tab", "C-i": "Tab",
		"Backspace": "Backspace", "BSpace": "Backspace", "C-BSpace": "Backspace",
		"Delete": "Delete", "DC": "Delete", "M-DC": "Delete",
		"Up": "Up", "M-S-Down": "Down", "Ctrl-Left": "Left", "Alt-Right": "Right",
		"Home": "Home", "S-End": "End",
		"PageUp": "PageUp", "PPage": "PageUp", "PgUp": "PageUp", "M-Page-Up": "PageUp",
		"PageDown": "PageDown", "NPage": "PageDown", "PgDn": "PageDown", "C-Page-Down": "PageDown",
	}
	for chord, wantBase := range cases {
		t.Run(chord, func(t *testing.T) {
			t.Parallel()
			base, reserved := reservedKeymapAuthoringBase(chord)
			if !reserved || base != wantBase {
				t.Fatalf("reservedKeymapAuthoringBase(%q) = (%q, %v), want (%q, true)", chord, base, reserved, wantBase)
			}
			if err := validateKeymapAuthoringChord(chord); err == nil || err.Error() != keymapReservedAuthoringReason {
				t.Fatalf("validateKeymapAuthoringChord(%q) error = %v, want shared reason %q", chord, err, keymapReservedAuthoringReason)
			}
		})
	}

	for _, chord := range []string{"o", "Space", "C-o", "M-7", "F1", "F20", "C-Space"} {
		if base, reserved := reservedKeymapAuthoringBase(chord); reserved || base != "" {
			t.Fatalf("reservedKeymapAuthoringBase(%q) = (%q, %v), want non-reserved", chord, base, reserved)
		}
		if err := validateKeymapAuthoringChord(chord); err != nil {
			t.Fatalf("validateKeymapAuthoringChord(%q) error = %v", chord, err)
		}
	}
}

func TestReservedKeymapAuthoringPreservesSequenceSafetyParity(t *testing.T) {
	t.Parallel()

	for _, sequence := range []string{"C-o o", "C-o F12", "M-x !"} {
		if err := validateKeymapAuthoringSequence(sequence); err != nil {
			t.Fatalf("authoring policy rejected positive sequence %q: %v", sequence, err)
		}
		if got, err := normalizeKeymapSequence(sequence); err != nil || got != sequence {
			t.Fatalf("normalizeKeymapSequence(%q) = %q, %v", sequence, got, err)
		}
	}
	for _, sequence := range []string{"o C-p", `C-o \\x1b`, "C-o User4"} {
		if err := validateKeymapAuthoringSequence(sequence); err != nil {
			t.Fatalf("authoring reserved policy changed non-reserved rejection for %q: %v", sequence, err)
		}
		if _, err := normalizeKeymapSequence(sequence); err == nil {
			t.Fatalf("normalizeKeymapSequence(%q) = nil error, want existing safety rejection", sequence)
		}
	}
	for _, sequence := range []string{"C-o Return", "C-o M-Left", "C-o PgDn"} {
		if err := validateKeymapAuthoringSequence(sequence); err == nil || err.Error() != keymapReservedAuthoringReason {
			t.Fatalf("validateKeymapAuthoringSequence(%q) error = %v, want shared reserved reason", sequence, err)
		}
	}
}

func TestProtectedKeybindingPolicyInspectsEveryShippedTriggerField(t *testing.T) {
	t.Parallel()

	tests := []keyBindingAction{
		{PlainChord: "Return"},
		{PlainChords: []string{"C-o", "M-DC"}},
		{PrefixChord: "PPage"},
		{Sequences: []string{"C-k C-p", "M-x S-Home"}},
	}
	for i, action := range tests {
		if reason, protected := keyBindingProtectedActionReason(action); !protected || !strings.Contains(reason, "shipped/default trigger") {
			t.Fatalf("synthetic trigger field %d reason = %q, protected=%v", i, reason, protected)
		}
	}
	if reason, protected := keyBindingProtectedActionReason(keyBindingAction{
		PlainChord: "M-1", PlainChords: []string{"C-o"}, PrefixChord: "F", Sequences: []string{"C-k C-p"},
	}); protected || reason != "" {
		t.Fatalf("safe synthetic action reason = %q, protected=%v", reason, protected)
	}
}

func TestDefaultCatalogProtectedActionInventoryAndMutationRows(t *testing.T) {
	t.Parallel()

	wantProtected := []string{
		"NotifySidebar:FocusAndAck",
		"SessionPopup:CyclePreviewPaneNext",
		"SessionPopup:CyclePreviewPanePrev",
		"SessionPopup:CyclePreviewWindowNext",
		"SessionPopup:CyclePreviewWindowPrev",
		"Settings:SwitchTabNext",
		"Settings:SwitchTabPrev",
		"next-window",
		"previous-window",
		"select-pane-down",
		"select-pane-left",
		"select-pane-right",
		"select-pane-up",
	}
	cmd := keybindingCorrectnessCommand(t, t.TempDir(), nil)
	var gotProtected []string
	for _, action := range defaultKeyBindingCatalog() {
		reason, protected := keyBindingProtectedActionReason(action)
		if !protected {
			continue
		}
		gotProtected = append(gotProtected, action.ID)
		entries, _, err := cmd.keybindingDetailEntries(action.ID)
		if err != nil {
			t.Fatalf("keybindingDetailEntries(%q) error = %v", action.ID, err)
		}
		if !hasEntryLabelContainingAll(entries, "Editing locked", reason) {
			t.Fatalf("protected detail %q = %#v, want visible lock reason %q", action.ID, entries, reason)
		}
		for _, entry := range entries {
			op, ok := parseKeymapDetailAction(entry.Value, action.ID)
			if ok && keybindingMutationOperation(op) {
				t.Fatalf("protected detail %q exposes mutation row %#v", action.ID, entry)
			}
		}
		for _, chord := range keybindingVisibleChords(action) {
			keyEntries, _, err := cmd.keybindingKeyDetailEntries(action.ID, chord)
			if err != nil {
				t.Fatalf("key detail %q/%q error = %v", action.ID, chord, err)
			}
			if !hasEntryLabelContaining(keyEntries, "Test delivery") || hasEntryLabelContaining(keyEntries, "Remove key") {
				t.Fatalf("protected key detail %q/%q = %#v, want test/read rows and no remove", action.ID, chord, keyEntries)
			}
		}
	}
	slices.Sort(gotProtected)
	if !slices.Equal(gotProtected, wantProtected) {
		t.Fatalf("protected default inventory = %#v, want %#v", gotProtected, wantProtected)
	}

	ko := keybindingCorrectnessCommand(t, t.TempDir(), nil)
	ko.lookupEnv = func(name string) string {
		if name == "PROJMUX_LOCALE" {
			return "ko-KR"
		}
		return ""
	}
	koEntries, _, err := ko.keybindingDetailEntries("previous-window")
	if err != nil {
		t.Fatal(err)
	}
	if !hasEntryLabelContaining(koEntries, "편집 잠김") {
		t.Fatalf("Korean protected detail = %#v, want localized lock label", koEntries)
	}
}

func TestProtectedActionUsesDefaultNotEffectiveTriggers(t *testing.T) {
	t.Parallel()

	protectedHome := t.TempDir()
	writeReservedKeymapFixture(t, protectedHome, `[bindings.previous-window]
keys = ["M-a"]
`)
	protectedCmd := keybindingCorrectnessCommand(t, protectedHome, nil)
	entries, _, err := protectedCmd.keybindingDetailEntries("previous-window")
	if err != nil {
		t.Fatal(err)
	}
	if !hasEntryLabelContaining(entries, "Editing locked") {
		t.Fatalf("safe custom override unlocked reserved default: %#v", entries)
	}

	customHome := t.TempDir()
	writeReservedKeymapFixture(t, customHome, `[bindings.ProjectSidebarToggle]
keys = ["Left"]
`)
	customCmd := keybindingCorrectnessCommand(t, customHome, nil)
	entries, _, err = customCmd.keybindingDetailEntries("ProjectSidebarToggle")
	if err != nil {
		t.Fatal(err)
	}
	if hasEntryLabelContaining(entries, "Editing locked") || !hasEntryLabelContaining(entries, "+ Add binding") {
		t.Fatalf("legacy reserved custom trigger locked safe shipped default: %#v", entries)
	}
}

func TestReservedCaptureTypedAndFinalSaveRejectBeforeMutation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(*settingsCommand, *bytes.Buffer, *bytes.Buffer) error
	}{
		{
			name: "single recorder forged result",
			run: func(cmd *settingsCommand, out, errOut *bytes.Buffer) error {
				cmd.runner = switchRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
					return intpickercompat.Result{Key: "enter", Value: "C-Enter"}, nil
				})
				cmd.nativePicker = nativePickerFromCompatRunner(cmd.runner)
				return cmd.runKeybindingRecorder("ProjectSidebarToggle", out, errOut)
			},
		},
		{
			name: "single typed",
			run: func(cmd *settingsCommand, out, errOut *bytes.Buffer) error {
				cmd.runner = switchRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
					return intpickercompat.Result{Key: "enter", Query: "M-DC"}, nil
				})
				cmd.nativePicker = nativePickerFromCompatRunner(cmd.runner)
				return cmd.runKeybindingTyped("ProjectSidebarToggle", false, out, errOut)
			},
		},
		{
			name: "sequence typed",
			run: func(cmd *settingsCommand, out, errOut *bytes.Buffer) error {
				cmd.runner = switchRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
					return intpickercompat.Result{Key: "enter", Query: "C-o PgDn"}, nil
				})
				cmd.nativePicker = nativePickerFromCompatRunner(cmd.runner)
				return cmd.runKeybindingTyped("ProjectSidebarToggle", false, out, errOut)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			cmd := keybindingCorrectnessCommand(t, home, nil)
			var tmuxCalls [][]string
			cmd.runCommand = func(name string, args ...string) error {
				tmuxCalls = append(tmuxCalls, append([]string{name}, args...))
				return nil
			}
			before := settingsNavConfigSnapshot(t, home)
			if err := tc.run(cmd, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("route error = %v", err)
			}
			if cmd.feedback == nil || cmd.feedback.Detail != keymapReservedAuthoringReason {
				t.Fatalf("feedback = %#v, want shared reason", cmd.feedback)
			}
			if after := settingsNavConfigSnapshot(t, home); after != before || len(tmuxCalls) != 0 {
				t.Fatalf("reserved rejection mutated config/live: before=%q after=%q calls=%#v", before, after, tmuxCalls)
			}
		})
	}

	for _, tc := range []struct {
		name string
		run  func(*settingsCommand) error
	}{
		{"single final save", func(cmd *settingsCommand) error {
			return cmd.saveKeymapKeysAndApply("ProjectSidebarToggle", []string{"M-Home"}, &bytes.Buffer{})
		}},
		{"sequence final save", func(cmd *settingsCommand) error {
			return cmd.saveKeymapSequencesAndApply("ProjectSidebarToggle", []string{"C-o NPage"}, &bytes.Buffer{})
		}},
	} {
		t.Run(tc.name+" before migration", func(t *testing.T) {
			home := t.TempDir()
			writeReservedKeymapFixture(t, home, `[bindings.ProjectSidebarToggle]
keys = ["M-1"]
`)
			path := filepath.Join(home, ".config", "projmux", "keymap.toml")
			before, _ := os.ReadFile(path)
			cmd := keybindingCorrectnessCommand(t, home, nil)
			var calls int
			cmd.runCommand = func(string, ...string) error { calls++; return nil }
			err := tc.run(cmd)
			if err == nil || err.Error() != keymapReservedAuthoringReason {
				t.Fatalf("final save error = %v, want shared reserved reason", err)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(after, before) || calls != 0 {
				t.Fatalf("guard ran after migration/write: before=%q after=%q calls=%d", before, after, calls)
			}
		})
	}
}

func TestProtectedActionForgedDispatchAndHelpersNeverWrite(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	before := settingsNavConfigSnapshot(t, home)
	step := 0
	cmd := keybindingCorrectnessCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		step++
		switch step {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixKeymap + "previous-window:add"}, nil
		case 2:
			if !hasEntryLabelContainingAll(options.Entries, "Feedback", "read only", "shipped/default trigger") {
				t.Fatalf("forged dispatch feedback frame = %#v", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		default:
			t.Fatalf("unexpected picker step %d", step)
			return intpickercompat.Result{}, nil
		}
	})
	var calls [][]string
	cmd.runCommand = func(name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}
	if err := cmd.runKeybindingDetail("previous-window", &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("forged dispatch error = %v", err)
	}
	if after := settingsNavConfigSnapshot(t, home); after != before || len(calls) != 0 {
		t.Fatalf("forged dispatch mutated config/live: before=%q after=%q calls=%#v", before, after, calls)
	}

	helpers := []struct {
		name string
		run  func(*settingsCommand) error
	}{
		{"save keys", func(c *settingsCommand) error {
			return c.saveKeymapKeysAndApply("previous-window", []string{"M-a"}, &bytes.Buffer{})
		}},
		{"save sequences", func(c *settingsCommand) error {
			return c.saveKeymapSequencesAndApply("previous-window", []string{"C-k C-p"}, &bytes.Buffer{})
		}},
		{"reset binding", func(c *settingsCommand) error {
			return c.resetKeymapBindingAndApply("previous-window", &bytes.Buffer{})
		}},
		{"reset keys", func(c *settingsCommand) error { return c.resetKeymapKeysAndApply("previous-window", &bytes.Buffer{}) }},
		{"reset sequences", func(c *settingsCommand) error {
			return c.resetKeymapSequencesAndApply("previous-window", &bytes.Buffer{})
		}},
		{"add alias", func(c *settingsCommand) error {
			return c.addKeymapAliasAndApply("previous-window", "M-a", &bytes.Buffer{})
		}},
		{"remove key", func(c *settingsCommand) error {
			return c.removeKeymapKeyAndApply("previous-window", "M-S-Left", &bytes.Buffer{})
		}},
		{"add sequence", func(c *settingsCommand) error {
			return c.addKeymapSequenceAndApply("previous-window", "C-k C-p", &bytes.Buffer{})
		}},
		{"replace sequence", func(c *settingsCommand) error {
			candidate, err := normalizeKeybindingAuthoringCandidate("C-k C-s")
			if err != nil {
				return err
			}
			return c.saveKeybindingCandidateAndApply("previous-window", candidate, "C-k C-p", &bytes.Buffer{})
		}},
		{"remove sequence", func(c *settingsCommand) error {
			return c.removeKeymapSequenceAndApply("previous-window", "C-k C-p", &bytes.Buffer{})
		}},
	}
	for _, helper := range helpers {
		t.Run(helper.name, func(t *testing.T) {
			helperHome := t.TempDir()
			helperCmd := keybindingCorrectnessCommand(t, helperHome, nil)
			var helperCalls int
			helperCmd.runCommand = func(string, ...string) error { helperCalls++; return nil }
			helperBefore := settingsNavConfigSnapshot(t, helperHome)
			err := helper.run(helperCmd)
			if err == nil || !strings.Contains(err.Error(), "read only") || !strings.Contains(err.Error(), "shipped/default trigger") {
				t.Fatalf("helper error = %v, want protected-action reason", err)
			}
			if after := settingsNavConfigSnapshot(t, helperHome); after != helperBefore || helperCalls != 0 {
				t.Fatalf("helper mutated config/live: before=%q after=%q calls=%d", helperBefore, after, helperCalls)
			}
		})
	}
}

func TestExistingReservedKeymapStillParsesRendersAndCompiles(t *testing.T) {
	t.Parallel()

	body := `schema_version = 2

[bindings.ProjectSidebarToggle]
keys = ["Enter", "M-Home", "PPage"]
sequences = ["C-k Enter", "C-p M-Right"]
`
	parsed, err := parseKeymapFile("/tmp/keymap.toml", body)
	if err != nil {
		t.Fatalf("parse existing reserved config: %v", err)
	}
	rendered := renderKeymapFile(parsed)
	reparsed, err := parseKeymapFile("/tmp/keymap.toml", rendered)
	if err != nil || renderKeymapFile(reparsed) != rendered {
		t.Fatalf("reserved config render parity failed: err=%v rendered=%q", err, rendered)
	}
	merged, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), parsed)
	if err != nil {
		t.Fatalf("merge existing reserved config: %v", err)
	}
	action, ok := keyBindingActionByID(merged, "ProjectSidebarToggle")
	if !ok {
		t.Fatal("merged catalog missing ProjectSidebarToggle")
	}
	if got := keyBindingEffectivePlainChords(action); !slices.Equal(got, []string{"Enter", "M-Home", "PPage"}) {
		t.Fatalf("effective reserved keys = %#v", got)
	}
	lines := strings.Join(tmuxBindLines("/bin/projmux", keyBindingCatalogForScopeFrom(merged, keyBindingScopeStandalone)), "\n")
	for _, want := range []string{"bind-key Enter", "bind-key -n M-Home", "bind-key PPage"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("runtime binds missing %q:\n%s", want, lines)
		}
	}
}

func writeReservedKeymapFixture(t *testing.T, home, body string) {
	t.Helper()
	path := filepath.Join(home, ".config", "projmux", "keymap.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReservedRecorderPolicyUsesSharedReasonForEveryInputShape(t *testing.T) {
	t.Parallel()

	for _, key := range []intpicker.RecorderKey{
		{Name: "enter"}, {Name: "ctrl-enter"}, {Name: "alt-shift-left"},
		{Name: "page-up"}, {Name: "backspace"}, {Name: "delete"},
	} {
		if _, err := normalizeKeybindingRecorderKey(key); err == nil || err.Error() != keymapReservedAuthoringReason {
			t.Fatalf("normalizeKeybindingRecorderKey(%#v) error = %v, want %q", key, err, keymapReservedAuthoringReason)
		}
	}
	if got, err := normalizeKeybindingSequenceStroke("o", 1, true); err != nil || got != "o" {
		t.Fatalf("later printable stroke = %q, %v", got, err)
	}
	if _, err := normalizeKeybindingSequenceStroke("o", 0, true); err == nil || !strings.Contains(err.Error(), "first stroke") {
		t.Fatalf("first printable error = %v, want existing first-stroke reason", err)
	}
}
