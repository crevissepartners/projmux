package app

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/i18n"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// keybindingCorrectnessCommand is a Settings command whose keybinding surfaces
// can be rendered without a live picker. Every test here drives the render
// functions or a scripted picker; none of them touches a real terminal.
func keybindingCorrectnessCommand(t *testing.T, home string, run func(intpickercompat.Options) (intpickercompat.Result, error)) *settingsCommand {
	t.Helper()
	if run == nil {
		run = func(intpickercompat.Options) (intpickercompat.Result, error) {
			t.Fatalf("unexpected picker call")
			return intpickercompat.Result{}, nil
		}
	}
	runner := switchRunnerFunc(run)
	return &settingsCommand{
		ai:           testAICommand(home),
		switcher:     testSettingsSwitchCommand(t, &stubSwitchPinStore{}),
		homeDir:      func() (string, error) { return home, nil },
		lookupEnv:    func(string) string { return "" },
		runCommand:   func(string, ...string) error { return nil },
		runner:       runner,
		nativePicker: nativePickerFromCompatRunner(runner),
	}
}

// TestSettingsKeybindingActionDetailMatrix keeps the internal semantic and
// handler contracts independent from the interaction-first detail. It walks
// every catalog action, so removing their visible rows cannot weaken manifest
// parity, while every selectable row must still resolve to an operation the
// detail loop handles.
func TestSettingsKeybindingActionDetailMatrix(t *testing.T) {
	t.Parallel()

	cmd := keybindingCorrectnessCommand(t, t.TempDir(), nil)
	catalog := defaultKeyBindingCatalog()
	if len(catalog) == 0 {
		t.Fatalf("empty keybinding catalog")
	}

	for _, action := range catalog {
		semantics, ok := keyBindingActionSemanticsFor(action)
		if !ok {
			t.Fatalf("action %q has no declared semantics", action.ID)
		}
		if strings.TrimSpace(semantics.TargetKind) == "" || strings.TrimSpace(semantics.ResultKind) == "" {
			t.Fatalf("action %q semantics = %#v, want a target kind and a result kind", action.ID, semantics)
		}
		handler, ok := keyBindingActionHandlerFor(action)
		if !ok {
			t.Fatalf("action %q has no pinned handler", action.ID)
		}
		if strings.TrimSpace(handler.Invocation) == "" {
			t.Fatalf("action %q handler = %#v, want an exact shipped invocation", action.ID, handler)
		}
		// The handler projection is the shipped manifest, not a second table.
		if handler.Manifest != "" {
			path, route, resolved := cli.Resolve(strings.Fields(handler.Manifest))
			if !resolved || strings.Join(path, " ") != handler.Manifest {
				t.Fatalf("action %q handler manifest %q does not resolve against internal/cli", action.ID, handler.Manifest)
			}
			top, found := cli.LookupRoute(path[0])
			if !found || string(top.Disposition) != handler.Disposition {
				t.Fatalf("action %q handler disposition = %q, want the manifest disposition for %q", action.ID, handler.Disposition, path[0])
			}
			want := route.Canonical
			if len(want) == 0 {
				want = top.Canonical
			}
			if strings.Join(handler.Canonical, ",") != strings.Join(want, ",") {
				t.Fatalf("action %q handler canonical = %#v, want the manifest list %#v", action.ID, handler.Canonical, want)
			}
		}

		entries, _, err := cmd.keybindingDetailEntries(action.ID)
		if err != nil {
			t.Fatalf("keybindingDetailEntries(%q) error = %v", action.ID, err)
		}
		defaultAction, _ := keyBindingActionByID(defaultKeyBindingCatalog(), action.ID)
		if !hasEntryLabelContainingAll(entries, keyBindingDisplayName(action), keybindingState(keymapFile{}, action, defaultAction)) {
			t.Fatalf("action detail %q = %#v, want the action name and concise state", action.ID, entries)
		}
		for _, want := range []string{"Single Keys", "Sequences"} {
			if !hasEntryLabelContaining(entries, want) {
				t.Fatalf("action detail %q = %#v, want current binding section %q", action.ID, entries, want)
			}
		}
		if keyBindingEditable(action) {
			if _, protected := keyBindingProtectedActionReason(defaultAction); !protected {
				for _, want := range []string{"+ Add binding", "Enter binding manually"} {
					if !hasEntryLabelContaining(entries, want) {
						t.Fatalf("action detail %q = %#v, want interaction %q", action.ID, entries, want)
					}
				}
			}
		}
		for _, forbidden := range []string{"Target kind", "Result kind", "Placement", "Anchor", "Handler", "manifest", "boundary", "Options"} {
			if hasEntryLabelContaining(entries, forbidden) {
				t.Fatalf("action detail %q = %#v, forbidden visible internal copy %q", action.ID, entries, forbidden)
			}
		}

		// Observable result: every selectable row resolves to an operation the
		// detail loop actually handles. A row whose value parses to nothing is
		// exactly the silent no-op this slice removes.
		for _, entry := range entries {
			value := strings.TrimSpace(entry.Value)
			if value == "" || value == settingsNoopValue || value == settingsBackValue {
				continue
			}
			op, ok := parseKeymapDetailAction(value, action.ID)
			if !ok {
				t.Fatalf("action detail %q row %q does not parse to a keybinding operation", action.ID, value)
			}
			switch {
			case op == "add", op == "type", op == "unbind", op == "reset":
			case strings.HasPrefix(op, "key:"):
			case strings.HasPrefix(op, "sequence:"):
			default:
				t.Fatalf("action detail %q row %q resolves to unhandled operation %q", action.ID, value, op)
			}
		}
	}
}

func TestSettingsKeybindingActionDetailPrioritizesStateBindingsAndActions(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		configure  func(*settingsCommand) error
		wantState  string
		wantReset  bool
		wantUnbind bool
	}{
		{name: "default", wantState: "Default", wantUnbind: true},
		{
			name: "custom",
			configure: func(cmd *settingsCommand) error {
				return cmd.addKeymapAliasAndApply("ProjectSidebarToggle", "C-r", &bytes.Buffer{})
			},
			wantState: "Custom", wantReset: true, wantUnbind: true,
		},
		{
			name: "unbound",
			configure: func(cmd *settingsCommand) error {
				return cmd.saveKeymapKeysAndApply("ProjectSidebarToggle", nil, &bytes.Buffer{})
			},
			wantState: "Unbound", wantReset: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			cmd := keybindingCorrectnessCommand(t, home, nil)
			if tc.configure != nil {
				if err := tc.configure(cmd); err != nil {
					t.Fatalf("configure detail: %v", err)
				}
			}
			entries, _, err := cmd.keybindingDetailEntries("ProjectSidebarToggle")
			if err != nil {
				t.Fatalf("keybindingDetailEntries() error = %v", err)
			}
			state := entryIndexLabelContaining(entries, tc.wantState)
			single := entryIndexLabelContaining(entries, "Single Keys")
			sequences := entryIndexLabelContaining(entries, "Sequences")
			add := entryIndexValue(entries, settingsActionPrefixKeymap+"ProjectSidebarToggle:add")
			typed := entryIndexValue(entries, settingsActionPrefixKeymap+"ProjectSidebarToggle:type")
			if state < 0 || single <= state || sequences <= single || add <= sequences || typed <= add {
				t.Fatalf("interaction-first order state=%d single=%d sequences=%d add=%d typed=%d entries=%#v", state, single, sequences, add, typed, entries)
			}
			if got := hasEntryValue(entries, settingsActionPrefixKeymap+"ProjectSidebarToggle:unbind"); got != tc.wantUnbind {
				t.Fatalf("unbind visible=%v, want %v: %#v", got, tc.wantUnbind, entries)
			}
			if got := hasEntryValue(entries, settingsActionPrefixKeymap+"ProjectSidebarToggle:reset"); got != tc.wantReset {
				t.Fatalf("reset visible=%v, want %v: %#v", got, tc.wantReset, entries)
			}
			if hasEntryLabelContaining(entries, "Options") {
				t.Fatalf("detail retains redundant Options divider: %#v", entries)
			}
		})
	}
}

// TestSettingsKeybindingAnchorCopyMatchesTheShippedTransport is the guard for
// the one claim an anchor row must never make. The interactive splits resolve
// their target in aiCommand.resolveTargetPane, which reads an explicit
// TMUX_SPLIT_TARGET_PANE or `display-message -p -F '#{pane_id}'` -- a raw `%N`
// transport id. No shipped keybinding handler reads a `metadata.uid` mirror, so
// no anchor row may say uid: that is both a false assertion about the handler
// and the exact Pane vocabulary mixing (raw pane id used as a canonical uid)
// the resource contract forbids.
func TestSettingsKeybindingAnchorCopyMatchesTheShippedTransport(t *testing.T) {
	t.Parallel()

	for _, action := range defaultKeyBindingCatalog() {
		semantics, ok := keyBindingActionSemanticsFor(action)
		if !ok {
			t.Fatalf("action %q has no declared semantics", action.ID)
		}
		if strings.Contains(strings.ToLower(semantics.Anchor), "uid") {
			t.Fatalf("action %q anchor %q claims a uid; the shipped handlers only carry raw %%N transport ids", action.ID, semantics.Anchor)
		}
	}

	// The interactive splits keep the two properties the anchor contract does
	// need: an explicit target pinned at press time, and never the Window's
	// persisted primaryPaneRef.
	for _, want := range []string{"%N", "explicit split target", "not the Window primaryPaneRef"} {
		if !strings.Contains(keyBindingAnchorCurrentPaneSplitTarget, want) {
			t.Fatalf("split anchor %q missing %q", keyBindingAnchorCurrentPaneSplitTarget, want)
		}
	}
	// The direct tmux navigation commands pass no target at all, and say so.
	for _, id := range []string{"last-pane", "select-pane-left", "select-pane-right", "select-pane-up", "select-pane-down"} {
		action, ok := keyBindingActionByID(defaultKeyBindingCatalog(), id)
		if !ok {
			t.Fatalf("catalog missing %q", id)
		}
		semantics, _ := keyBindingActionSemanticsFor(action)
		if semantics.Anchor != keyBindingAnchorActiveTmuxPane {
			t.Fatalf("navigation action %q anchor = %q, want the no-explicit-target anchor", id, semantics.Anchor)
		}
	}
	// `new-window -c "#{pane_current_path}"` takes the cwd and nothing else.
	newWindow, ok := keyBindingActionByID(defaultKeyBindingCatalog(), "new-window")
	if !ok {
		t.Fatalf("catalog missing new-window")
	}
	if !strings.Contains(newWindow.TmuxBody, `-c "#{pane_current_path}"`) {
		t.Fatalf("new-window body = %q, the cwd-seed anchor no longer matches the shipped command", newWindow.TmuxBody)
	}
	newWindowSemantics, _ := keyBindingActionSemanticsFor(newWindow)
	if newWindowSemantics.Anchor != keyBindingAnchorCurrentPaneCwdSeed {
		t.Fatalf("new-window anchor = %q, want the cwd-seed anchor", newWindowSemantics.Anchor)
	}
}

// TestSettingsKeybindingKeyDetailRowsAreAllHandled is the key-detail half of
// the matrix: no row in a key detail may be a value the loop drops on the
// floor, and Test delivery is either a live Action or a disabled row that
// carries a reason and a next step.
func TestSettingsKeybindingKeyDetailRowsAreAllHandled(t *testing.T) {
	t.Parallel()

	cmd := keybindingCorrectnessCommand(t, t.TempDir(), nil)
	for _, action := range defaultKeyBindingCatalog() {
		for _, chord := range keybindingVisibleChords(action) {
			entries, _, err := cmd.keybindingKeyDetailEntries(action.ID, chord)
			if err != nil {
				t.Fatalf("keybindingKeyDetailEntries(%q, %q) error = %v", action.ID, chord, err)
			}
			if !hasEntryLabelContainingAll(entries, "Key", keybindingChordDisplay(chord)) {
				t.Fatalf("key detail %q/%q = %#v, want the current key", action.ID, chord, entries)
			}
			for _, forbidden := range []string{"Canonical key", "Delivery path"} {
				if hasEntryLabelContaining(entries, forbidden) {
					t.Fatalf("key detail %q/%q = %#v, forbidden visible copy %q", action.ID, chord, entries, forbidden)
				}
			}
			if !hasEntryLabelContaining(entries, "Test delivery") {
				t.Fatalf("key detail %q/%q = %#v, want a Test delivery row", action.ID, chord, entries)
			}
			testRow := entryWithLabelContaining(entries, "Test delivery")
			if strings.TrimSpace(testRow.Value) == settingsNoopValue {
				label := stripANSI(testRow.Label)
				alternative := entryWithLabelContaining(entries, "Try instead")
				if !strings.Contains(label, "unavailable") || alternative == nil ||
					!strings.Contains(stripANSI(alternative.Label), "projmux setup") {
					t.Fatalf("disabled Test delivery row %q and entries %#v must carry a reason and a usable alternative", label, entries)
				}
			}
			for _, entry := range entries {
				value := strings.TrimSpace(entry.Value)
				if value == "" || value == settingsNoopValue || value == settingsBackValue {
					continue
				}
				op, ok := parseKeymapDetailAction(value, action.ID)
				if !ok {
					t.Fatalf("key detail %q row %q does not parse to a keybinding operation", action.ID, value)
				}
				if !strings.HasPrefix(op, "remove:") && !strings.HasPrefix(op, "replace:") &&
					!strings.HasPrefix(op, "type-replace:") && !strings.HasPrefix(op, "test:") {
					t.Fatalf("key detail %q row %q resolves to unhandled operation %q", action.ID, value, op)
				}
			}
		}
	}
}

// keybindingRenderedSurfaceLabels renders every keybinding surface for one
// locale and returns the visible labels.
func keybindingRenderedSurfaceLabels(t *testing.T, locale string) []string {
	t.Helper()
	home := t.TempDir()
	runner := switchRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
		t.Fatalf("unexpected picker call")
		return intpickercompat.Result{}, nil
	})
	cmd := &settingsCommand{
		ai:       testAICommand(home),
		switcher: testSettingsSwitchCommand(t, &stubSwitchPinStore{}),
		homeDir:  func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == i18n.LocaleEnvName {
				return locale
			}
			return ""
		},
		runner:       runner,
		nativePicker: nativePickerFromCompatRunner(runner),
	}

	var labels []string
	collect := func(entries []intpickercompat.Entry) {
		for _, entry := range entries {
			labels = append(labels, stripANSI(entry.Label))
		}
	}
	root, err := cmd.keybindingEntries()
	if err != nil {
		t.Fatalf("keybindingEntries() error = %v", err)
	}
	collect(cmd.localizeSettingsOptions(intpickercompat.Options{UI: "settings-keybindings", Entries: root}).Entries)
	for _, category := range keyBindingCategoryOrder {
		entries, err := cmd.keybindingCategoryEntries(category.ID)
		if err != nil {
			t.Fatalf("keybindingCategoryEntries(%q) error = %v", category.ID, err)
		}
		collect(cmd.localizeSettingsOptions(intpickercompat.Options{UI: "settings-keybindings-category", Entries: entries}).Entries)
	}
	for _, surface := range keyBindingSurfaceOrder {
		entries, err := cmd.keybindingSurfaceEntries(surface.ID)
		if err != nil {
			t.Fatalf("keybindingSurfaceEntries(%q) error = %v", surface.ID, err)
		}
		collect(cmd.localizeSettingsOptions(intpickercompat.Options{UI: "settings-keybindings-surface", Entries: entries}).Entries)
	}
	for _, action := range defaultKeyBindingCatalog() {
		detail, _, err := cmd.keybindingDetailEntries(action.ID)
		if err != nil {
			t.Fatalf("keybindingDetailEntries(%q) error = %v", action.ID, err)
		}
		collect(cmd.localizeSettingsOptions(intpickercompat.Options{UI: "settings-keybinding-detail", Entries: detail}).Entries)
		for _, chord := range keybindingVisibleChords(action) {
			keyDetail, _, err := cmd.keybindingKeyDetailEntries(action.ID, chord)
			if err != nil {
				t.Fatalf("keybindingKeyDetailEntries(%q, %q) error = %v", action.ID, chord, err)
			}
			collect(cmd.localizeSettingsOptions(intpickercompat.Options{UI: "settings-keybinding-key-detail", Entries: keyDetail}).Entries)
		}
	}
	if err := cmd.addKeymapSequenceAndApply("ProjectSidebarToggle", "C-k C-p", &bytes.Buffer{}); err != nil {
		t.Fatalf("seed sequence detail: %v", err)
	}
	sequenceDetail, _, err := cmd.keybindingSequenceDetailEntries("ProjectSidebarToggle", "C-k C-p")
	if err != nil {
		t.Fatalf("keybindingSequenceDetailEntries() error = %v", err)
	}
	collect(cmd.localizeSettingsOptions(intpickercompat.Options{UI: "settings-keybinding-sequence-detail", Entries: sequenceDetail}).Entries)
	return labels
}

// TestSettingsKeybindingRetiredContainerCopyIsGone is the visible-copy
// negative guard: normal details contain neither a catch-all teaching
// container nor the internal/storage/delivery rows removed by Phase 2.
func TestSettingsKeybindingRetiredContainerCopyIsGone(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		locale   string
		retired  []string
		required []string
	}{
		{
			locale: "en-US",
			retired: []string{
				"Advanced...", "Advanced typed entry", "Troubleshooting", "Raw diagnostic view", "advanced options", "advanced diagnostics", "Test key delivery, Advanced", "Advanced delivery",
				"Target kind", "Result kind", "Placement", "Anchor", "Handler", "manifest", "boundary", "Safe direct keys", "Never saved as keys", "Terminal adapter", "Canonical key", "Canonical storage", "Delivery path", "Cancellation", "authoring and saved bytes", "saved logical strokes",
			},
			required: []string{"Enter binding manually", "Test delivery"},
		},
		{
			locale: "ko-KR",
			retired: []string{
				"고급...", "문제 해결", "원시 진단 보기", "대상 종류", "결과 종류", "배치", "앵커", "핸들러", "매니페스트", "경계", "안전한 직접 키", "키로 저장하지 않음", "터미널 어댑터", "정규 키", "정규 저장", "전달 경로", "취소 동작",
			},
			required: nil,
		},
	} {
		labels := keybindingRenderedSurfaceLabels(t, tc.locale)
		joined := strings.Join(labels, "\n")
		for _, retired := range tc.retired {
			if strings.Contains(joined, retired) {
				t.Fatalf("locale %s keybinding surfaces still render retired copy %q", tc.locale, retired)
			}
		}
		for _, required := range tc.required {
			if !strings.Contains(joined, required) {
				t.Fatalf("locale %s keybinding surfaces missing %q", tc.locale, required)
			}
		}
	}

	// The i18n catalog must not keep the retired keys alive either.
	for _, key := range []i18n.Key{
		"settings.text.advanced_ellipsis",
		"settings.text.raw_diagnostic_view",
		"settings.text.troubleshooting",
		"settings.text.test_key_delivery_advanced",
		"settings.footer.keybindings_add_key_advanced",
	} {
		if missing := i18n.DefaultCatalog().MissingFallbackKeys([]i18n.Key{key}); len(missing) == 0 {
			t.Fatalf("i18n catalog still defines retired key %q", key)
		}
	}
}

// TestSettingsKeybindingTestDeliveryIsNotANoOp is the direct negative test for
// the old behavior: selecting a `test:*` row used to fall back into the same
// loop with nothing to show. It must now produce a feedback result.
func TestSettingsKeybindingTestDeliveryIsNotANoOp(t *testing.T) {
	t.Parallel()

	var frames []intpickercompat.Options
	var calls int
	cmd := keybindingCorrectnessCommand(t, t.TempDir(), func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		frames = append(frames, options)
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsActionPrefixKeymap + "ProjectSidebarToggle:test:M-1"}, nil
		default:
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		}
	})
	cmd.probeKeybinding = func(key probeKey, _ time.Duration) (probeResult, error) {
		return classifyProbeInput(key, []byte("\x1b1")), nil
	}

	if err := cmd.runKeybindingKeyDetail("ProjectSidebarToggle", "M-1", &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runKeybindingKeyDetail() error = %v", err)
	}
	if len(frames) < 2 {
		t.Fatalf("frames = %d, want the key detail to re-render with a result", len(frames))
	}
	after := frames[len(frames)-1]
	for _, want := range []string{"Feedback", "Test delivery complete", "delivered"} {
		if !hasEntryLabelContaining(after.Entries, want) {
			t.Fatalf("frame after Test delivery = %#v, want %q", after.Entries, want)
		}
	}
}

// TestSettingsKeybindingDeliveryTestReportsEveryOutcome pins the four
// observable outcomes and the four reported fields.
func TestSettingsKeybindingDeliveryTestReportsEveryOutcome(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		chord    string
		sequence []byte
		status   keybindingDeliveryDiagnosticStatus
		wants    []string
	}{
		{
			name:     "delivered",
			chord:    "M-1",
			sequence: []byte("\x1b1"),
			status:   keybindingDeliveryDelivered,
			wants:    []string{"logical key: Alt-1 (M-1)", `raw observation: \x1b1`, "tmux received key: M-1", "delivery status: delivered"},
		},
		{
			name:     "missing",
			chord:    "M-1",
			sequence: nil,
			status:   keybindingDeliveryMissing,
			wants:    []string{"raw observation: (none)", "tmux received key: (none)", "delivery status: key-did-not-arrive"},
		},
		{
			name:     "adapter-needed",
			chord:    "M-1",
			sequence: []byte("\x1b[49;3u"),
			status:   keybindingDeliveryAdapterNeeded,
			wants:    []string{`raw observation: \x1b[49;3u`, "delivery status: adapter-needed"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := keybindingCorrectnessCommand(t, t.TempDir(), nil)
			cmd.probeKeybinding = func(key probeKey, _ time.Duration) (probeResult, error) {
				return classifyProbeInput(key, tc.sequence), nil
			}
			action, ok := keyBindingActionByID(defaultKeyBindingCatalog(), "ProjectSidebarToggle")
			if !ok {
				t.Fatalf("catalog missing ProjectSidebarToggle")
			}
			obs, err := cmd.observeKeybindingDeliveryWithProbe(action, tc.chord)
			if err != nil {
				t.Fatalf("observeKeybindingDeliveryWithProbe() error = %v", err)
			}
			if obs.Status != tc.status {
				t.Fatalf("status = %q, want %q", obs.Status, tc.status)
			}
			rendered := strings.Join(renderKeybindingDeliveryObservation(obs), "\n")
			for _, want := range tc.wants {
				if !strings.Contains(rendered, want) {
					t.Fatalf("rendered = %q, want %q", rendered, want)
				}
			}
		})
	}

	ambiguous := classifyKeybindingDeliveryObservation("Enter", "C-m", `\r`)
	if ambiguous.Status != keybindingDeliveryAmbiguous || !strings.Contains(ambiguous.Summary, "share one byte sequence") {
		t.Fatalf("ambiguous observation = %#v, want the ambiguous outcome", ambiguous)
	}
}

// TestSettingsKeybindingDeliveryTestUsesOnePickerReaderInsideTmux is the
// reader-conflict and hang guard: inside a tmux popup the controlling-TTY probe
// is never started, the picker's own recorder is the single reader, and the
// result is still observable.
func TestSettingsKeybindingDeliveryTestUsesOnePickerReaderInsideTmux(t *testing.T) {
	t.Parallel()

	var recorderOptions intpickercompat.Options
	var calls int
	cmd := keybindingCorrectnessCommand(t, t.TempDir(), func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		if options.Recorder != nil {
			recorderOptions = options
			if _, err := options.Recorder.Normalize(intpicker.RecorderKey{Name: "alt-1"}); err != nil {
				t.Fatalf("recorder Normalize(alt-1) error = %v", err)
			}
			return intpickercompat.Result{Key: "enter", Value: "M-1"}, nil
		}
		return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
	})
	cmd.lookupEnv = func(name string) string {
		if name == "TMUX" {
			return "/tmp/tmux-1000/default,1,0"
		}
		return ""
	}
	cmd.physicalCaptureAvailable = func() bool { return false }
	cmd.probeKeybinding = func(probeKey, time.Duration) (probeResult, error) {
		t.Fatalf("the controlling-TTY probe must not run while the popup owns the terminal")
		return probeResult{}, nil
	}
	cmd.nativeKeyCapture = func(context.Context) (string, bool, error) {
		t.Fatalf("native capture must not run for a delivery test")
		return "", false, nil
	}

	if got := cmd.keybindingDeliveryTestReader(); got != keybindingDeliveryTestRecorder {
		t.Fatalf("reader = %q, want the picker recorder inside tmux", got)
	}
	if err := cmd.runKeybindingDeliveryTest("ProjectSidebarToggle", "M-1", &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runKeybindingDeliveryTest() error = %v", err)
	}
	if got, want := recorderOptions.UI, "settings-keybinding-delivery-test"; got != want {
		t.Fatalf("recorder UI = %q, want %q", got, want)
	}
	if recorderOptions.Recorder.Validate != nil {
		t.Fatalf("a delivery test must not run keymap conflict validation against its own bound key")
	}
	if cmd.feedback == nil || !strings.Contains(cmd.feedback.Summary, "Test delivery complete") ||
		!strings.Contains(cmd.feedback.Detail, "delivered") {
		t.Fatalf("feedback = %#v, want an observable delivered result", cmd.feedback)
	}
}

// TestSettingsKeybindingDeliveryTestCancelIsObservable covers the recorder Esc
// path: cancelling reports a cancelled result rather than returning silently.
func TestSettingsKeybindingDeliveryTestCancelIsObservable(t *testing.T) {
	t.Parallel()

	cmd := keybindingCorrectnessCommand(t, t.TempDir(), func(options intpickercompat.Options) (intpickercompat.Result, error) {
		if options.Recorder == nil {
			t.Fatalf("expected the recorder frame, got %#v", options)
		}
		return intpickercompat.Result{Key: "esc"}, nil
	})
	cmd.lookupEnv = func(name string) string {
		if name == "TMUX" {
			return "/tmp/tmux-1000/default,1,0"
		}
		return ""
	}
	cmd.physicalCaptureAvailable = func() bool { return false }

	if err := cmd.runKeybindingDeliveryTest("ProjectSidebarToggle", "M-1", &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runKeybindingDeliveryTest() error = %v", err)
	}
	if cmd.feedback == nil || !strings.Contains(cmd.feedback.Summary, "Test delivery cancelled") {
		t.Fatalf("feedback = %#v, want an observable cancelled result", cmd.feedback)
	}
}

// TestSettingsKeybindingDeliveryTestUnavailableNamesReasonAndNextStep covers
// the untrusted-context contract: no reader, or a chord the recorder consumes
// as its own control, becomes a disabled row with a reason and the canonical
// next step -- never a row that quietly does nothing.
func TestSettingsKeybindingDeliveryTestUnavailableNamesReasonAndNextStep(t *testing.T) {
	t.Parallel()

	t.Run("no reader", func(t *testing.T) {
		t.Parallel()
		cmd := keybindingCorrectnessCommand(t, t.TempDir(), nil)
		cmd.physicalCaptureAvailable = func() bool { return false }

		if got := cmd.keybindingDeliveryTestReader(); got != keybindingDeliveryTestUnsupported {
			t.Fatalf("reader = %q, want unavailable", got)
		}
		entries, _, err := cmd.keybindingKeyDetailEntries("ProjectSidebarToggle", "M-1")
		if err != nil {
			t.Fatalf("keybindingKeyDetailEntries() error = %v", err)
		}
		row := entryWithLabelContaining(entries, "Test delivery")
		if row == nil || row.Value != settingsNoopValue {
			t.Fatalf("entries = %#v, want a disabled Test delivery row", entries)
		}
		label := stripANSI(row.Label)
		for _, want := range []string{"unavailable", "no interactive key reader"} {
			if !strings.Contains(label, want) {
				t.Fatalf("disabled row %q, want %q", label, want)
			}
		}
		if !hasEntryLabelContainingAll(entries, "Try instead", "projmux setup", "plain terminal") {
			t.Fatalf("entries = %#v, want an immediately usable alternative", entries)
		}
		if err := cmd.runKeybindingDeliveryTest("ProjectSidebarToggle", "M-1", &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatalf("runKeybindingDeliveryTest() error = %v", err)
		}
		if cmd.feedback == nil || !strings.Contains(cmd.feedback.Summary, "Test delivery unavailable") ||
			!strings.Contains(cmd.feedback.Detail, "projmux setup") {
			t.Fatalf("feedback = %#v, want an unavailable reason and a next step", cmd.feedback)
		}
	})

	t.Run("recorder control key", func(t *testing.T) {
		t.Parallel()
		cmd := keybindingCorrectnessCommand(t, t.TempDir(), nil)
		cmd.lookupEnv = func(name string) string {
			if name == "TMUX" {
				return "/tmp/tmux-1000/default,1,0"
			}
			return ""
		}
		cmd.physicalCaptureAvailable = func() bool { return false }

		entries, _, err := cmd.keybindingKeyDetailEntries("NotifySidebar:FocusAndAck", "Enter")
		if err != nil {
			t.Fatalf("keybindingKeyDetailEntries() error = %v", err)
		}
		row := entryWithLabelContaining(entries, "Test delivery")
		if row == nil || row.Value != settingsNoopValue {
			t.Fatalf("entries = %#v, want a disabled Test delivery row for a recorder control key", entries)
		}
		label := stripANSI(row.Label)
		for _, want := range []string{"unavailable", "recorder uses Enter"} {
			if !strings.Contains(label, want) {
				t.Fatalf("disabled row %q, want %q", label, want)
			}
		}
		if !hasEntryLabelContainingAll(entries, "Try instead", "projmux setup", "plain terminal") {
			t.Fatalf("entries = %#v, want an immediately usable alternative", entries)
		}
	})
}

// TestSettingsKeybindingDeliveryTestNeverWritesKeymapOrTmux pins that the
// observation stays an observation: raw delivery results are never promoted
// into the stored logical-key model and never reload tmux.
func TestSettingsKeybindingDeliveryTestNeverWritesKeymapOrTmux(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var tmuxCalls [][]string
	cmd := keybindingCorrectnessCommand(t, home, nil)
	cmd.runCommand = func(name string, args ...string) error {
		tmuxCalls = append(tmuxCalls, append([]string{name}, args...))
		return nil
	}
	cmd.probeKeybinding = func(key probeKey, _ time.Duration) (probeResult, error) {
		return classifyProbeInput(key, []byte("\x1b[49;3u")), nil
	}

	before := settingsNavConfigSnapshot(t, home)
	if err := cmd.runKeybindingDeliveryTest("ProjectSidebarToggle", "M-1", &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runKeybindingDeliveryTest() error = %v", err)
	}
	if after := settingsNavConfigSnapshot(t, home); after != before {
		t.Fatalf("delivery test changed stored config:\nbefore=%q\nafter=%q", before, after)
	}
	if len(tmuxCalls) != 0 {
		t.Fatalf("tmux calls = %#v, want none from a delivery test", tmuxCalls)
	}
	if cmd.feedback == nil || !strings.Contains(cmd.feedback.Detail, "adapter-needed") {
		t.Fatalf("feedback = %#v, want the adapter-needed observation", cmd.feedback)
	}
}

// TestSettingsKeybindingAddPathsShareNormalizationAndValidation is the
// verification-3 audit: recorder, typed entry, and captured keys go through the
// same normalization and the same pre-write validation, so all three reject the
// same chord with the same reason and none of them writes first.
func TestSettingsKeybindingAddPathsShareNormalizationAndValidation(t *testing.T) {
	t.Parallel()

	// Normalization parity: the recorder's decoded chord and the typed name of
	// the same key normalize to one spelling.
	for _, tc := range []struct {
		key   intpicker.RecorderKey
		typed string
	}{
		{intpicker.RecorderKey{Name: "f12"}, "f12"},
		{intpicker.RecorderKey{Name: "ctrl-r"}, "C-r"},
		{intpicker.RecorderKey{Text: "a"}, "a"},
	} {
		recorded, err := normalizeKeybindingRecorderKey(tc.key)
		if err != nil {
			t.Fatalf("normalizeKeybindingRecorderKey(%#v) error = %v", tc.key, err)
		}
		typed, err := normalizeKeymapTypedChord(tc.typed)
		if err != nil {
			t.Fatalf("normalizeKeymapTypedChord(%q) error = %v", tc.typed, err)
		}
		if recorded != typed {
			t.Fatalf("recorder chord %q != typed chord %q for %#v", recorded, typed, tc.key)
		}
	}

	// Rejection parity: the same conflicting chord is refused by the shared
	// validator regardless of which Add path produced it.
	home := t.TempDir()
	cmd := keybindingCorrectnessCommand(t, home, nil)
	err := cmd.validateKeymapAliasForAction("Sidebar:PinProject", "C-x")
	if err == nil || !strings.Contains(err.Error(), "bound to both Sidebar:PinProject and Sidebar:KillSession") {
		t.Fatalf("validateKeymapAliasForAction() error = %v, want the same-surface conflict", err)
	}
	if before, after := settingsNavConfigSnapshot(t, home), settingsNavConfigSnapshot(t, home); before != after {
		t.Fatalf("validation is not allowed to write configuration")
	}

	// The captured path runs the same validator before its write boundary.
	if err := cmd.addCapturedKeybindingChord("Sidebar:PinProject", "C-x", &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("addCapturedKeybindingChord() error = %v, want a handled rejection", err)
	}
	if cmd.feedback == nil || !strings.Contains(cmd.feedback.Detail, "bound to both Sidebar:PinProject and Sidebar:KillSession") {
		t.Fatalf("feedback = %#v, want the shared conflict reason", cmd.feedback)
	}
	if snapshot := settingsNavConfigSnapshot(t, home); strings.Contains(snapshot, "keymap.toml") {
		t.Fatalf("rejected capture wrote %q", snapshot)
	}
}

// TestSettingsKeybindingRecorderCancelIsObservable covers the recorder cancel
// leg of verification 3.
func TestSettingsKeybindingRecorderCancelIsObservable(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := keybindingCorrectnessCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		if options.Recorder == nil {
			t.Fatalf("expected the recorder frame")
		}
		return intpickercompat.Result{Key: "esc"}, nil
	})
	before := settingsNavConfigSnapshot(t, home)
	if err := cmd.runKeybindingRecorder("ProjectSidebarToggle", &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runKeybindingRecorder() error = %v", err)
	}
	if cmd.feedback == nil || !strings.Contains(cmd.feedback.Summary, "Keybinding cancelled") {
		t.Fatalf("feedback = %#v, want an observable cancelled result", cmd.feedback)
	}
	if after := settingsNavConfigSnapshot(t, home); after != before {
		t.Fatalf("recorder cancel wrote configuration")
	}
}

// TestSettingsKeybindingTypedNormalizationRejectionIsObservable covers the
// typed normalization leg: a raw escape payload is refused with a reason and
// the Settings popup stays open.
func TestSettingsKeybindingTypedNormalizationRejectionIsObservable(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	var calls int
	cmd := keybindingCorrectnessCommand(t, home, func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		if options.UI != "settings-keybinding-type" {
			t.Fatalf("call %d UI = %q, want the typed frame", calls, options.UI)
		}
		return intpickercompat.Result{Key: "enter", Query: `\x1b[49;3u`}, nil
	})
	if err := cmd.runKeybindingTyped("ProjectSidebarToggle", false, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runKeybindingTyped() error = %v, want a handled rejection", err)
	}
	if cmd.feedback == nil || !strings.Contains(cmd.feedback.Summary, "Keybinding failed") ||
		!strings.Contains(cmd.feedback.Detail, "escape") {
		t.Fatalf("feedback = %#v, want the normalization reason", cmd.feedback)
	}
	if snapshot := settingsNavConfigSnapshot(t, home); strings.Contains(snapshot, "keymap.toml") {
		t.Fatalf("rejected typed entry wrote %q", snapshot)
	}
}

// TestSettingsKeybindingCurrentDirectoryActionPinsItsHandler is the internal
// current-directory navigation contract. The action keeps its legacy `current`
// route: the manifest classifies the route's canonical projection as the
// read-only `get pane` cwd field, while the action's own outcome remains
// ensure-and-attach. None of that internal boundary becomes a passive detail
// row.
func TestSettingsKeybindingCurrentDirectoryActionPinsItsHandler(t *testing.T) {
	t.Parallel()

	action, ok := keyBindingActionByID(defaultKeyBindingCatalog(), "current-project-session")
	if !ok {
		t.Fatalf("catalog missing current-project-session")
	}
	if got, want := keyBindingDisplayName(action), "Open Project for Current Directory"; got != want {
		t.Fatalf("display name = %q, want %q", got, want)
	}
	semantics, ok := keyBindingActionSemanticsFor(action)
	if !ok {
		t.Fatalf("current-project-session has no semantics")
	}
	if !strings.Contains(semantics.ResultKind, "attach") {
		t.Fatalf("result kind = %q, want a real navigation outcome", semantics.ResultKind)
	}
	if !strings.Contains(semantics.Anchor, "read-only input, not the outcome") {
		t.Fatalf("anchor = %q, want the cwd query marked as an input", semantics.Anchor)
	}

	handler, ok := keyBindingActionHandlerFor(action)
	if !ok {
		t.Fatalf("current-project-session has no pinned handler")
	}
	if handler.Invocation != "projmux current" {
		t.Fatalf("handler invocation = %q, want the exact shipped route", handler.Invocation)
	}
	route, found := cli.LookupRoute("current")
	if !found {
		t.Fatalf("shipped manifest lost the `current` route")
	}
	if handler.Disposition != string(route.Disposition) || handler.Disposition != string(cli.DispositionCompatibility) {
		t.Fatalf("handler disposition = %q, want the manifest compatibility classification", handler.Disposition)
	}
	if strings.Join(handler.Canonical, ",") != strings.Join(route.Canonical, ",") {
		t.Fatalf("handler canonical = %#v, want the manifest list %#v", handler.Canonical, route.Canonical)
	}
	if !strings.Contains(handler.Note, "read-only input step only") {
		t.Fatalf("handler note = %q, want the read-only/ensure boundary spelled out", handler.Note)
	}

	cmd := keybindingCorrectnessCommand(t, t.TempDir(), nil)
	entries, _, err := cmd.keybindingDetailEntries("current-project-session")
	if err != nil {
		t.Fatalf("keybindingDetailEntries() error = %v", err)
	}
	if !hasEntryLabelContainingAll(entries, "Open Project for Current Directory", "Available") ||
		!hasEntryLabelContaining(entries, "+ Add binding") {
		t.Fatalf("current-directory detail = %#v, want state and binding actions", entries)
	}
	for _, forbidden := range []string{"Result kind", "ensure and attach the Project runtime", "read-only input, not the outcome", "projmux current", "compatibility", "get pane", "read-only input step only"} {
		if hasEntryLabelContaining(entries, forbidden) {
			t.Fatalf("current-directory detail = %#v, internal contract %q became visible", entries, forbidden)
		}
	}
}

// TestSettingsKeybindingAgentCreateAndResumeStayDistinct pins that Agent create
// and Agent resume never normalize into one action.
func TestSettingsKeybindingAgentCreateAndResumeStayDistinct(t *testing.T) {
	t.Parallel()

	catalog := defaultKeyBindingCatalog()
	creates := []string{"ai-split-codex-right", "ai-split-codex-down", "ai-split-claude-right", "ai-split-claude-down"}
	for _, id := range creates {
		action, ok := keyBindingActionByID(catalog, id)
		if !ok {
			t.Fatalf("catalog missing %q", id)
		}
		semantics, _ := keyBindingActionSemanticsFor(action)
		if !strings.Contains(semantics.ResultKind, "always a new Agent") ||
			!strings.Contains(semantics.ResultKind, "never resumes") {
			t.Fatalf("create action %q result kind = %q, want an always-new Agent result", id, semantics.ResultKind)
		}
		if semantics.Anchor != keyBindingAnchorCurrentPaneSplitTarget {
			t.Fatalf("create action %q anchor = %q, want the exact current Pane split-target anchor", id, semantics.Anchor)
		}
	}

	resume, ok := keyBindingActionByID(catalog, "AIResumePickerToggle")
	if !ok {
		t.Fatalf("catalog missing AIResumePickerToggle")
	}
	resumeSemantics, _ := keyBindingActionSemanticsFor(resume)
	if !strings.Contains(resumeSemantics.ResultKind, "resume one existing Offline or Failed Agent") ||
		!strings.Contains(resumeSemantics.ResultKind, "never creates an Agent") {
		t.Fatalf("resume result kind = %q, want an existing-Agent-only result", resumeSemantics.ResultKind)
	}
	for _, id := range creates {
		if id == resume.ID {
			t.Fatalf("create and resume collapsed onto one action id %q", id)
		}
	}
}

// TestSettingsKeybindingApplyRecoveryContractIsPreserved keeps the
// saved/prepared/running-session report and the canonical/compatibility apply
// recovery lines intact across this slice.
func TestSettingsKeybindingApplyRecoveryContractIsPreserved(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		report keymapApplyReport
		wants  []string
	}{
		{
			name: "live skipped",
			report: keymapApplyReport{
				Saved:    keymapApplyStage{Status: keymapApplyOK},
				Prepared: keymapApplyStage{Status: keymapApplyOK},
				Live:     keymapApplyStage{Status: keymapApplySkipped},
			},
			wants: []string{"Saved: ok", "Prepared: ok", "Running session: skipped", "Next: run `projmux tmux apply`"},
		},
		{
			name: "prepared failed",
			report: keymapApplyReport{
				Saved:    keymapApplyStage{Status: keymapApplyOK},
				Prepared: keymapApplyStage{Status: keymapApplyFailed},
				Live:     keymapApplyStage{Status: keymapApplySkipped},
			},
			wants: []string{"Recovery: resolve the generated tmux config error, then run `projmux tmux apply`."},
		},
		{
			name: "saved failed",
			report: keymapApplyReport{
				Saved:    keymapApplyStage{Status: keymapApplyFailed},
				Prepared: keymapApplyStage{Status: keymapApplySkipped},
				Live:     keymapApplyStage{Status: keymapApplySkipped},
			},
			wants: []string{"Recovery: fix the keymap.toml problem"},
		},
	} {
		var out bytes.Buffer
		if err := writeKeymapApplyReport(&out, tc.report); err != nil {
			t.Fatalf("%s: writeKeymapApplyReport() error = %v", tc.name, err)
		}
		for _, want := range tc.wants {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("%s: report = %q, want %q", tc.name, out.String(), want)
			}
		}
	}
}
