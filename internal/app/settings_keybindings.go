package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/platformkeys"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func (c *settingsCommand) keybindingsOptions(active string) intpickercompat.Options {
	entries, err := c.keybindingEntries()
	if err != nil {
		entries = []intpickercompat.Entry{
			c.backEntry(),
			{
				Label: c.rowLabelDim("Keymap error", err.Error()),
				Value: settingsNoopValue,
			},
		}
	}
	return intpickercompat.Options{
		UI:         "settings-keybindings",
		Entries:    entries,
		Title:      "Keybindings",
		Prompt:     "Settings > Keybindings > ",
		Footer:     projmuxFooter("Actions show their active keys and current state."),
		ExpectKeys: []string{"enter"},
		Bindings:   c.settingsCloseBindings(),
	}
}

func normalizeKeybindingsTab(active string) string {
	return settingsKeybindingsBindings
}

func (c *settingsCommand) runKeybindingsSection(stdout, stderr io.Writer) error {
	return c.runKeybindingsSectionWithActive(settingsKeybindingsBindings, stdout, stderr)
}

func (c *settingsCommand) runKeybindingsSectionWithActive(initial string, stdout, stderr io.Writer) error {
	active := settingsKeybindingsBindings
	if normalized := normalizeKeybindingsTab(initial); normalized != "" {
		active = normalized
	}
	for {
		options := c.keybindingsOptions(active)
		result, err := c.runPicker(options)
		if err != nil {
			return err
		}
		action := strings.TrimSpace(result.Value)
		if result.Key != "enter" || action == "" {
			return errSettingsClosed
		}
		if action == settingsBackValue {
			return nil
		}
		if action == settingsNoopValue {
			continue
		}
		if categoryID, ok := strings.CutPrefix(action, settingsActionPrefixKeymapCategory); ok {
			if err := c.runKeybindingCategorySection(categoryID, stdout, stderr); err != nil {
				return err
			}
			continue
		}
		return fmt.Errorf("unknown keybinding settings action: %s", action)
	}
}

// runKeybindingCategorySection drives one Keybindings category.
func (c *settingsCommand) runKeybindingCategorySection(categoryID string, stdout, stderr io.Writer) error {
	label, ok := keyBindingCategoryLabelByID(categoryID)
	if !ok {
		return fmt.Errorf("unknown keybinding category: %s", categoryID)
	}
	for {
		entries, err := c.keybindingCategoryEntries(categoryID)
		if err != nil {
			entries = []intpickercompat.Entry{
				c.backEntry(),
				{Label: c.rowLabelDim("Keymap error", err.Error()), Value: settingsNoopValue},
			}
		}
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-keybindings-category",
			Entries:    entries,
			Title:      "Keybindings - " + label,
			Prompt:     "Settings > Keybindings > " + label + " > ",
			Footer:     projmuxFooter("Actions show their active keys and current state."),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		})
		if err != nil {
			return err
		}
		action := strings.TrimSpace(result.Value)
		if result.Key != "enter" || action == "" {
			return errSettingsClosed
		}
		switch {
		case action == settingsBackValue:
			return nil
		case action == settingsNoopValue:
			continue
		case action == settingsNativeKeysToggle:
			if err := c.toggleNativeKeysSetting(stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixKeymapSurface):
			surface := strings.TrimPrefix(action, settingsActionPrefixKeymapSurface)
			if err := c.runKeybindingSurfaceSection(label, surface, stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixKeymap):
			if err := c.runKeybindingDetail(strings.TrimPrefix(action, settingsActionPrefixKeymap), stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown keybinding category action: %s", action)
		}
	}
}

// runKeybindingSurfaceSection drives one surface group inside the
// sidebar/picker category.
func (c *settingsCommand) runKeybindingSurfaceSection(categoryLabel, surface string, stdout, stderr io.Writer) error {
	surfaceLabel, ok := keyBindingSurfaceLabel(surface)
	if !ok {
		return fmt.Errorf("unknown keybinding surface: %s", surface)
	}
	for {
		entries, err := c.keybindingSurfaceEntries(surface)
		if err != nil {
			entries = []intpickercompat.Entry{
				c.backEntry(),
				{Label: c.rowLabelDim("Keymap error", err.Error()), Value: settingsNoopValue},
			}
		}
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-keybindings-surface",
			Entries:    entries,
			Title:      "Keybindings - " + surfaceLabel,
			Prompt:     "Settings > Keybindings > " + categoryLabel + " > " + surfaceLabel + " > ",
			Footer:     projmuxFooter("Actions show their active keys and current state."),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		})
		if err != nil {
			return err
		}
		action := strings.TrimSpace(result.Value)
		if result.Key != "enter" || action == "" {
			return errSettingsClosed
		}
		switch {
		case action == settingsBackValue:
			return nil
		case action == settingsNoopValue:
			continue
		case strings.HasPrefix(action, settingsActionPrefixKeymap):
			if err := c.runKeybindingDetail(strings.TrimPrefix(action, settingsActionPrefixKeymap), stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown keybinding surface action: %s", action)
		}
	}
}

func (c *settingsCommand) toggleNativeKeysSetting(stdout, stderr io.Writer) error {
	enabled, err := c.currentNativeKeysSetting()
	if err != nil {
		c.setSettingsFeedback("Native macOS keybindings failed", err.Error())
		return nil
	}
	return c.runSettingsMutation("Native macOS keybindings", stdout, stderr, func(io.Writer, io.Writer) error {
		return c.setNativeKeysSetting(!enabled)
	})
}

func keyBindingCategoryLabelByID(id string) (string, bool) {
	for _, category := range keyBindingCategoryOrder {
		if category.ID == id {
			return category.Label, true
		}
	}
	return "", false
}

func (c *settingsCommand) runKeybindingDetail(actionID string, stdout, stderr io.Writer) error {
	for {
		entries, title, err := c.keybindingDetailEntries(actionID)
		if err != nil {
			return err
		}
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-keybinding-detail",
			Entries:    entries,
			Title:      title,
			Prompt:     "Settings > Keybindings > Action > ",
			Footer:     projmuxFooter("Manage the active keys for this action."),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		})
		if err != nil {
			return err
		}
		action := strings.TrimSpace(result.Value)
		if result.Key != "enter" || action == "" {
			return errSettingsClosed
		}
		if action == settingsBackValue {
			return nil
		}
		if action == settingsNoopValue {
			continue
		}
		op, ok := parseKeymapDetailAction(action, actionID)
		if !ok {
			return fmt.Errorf("unknown keybinding detail action: %s", action)
		}
		switch op {
		case "add":
			var err error
			if c.keybindingPhysicalCaptureAvailable() {
				err = c.runKeybindingAdd(actionID, stdout, stderr)
			} else {
				err = c.runKeybindingRecorder(actionID, stdout)
			}
			if err != nil {
				return err
			}
		case "capture":
			if err := c.runKeybindingCapture(actionID, stdout); err != nil {
				return err
			}
		case "type":
			if err := c.runKeybindingTyped(actionID, false, stdout); err != nil {
				return err
			}
		case "advanced":
			if _, err := c.runKeybindingAddAdvanced(actionID, stdout, stderr); err != nil {
				return err
			}
		case "unbind":
			if err := c.runSettingsMutation("Keybinding", stdout, stderr, func(out, _ io.Writer) error {
				return c.saveKeymapKeysAndApply(actionID, nil, out)
			}); err != nil {
				return err
			}
		case "reset":
			if err := c.runSettingsMutation("Keybinding", stdout, stderr, func(out, _ io.Writer) error {
				return c.resetKeymapKeysAndApply(actionID, out)
			}); err != nil {
				return err
			}
		default:
			if chord, ok := strings.CutPrefix(op, "key:"); ok {
				if err := c.runKeybindingKeyDetail(actionID, chord, stdout, stderr); err != nil {
					return err
				}
				continue
			}
			if chord, ok := strings.CutPrefix(op, "remove:"); ok {
				if err := c.runSettingsMutation("Keybinding", stdout, stderr, func(out, _ io.Writer) error {
					return c.removeKeymapKeyAndApply(actionID, chord, out)
				}); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("unknown keybinding operation: %s", op)
		}
	}
}

func (c *settingsCommand) runKeybindingRecorder(actionID string, stdout io.Writer) error {
	_, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return err
	}
	action, ok := keyBindingActionByID(actions, actionID)
	if !ok {
		return fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	recorder := &intpicker.RecorderOptions{
		Normalize: normalizeKeybindingRecorderKey,
		Validate: func(chord string) error {
			return c.validateKeymapAliasForAction(action.ID, chord)
		},
	}
	result, err := c.runPicker(intpickercompat.Options{
		UI:            "settings-keybinding-recorder",
		Title:         "Record Key - " + keyBindingDisplayName(action),
		Header:        "Action: " + keyBindingDisplayName(action),
		Footer:        projmuxFooter("Enter confirms · Esc cancels · Advanced typed entry is available from Action detail."),
		DisableSearch: true,
		Bindings:      c.settingsCloseBindings(),
		Recorder:      recorder,
	})
	if err != nil {
		return err
	}
	if result.Key == "esc" {
		return nil
	}
	if result.Key != "enter" {
		return errSettingsClosed
	}
	chord := strings.TrimSpace(result.Value)
	if chord == "" {
		return nil
	}
	return c.addKeymapAliasAndApply(action.ID, chord, stdout)
}

func normalizeKeybindingRecorderKey(key intpicker.RecorderKey) (string, error) {
	if key.Text != "" {
		if key.Text == " " {
			return normalizeKeymapTypedChord("Space")
		}
		if len([]rune(key.Text)) != 1 {
			return "", fmt.Errorf("input is not a single key")
		}
		return normalizeKeymapTypedChord(key.Text)
	}
	name := strings.TrimSpace(key.Name)
	if name == "" {
		return "", fmt.Errorf("input has no stable key name")
	}
	parts := strings.Split(name, "-")
	var modifiers []string
	for len(parts) > 1 {
		switch parts[0] {
		case "ctrl":
			modifiers = append(modifiers, "C")
		case "alt":
			modifiers = append(modifiers, "M")
		case "shift":
			modifiers = append(modifiers, "S")
		default:
			goto base
		}
		parts = parts[1:]
	}
base:
	baseName := strings.Join(parts, "-")
	switch baseName {
	case "left":
		baseName = "Left"
	case "right":
		baseName = "Right"
	case "up":
		baseName = "Up"
	case "down":
		baseName = "Down"
	case "home":
		baseName = "Home"
	case "end":
		baseName = "End"
	case "page-up":
		baseName = "PPage"
	case "page-down":
		baseName = "NPage"
	case "delete":
		baseName = "DC"
	case "backspace":
		baseName = "BSpace"
	case "tab":
		baseName = "Tab"
	case "enter":
		baseName = "Enter"
	case "esc":
		baseName = "Escape"
	}
	if baseName == "Enter" || baseName == "Escape" {
		if len(modifiers) == 0 {
			return "", fmt.Errorf("plain %s is a recorder control; use Advanced typed entry", baseName)
		}
	}
	chord := baseName
	if len(modifiers) != 0 {
		chord = strings.Join(modifiers, "-") + "-" + baseName
	}
	return normalizeKeymapTypedChord(chord)
}

func (c *settingsCommand) validateKeymapAliasForAction(actionID, chord string) error {
	chord, err := normalizeKeymapTypedChord(chord)
	if err != nil {
		return err
	}
	current, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return err
	}
	action, ok := keyBindingActionByID(actions, actionID)
	if !ok {
		return fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	defaultAction, ok := keyBindingActionByID(defaultKeyBindingCatalog(), action.ID)
	if !ok {
		return fmt.Errorf("unknown keybinding action: %s", action.ID)
	}
	var keys []string
	if action.Tier == keyBindingTierTransportDependent {
		if chord == strings.TrimSpace(defaultAction.PlainChord) {
			return fmt.Errorf("key %q is this action's transport default; choose a separate custom key", chord)
		}
		keys = append(keys, keymapConfiguredAliasChords(current, defaultAction)...)
	} else {
		keys = append(keys, keyBindingEffectivePlainChords(action)...)
	}
	keys = append(keys, chord)
	if current.Bindings == nil {
		current.Bindings = map[string]keymapOverride{}
	}
	override := current.Bindings[action.ID]
	override.Plain = nil
	override.KeysSet = true
	override.Keys = uniqueNonEmptyStrings(keys)
	current.Bindings[action.ID] = override
	_, err = mergeKeymapOverrides(defaultKeyBindingCatalog(), current)
	return err
}

func parseKeymapDetailAction(value, actionID string) (string, bool) {
	action, ok := keyBindingActionByID(defaultKeyBindingCatalog(), actionID)
	if !ok {
		action = keyBindingAction{ID: actionID}
	}
	var matched bool
	for _, id := range keyBindingActionAliases(action) {
		prefix := settingsActionPrefixKeymap + id + ":"
		if strings.HasPrefix(value, prefix) {
			actionID = id
			matched = true
			break
		}
	}
	if !matched {
		return "", false
	}
	op := strings.TrimPrefix(value, settingsActionPrefixKeymap+actionID+":")
	switch {
	case op == "add", op == "advanced", op == "capture", op == "type", op == "reset", op == "unbind":
		return op, true
	case strings.HasPrefix(op, "key:"):
		return op, true
	case strings.HasPrefix(op, "remove:"):
		return op, true
	case strings.HasPrefix(op, "test:"):
		return op, true
	}
	return "", false
}

func (c *settingsCommand) runKeybindingAdd(actionID string, stdout, stderr io.Writer) error {
	for {
		entries, title, err := c.keybindingAddEntries(actionID)
		if err != nil {
			return err
		}
		footer := "Press a key to add it to this action."
		if !c.keybindingPhysicalCaptureAvailable() {
			footer = "Physical key capture is unavailable here; enter a key name."
		}
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-keybinding-add",
			Entries:    entries,
			Title:      title,
			Prompt:     "Settings > Keybindings > Action > Add key > ",
			Footer:     projmuxFooter(footer),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		})
		if err != nil {
			return err
		}
		action := strings.TrimSpace(result.Value)
		if result.Key != "enter" || action == "" {
			return errSettingsClosed
		}
		if action == settingsBackValue {
			return nil
		}
		if action == settingsNoopValue {
			continue
		}
		op, ok := parseKeymapDetailAction(action, actionID)
		if !ok {
			return fmt.Errorf("unknown keybinding add action: %s", action)
		}
		switch op {
		case "capture":
			return c.runKeybindingCapture(actionID, stdout)
		case "type":
			return c.runKeybindingTyped(actionID, false, stdout)
		case "advanced":
			done, err := c.runKeybindingAddAdvanced(actionID, stdout, stderr)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		default:
			return fmt.Errorf("unknown keybinding add operation: %s", op)
		}
	}
}

func (c *settingsCommand) runKeybindingAddAdvanced(actionID string, stdout, stderr io.Writer) (bool, error) {
	for {
		entries, title, err := c.keybindingAddAdvancedEntries(actionID)
		if err != nil {
			return false, err
		}
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-keybinding-add-advanced",
			Entries:    entries,
			Title:      title,
			Prompt:     "Settings > Keybindings > Action > Add key > Advanced > ",
			Footer:     projmuxFooter("Typed key names and raw diagnostics stay advanced."),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		})
		if err != nil {
			return false, err
		}
		action := strings.TrimSpace(result.Value)
		if result.Key != "enter" || action == "" {
			return false, errSettingsClosed
		}
		if action == settingsBackValue {
			return false, nil
		}
		if action == settingsNoopValue {
			continue
		}
		op, ok := parseKeymapDetailAction(action, actionID)
		if !ok {
			return false, fmt.Errorf("unknown keybinding add advanced action: %s", action)
		}
		switch op {
		case "type":
			return true, c.runKeybindingTyped(actionID, false, stdout)
		default:
			return false, fmt.Errorf("unknown keybinding add advanced operation: %s", op)
		}
	}
}

func (c *settingsCommand) runKeybindingKeyDetail(actionID, chord string, stdout, stderr io.Writer) error {
	for {
		entries, title, err := c.keybindingKeyDetailEntries(actionID, chord)
		if err != nil {
			return err
		}
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-keybinding-key-detail",
			Entries:    entries,
			Title:      title,
			Prompt:     "Settings > Keybindings > Action > Key > ",
			Footer:     projmuxFooter("Manage this active key."),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		})
		if err != nil {
			return err
		}
		action := strings.TrimSpace(result.Value)
		if result.Key != "enter" || action == "" {
			return errSettingsClosed
		}
		if action == settingsBackValue {
			return nil
		}
		if action == settingsNoopValue {
			continue
		}
		op, ok := parseKeymapDetailAction(action, actionID)
		if !ok {
			return fmt.Errorf("unknown keybinding key action: %s", action)
		}
		switch {
		case strings.HasPrefix(op, "remove:"):
			removeChord := strings.TrimPrefix(op, "remove:")
			return c.runSettingsMutation("Keybinding", stdout, stderr, func(out, _ io.Writer) error {
				return c.removeKeymapKeyAndApply(actionID, removeChord, out)
			})
		case strings.HasPrefix(op, "test:"):
			continue
		default:
			return fmt.Errorf("unknown keybinding key operation: %s", op)
		}
	}
}

// keybindingPhysicalCaptureAvailable reports whether the "Press a key"
// physical-capture flow can actually receive input in this context. Native
// physical capture (macOS) always qualifies. The terminal fallback reads the
// controlling /dev/tty directly, which only receives input when Settings is
// not running inside a tmux display-popup (the popup client owns the tty), so
// inside tmux that path would block until the probe timeout.
func (c *settingsCommand) keybindingPhysicalCaptureAvailable() bool {
	if c.physicalCaptureAvailable != nil {
		return c.physicalCaptureAvailable()
	}
	if c.nativeKeyCapture != nil && platformkeys.Available() {
		return true
	}
	if c.probeKeybinding != nil {
		return true
	}
	env := c.lookupEnv
	if env == nil {
		env = os.Getenv
	}
	return strings.TrimSpace(env("TMUX")) == ""
}

func (c *settingsCommand) keybindingPrefersNativeCapture() bool {
	if c.preferNativeKeyCapture != nil {
		return c.preferNativeKeyCapture()
	}
	return platformkeys.Available()
}

func (c *settingsCommand) keybindingNativeCaptureGrace() time.Duration {
	if c.nativeKeyCaptureGrace > 0 {
		return c.nativeKeyCaptureGrace
	}
	return 100 * time.Millisecond
}

func (c *settingsCommand) runKeybindingCapture(actionID string, stdout io.Writer) error {
	if !c.keybindingPhysicalCaptureAvailable() {
		fmt.Fprintln(stdout, "physical key capture is unavailable in this context; enter a key name instead")
		return c.runKeybindingTyped(actionID, false, stdout)
	}
	_, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return err
	}
	action, ok := keyBindingActionByID(actions, actionID)
	if !ok {
		return fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	key := captureProbeKeyForAction(action)
	fmt.Fprintf(stdout, "capturing custom key for %s; press the key you want to add\n", keyBindingDisplayName(action))

	ctx, cancel := context.WithTimeout(context.Background(), defaultProbeTimeout)
	defer cancel()
	type probeCapture struct {
		result probeResult
		err    error
	}
	startProbe := func() <-chan probeCapture {
		results := make(chan probeCapture, 1)
		go func() {
			res, err := c.probeLabKeybindingContext(ctx, key, defaultProbeTimeout)
			results <- probeCapture{result: res, err: err}
		}()
		return results
	}
	probeResultCh := startProbe()
	type nativeCapture struct {
		chord    string
		captured bool
		err      error
	}
	var nativeResultCh <-chan nativeCapture
	if c.nativeKeyCapture != nil {
		results := make(chan nativeCapture, 1)
		nativeResultCh = results
		go func() {
			chord, captured, err := c.nativeKeyCapture(ctx)
			results <- nativeCapture{chord: chord, captured: captured, err: err}
		}()
	}

	var res probeResult
	var pendingProbe *probeCapture
	var nativeGraceTimer *time.Timer
	var nativeGrace <-chan time.Time
	defer func() {
		if nativeGraceTimer != nil {
			nativeGraceTimer.Stop()
		}
	}()
	preferNative := c.keybindingPrefersNativeCapture()
	applyNative := func(native nativeCapture) (bool, error) {
		if native.err != nil {
			fmt.Fprintf(stdout, "native physical-key capture unavailable: %v; using terminal capture\n", native.err)
			return false, nil
		}
		if !native.captured || strings.TrimSpace(native.chord) == "" {
			return false, nil
		}
		cancel()
		chord, err := normalizeKeymapTypedChord(native.chord)
		if err != nil {
			return true, err
		}
		fmt.Fprintf(stdout, "capture custom key: OK native physical key %s\n", chord)
		fmt.Fprintln(stdout, "capture result:")
		fmt.Fprintln(stdout, "  logical key: "+keybindingChordDisplay(chord))
		fmt.Fprintln(stdout, "  raw bytes: (native physical event)")
		fmt.Fprintln(stdout, "  tmux received key: "+chord)
		fmt.Fprintln(stdout, "  delivery status: delivered - physical key captured before terminal encoding")
		return true, c.addKeymapAliasAndApply(action.ID, chord, stdout)
	}

captureLoop:
	for {
		select {
		case native := <-nativeResultCh:
			nativeResultCh = nil
			handled, err := applyNative(native)
			if handled || err != nil {
				return err
			}
			if pendingProbe == nil {
				continue
			}
			cancel()
			if pendingProbe.err != nil {
				return pendingProbe.err
			}
			res = pendingProbe.result
			break captureLoop
		case probe := <-probeResultCh:
			if preferNative && nativeResultCh != nil {
				if probe.err == nil && isAmbiguousEnterSequence(probe.result.Sequence) {
					// Enter selected the Press a key row; it is a recorder
					// control, not a candidate. Read the operator's next key.
					probeResultCh = startProbe()
					continue
				}
				pendingProbe = &probe
				probeResultCh = nil
				if probe.err == nil {
					nativeGraceTimer = time.NewTimer(c.keybindingNativeCaptureGrace())
					nativeGrace = nativeGraceTimer.C
				}
				continue
			}
			cancel()
			if probe.err != nil {
				return probe.err
			}
			res = probe.result
			break captureLoop
		case <-nativeGrace:
			cancel()
			if pendingProbe.err != nil {
				return pendingProbe.err
			}
			res = pendingProbe.result
			break captureLoop
		}
	}
	fmt.Fprintf(stdout, "capture custom key: %s\n", renderProbeStatus(res))
	for _, line := range renderKeybindingDeliveryDiagnostic(res) {
		fmt.Fprintln(stdout, line)
	}

	switch res.Status {
	case probeStatusPlain:
		chord, ok := captureResultPlainChord(res)
		if !ok {
			fmt.Fprintf(stdout, "captured key is plain, but Settings could not normalize it to a key name\n")
			return nil
		}
		return c.addKeymapAliasAndApply(action.ID, chord, stdout)
	case probeStatusUnknown:
		chord, ok := suggestedPlainChordForSequence(res.Sequence)
		if !ok {
			fmt.Fprintf(stdout, "captured raw sequence %s is not safe to persist; type a custom key name instead\n", visibleEscape(string(res.Sequence)))
			return nil
		}
		return c.addKeymapAliasAndApply(action.ID, chord, stdout)
	case probeStatusTimeout:
		fmt.Fprintf(stdout, "no key was captured; nothing changed\n")
		return nil
	default:
		return fmt.Errorf("unknown keybinding capture status: %s", res.Status)
	}
}

func (c *settingsCommand) runKeybindingTyped(actionID string, replace bool, stdout io.Writer) error {
	_, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return err
	}
	action, ok := keyBindingActionByID(actions, actionID)
	if !ok {
		return fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	mode := "Add Key"
	if replace {
		mode = "Replace Keys"
	}
	result, err := c.runPicker(intpickercompat.Options{
		UI:          "settings-keybinding-type",
		Entries:     []intpickercompat.Entry{c.backEntry(), {Label: c.rowLabelInfo("Action", keyBindingDisplayName(action), keybindingAliasesSummary(action)), Value: settingsNoopValue}},
		Title:       mode + " - " + keyBindingDisplayName(action),
		Prompt:      "Enter key > ",
		Footer:      projmuxFooter("Enter a key such as C-r, M-a, M-S-Left, or C-Space."),
		ExpectKeys:  []string{"enter"},
		Bindings:    c.settingsCloseBindings(),
		AcceptQuery: true,
	})
	if err != nil {
		return err
	}
	if result.Key != "enter" {
		return nil
	}
	chord, err := normalizeKeymapTypedChord(result.Query)
	if err != nil {
		return err
	}
	if chord == "" {
		return nil
	}
	if replace {
		return c.saveKeymapKeysAndApply(action.ID, []string{chord}, stdout)
	}
	return c.addKeymapAliasAndApply(action.ID, chord, stdout)
}

func captureProbeKeyForAction(action keyBindingAction) probeKey {
	return probeKey{
		ActionID: action.ID,
		Label:    "custom key",
		Action:   "custom key for " + keyBindingDisplayName(action),
	}
}

func captureResultPlainChord(res probeResult) (string, bool) {
	if chord := strings.TrimSpace(res.Key.PlainChord); chord != "" {
		return chord, true
	}
	return suggestedPlainChordForSequence(res.Sequence)
}

type keybindingDeliveryDiagnosticStatus string

const (
	keybindingDeliveryDelivered     keybindingDeliveryDiagnosticStatus = "delivered"
	keybindingDeliveryMissing       keybindingDeliveryDiagnosticStatus = "key-did-not-arrive"
	keybindingDeliveryAmbiguous     keybindingDeliveryDiagnosticStatus = "ambiguous-key"
	keybindingDeliveryAdapterNeeded keybindingDeliveryDiagnosticStatus = "adapter-needed"
)

type keybindingDeliveryDiagnostic struct {
	Status          keybindingDeliveryDiagnosticStatus
	LogicalKey      string
	RawBytes        string
	TmuxReceivedKey string
	Summary         string
}

func keybindingDeliveryDiagnosticForProbe(res probeResult) keybindingDeliveryDiagnostic {
	diag := keybindingDeliveryDiagnostic{
		LogicalKey:      strings.TrimSpace(res.Key.Label),
		RawBytes:        visibleEscape(string(res.Sequence)),
		TmuxReceivedKey: "(none)",
	}
	if diag.LogicalKey == "" {
		diag.LogicalKey = "custom key"
	}
	if len(res.Sequence) == 0 {
		diag.Status = keybindingDeliveryMissing
		diag.RawBytes = "(none)"
		diag.Summary = "key did not arrive; terminal or OS likely intercepted it before tmux"
		return diag
	}
	if isAmbiguousEnterSequence(res.Sequence) {
		diag.Status = keybindingDeliveryAmbiguous
		diag.TmuxReceivedKey = "Enter / C-m"
		diag.Summary = "ambiguous key; Enter and Ctrl-M share this byte sequence"
		return diag
	}
	if chord := strings.TrimSpace(res.Key.PlainChord); chord != "" && res.Status == probeStatusPlain {
		diag.Status = keybindingDeliveryDelivered
		diag.TmuxReceivedKey = chord
		diag.Summary = "logical key reached tmux as the expected plain key"
		return diag
	}
	if chord, ok := suggestedPlainChordForSequence(res.Sequence); ok && res.Key.Plain == "" {
		diag.Status = keybindingDeliveryDelivered
		diag.TmuxReceivedKey = chord
		diag.Summary = "captured bytes can be saved as a safe tmux plain key"
		return diag
	}
	if chord, ok := suggestedPlainChordForSequence(res.Sequence); ok && res.Status == probeStatusPlain {
		diag.Status = keybindingDeliveryDelivered
		diag.TmuxReceivedKey = chord
		diag.Summary = "logical key reached tmux as a plain key"
		return diag
	}
	diag.Status = keybindingDeliveryAdapterNeeded
	diag.TmuxReceivedKey = "not a saved tmux key"
	diag.Summary = "adapter-needed key; use a Projmux terminal adapter snippet/apply path or choose a safe direct key"
	return diag
}

func renderKeybindingDeliveryDiagnostic(res probeResult) []string {
	diag := keybindingDeliveryDiagnosticForProbe(res)
	return []string{
		"capture result:",
		"  logical key: " + diag.LogicalKey,
		"  raw bytes: " + diag.RawBytes,
		"  tmux received key: " + diag.TmuxReceivedKey,
		"  delivery status: " + string(diag.Status) + " - " + diag.Summary,
	}
}

// keybindingEntries renders the Keybindings root: one row per navigation
// category. The flat action wall is gone; every action is reachable through
// exactly one category, and search still crosses categories because each
// category row carries its members' search text.
func (c *settingsCommand) keybindingEntries() ([]intpickercompat.Entry, error) {
	keymap, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return nil, err
	}
	locale := c.locale()
	entries := make([]intpickercompat.Entry, 0, len(keyBindingCategoryOrder)+1)
	entries = append(entries, c.backEntry())
	for _, category := range keyBindingCategoryOrder {
		if category.ID == keyBindingCategoryInput {
			entries = append(entries, intpickercompat.Entry{
				Label:     c.rowLabel(settingsGlyphOpen, settingsColorType, category.Label, c.keybindingInputDeliverySummary()),
				Value:     settingsActionPrefixKeymapCategory + category.ID,
				SearchKey: "input delivery native macOS keybindings Accessibility Option",
			})
			continue
		}
		members := keybindingActionsInCategory(actions, category.ID)
		entries = append(entries, intpickercompat.Entry{
			Label:     c.rowLabel(settingsGlyphOpen, settingsColorType, category.Label, fmt.Sprintf("%d actions", len(members))),
			Value:     settingsActionPrefixKeymapCategory + category.ID,
			SearchKey: keybindingCategorySearchText(locale, keymap, members),
		})
	}
	return entries, nil
}

// keybindingActionsInCategory returns the catalog actions assigned to one
// category, in catalog order.
func keybindingActionsInCategory(actions []keyBindingAction, category string) []keyBindingAction {
	members := make([]keyBindingAction, 0, len(actions))
	for _, action := range actions {
		if assigned, ok := keyBindingActionCategory(action); ok && assigned == category {
			members = append(members, action)
		}
	}
	return members
}

func keybindingCategorySearchText(locale i18n.Locale, keymap keymapFile, members []keyBindingAction) string {
	parts := make([]string, 0, len(members)*3)
	defaults := defaultKeyBindingCatalog()
	for _, action := range members {
		defaultAction, _ := keyBindingActionByID(defaults, action.ID)
		parts = append(parts,
			action.ID,
			keyBindingDisplayName(action),
			action.Surface,
			action.Description,
			strings.Join(keybindingVisibleChords(action), " "),
			keybindingState(keymap, action, defaultAction),
			keybindingLocalizedSearchText(locale, action),
		)
	}
	return strings.Join(parts, " ")
}

func (c *settingsCommand) keybindingInputDeliverySummary() string {
	enabled, err := c.currentNativeKeysSetting()
	switch {
	case err != nil:
		return "global config unreadable"
	case !nativeKeysEnvEnabled(c.lookupEnv):
		return "native macOS keybindings off - PROJMUX_NATIVE_KEYS override"
	case enabled:
		return "native macOS keybindings on"
	default:
		return "native macOS keybindings off"
	}
}

// keybindingCategoryEntries renders one category. The sidebar/picker category
// nests one more level by surface, because its actions only make sense
// alongside the surface that owns them.
func (c *settingsCommand) keybindingCategoryEntries(category string) ([]intpickercompat.Entry, error) {
	if category == keyBindingCategoryInput {
		return c.keybindingInputDeliveryEntries(), nil
	}
	keymap, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return nil, err
	}
	locale := c.locale()
	members := keybindingActionsInCategory(actions, category)
	entries := []intpickercompat.Entry{c.backEntry()}
	if category == keyBindingCategorySurfaces {
		for _, surface := range keyBindingSurfaceOrder {
			surfaceMembers := keybindingActionsInSurface(members, surface.ID)
			if len(surfaceMembers) == 0 {
				continue
			}
			entries = append(entries, intpickercompat.Entry{
				Label:     c.rowLabel(settingsGlyphOpen, settingsColorType, surface.Label, fmt.Sprintf("%d actions", len(surfaceMembers))),
				Value:     settingsActionPrefixKeymapSurface + surface.ID,
				SearchKey: keybindingCategorySearchText(locale, keymap, surfaceMembers),
			})
		}
		return entries, nil
	}
	return append(entries, c.keybindingActionEntries(keymap, members)...), nil
}

func keybindingActionsInSurface(actions []keyBindingAction, surface string) []keyBindingAction {
	members := make([]keyBindingAction, 0, len(actions))
	for _, action := range actions {
		if action.Surface == surface {
			members = append(members, action)
		}
	}
	return members
}

func (c *settingsCommand) keybindingSurfaceEntries(surface string) ([]intpickercompat.Entry, error) {
	keymap, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return nil, err
	}
	members := keybindingActionsInSurface(keybindingActionsInCategory(actions, keyBindingCategorySurfaces), surface)
	return append([]intpickercompat.Entry{c.backEntry()}, c.keybindingActionEntries(keymap, members)...), nil
}

// keybindingActionEntries renders action rows with their active keys and
// state. Each row opens the action detail; no row mutates a binding.
func (c *settingsCommand) keybindingActionEntries(keymap keymapFile, members []keyBindingAction) []intpickercompat.Entry {
	defaults := defaultKeyBindingCatalog()
	locale := c.locale()
	entries := make([]intpickercompat.Entry, 0, len(members))
	for _, action := range members {
		defaultAction, _ := keyBindingActionByID(defaults, action.ID)
		state := keybindingState(keymap, action, defaultAction)
		displayName := keyBindingDisplayName(action)
		entries = append(entries, intpickercompat.Entry{
			Label:     c.rowLabel(settingsGlyphOpen, settingsColorType, displayName, keybindingListSummary(action, state)),
			Value:     settingsActionPrefixKeymap + action.ID,
			SearchKey: strings.Join([]string{action.ID, displayName, action.Surface, action.Description, strings.Join(keybindingVisibleChords(action), " "), keybindingLocalizedSearchText(locale, action)}, " "),
		})
	}
	return entries
}

// keybindingInputDeliveryEntries is the Input delivery category: the native
// key toggle only. It holds no keymap action, which is why it is declared as a
// category rather than derived from the action catalog.
func (c *settingsCommand) keybindingInputDeliveryEntries() []intpickercompat.Entry {
	entries := []intpickercompat.Entry{c.backEntry()}
	enabled, settingErr := c.currentNativeKeysSetting()
	switch {
	case settingErr != nil:
		entries = append(entries, intpickercompat.Entry{
			Label:     c.rowLabelDim("Native macOS keybindings", "global config unreadable - "+settingErr.Error()),
			Value:     settingsNoopValue,
			SearchKey: "native macOS keybindings Accessibility Option",
		})
	case !nativeKeysEnvEnabled(c.lookupEnv):
		entries = append(entries, intpickercompat.Entry{
			Label:     c.rowLabel(settingsGlyphInactive, settingsColorDim, "Native macOS keybindings", "off - PROJMUX_NATIVE_KEYS override"),
			Value:     settingsNativeKeysToggle,
			SearchKey: "native macOS keybindings Accessibility Option PROJMUX_NATIVE_KEYS off",
		})
	case enabled:
		entries = append(entries, intpickercompat.Entry{
			Label:     c.rowLabel(settingsGlyphToggle, settingsColorAdd, "Native macOS keybindings", "on - modified chords only, processed locally"),
			Value:     settingsNativeKeysToggle,
			SearchKey: "native macOS keybindings Accessibility Option on",
		})
	default:
		entries = append(entries, intpickercompat.Entry{
			Label:     c.rowLabel(settingsGlyphInactive, settingsColorDim, "Native macOS keybindings", "off - broker and Accessibility prompt disabled"),
			Value:     settingsNativeKeysToggle,
			SearchKey: "native macOS keybindings Accessibility Option off",
		})
	}
	return entries
}

// keybindingSemanticEntries renders the action's product semantics as
// read-only rows: what it targets, what it produces, and — for the interactive
// splits — where the result lands and which anchor it is placed against.
func (c *settingsCommand) keybindingSemanticEntries(action keyBindingAction) []intpickercompat.Entry {
	semantics, ok := keyBindingActionSemanticsFor(action)
	if !ok {
		return nil
	}
	entries := make([]intpickercompat.Entry, 0, 4)
	for _, row := range [][2]string{
		{"Target kind", semantics.TargetKind},
		{"Result kind", semantics.ResultKind},
		{"Placement", semantics.Placement},
		{"Anchor", semantics.Anchor},
	} {
		if strings.TrimSpace(row[1]) == "" {
			continue
		}
		entries = append(entries, intpickercompat.Entry{
			Label:     c.rowLabelInfo(row[0], row[1], ""),
			Value:     settingsNoopValue,
			SearchKey: strings.ToLower(row[0] + " " + row[1]),
		})
	}
	return entries
}

func (c *settingsCommand) keybindingDetailEntries(actionID string) ([]intpickercompat.Entry, string, error) {
	keymap, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return nil, "", err
	}
	action, ok := keyBindingActionByID(actions, actionID)
	if !ok {
		return nil, "", fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	defaultAction, _ := keyBindingActionByID(defaultKeyBindingCatalog(), actionID)
	state := keybindingState(keymap, action, defaultAction)
	entries := []intpickercompat.Entry{
		c.backEntry(),
		{
			Label: c.rowLabelInfo(keyBindingDisplayName(action), state, action.Description),
			Value: settingsNoopValue,
		},
		{
			Label: c.rowLabelInfo("Keys", keybindingAliasesSummary(action), keybindingKeysDetail(action)),
			Value: settingsNoopValue,
		},
	}
	entries = append(entries, c.keybindingSemanticEntries(action)...)
	prefix := settingsActionPrefixKeymap + action.ID + ":"
	for _, key := range keybindingVisibleChords(action) {
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphOpen, settingsColorType, keybindingChordDisplay(key), "key detail"),
			Value: prefix + "key:" + key,
		})
	}
	if !keyBindingEditable(action) {
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphOpen, settingsColorType, "Troubleshooting", "Test key delivery, Advanced..."),
			Value: settingsNoopValue,
		})
		title := "Keybinding - " + keyBindingDisplayName(action)
		return entries, title, nil
	}
	addKeyHint := "press desired key"
	if !c.keybindingPhysicalCaptureAvailable() {
		addKeyHint = "record and confirm a key combination"
	}
	entries = append(entries, intpickercompat.Entry{
		Label: c.rowLabel(settingsGlyphAdd, settingsColorAdd, "+ Add key", addKeyHint),
		Value: prefix + "add",
	})
	if !c.keybindingPhysicalCaptureAvailable() {
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphOpen, settingsColorType, "Advanced...", "type literal or nonstandard tmux key name"),
			Value: prefix + "advanced",
		})
	}
	entries = append(entries, intpickercompat.Entry{
		Label: c.rowLabelInfo("Options", keybindingActionsSummary(keymap, action, defaultAction), "choose a row below"),
		Value: settingsNoopValue,
	})
	if len(keybindingVisibleChords(action)) != 0 {
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphRemove, settingsColorRemove, "Unbind", "remove all active keys"),
			Value: prefix + "unbind",
		})
	}
	if keybindingShowResetAction(state) {
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphBack, settingsColorBack, keybindingResetActionLabel(state, defaultAction), keybindingResetExplanation(defaultAction)),
			Value: prefix + "reset",
		})
	}
	entries = append(entries, intpickercompat.Entry{
		Label: c.rowLabel(settingsGlyphOpen, settingsColorType, "Troubleshooting", "Test key delivery, Advanced..."),
		Value: settingsNoopValue,
	})
	title := "Keybinding - " + keyBindingDisplayName(action)
	return entries, title, nil
}

func (c *settingsCommand) keybindingAddEntries(actionID string) ([]intpickercompat.Entry, string, error) {
	_, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return nil, "", err
	}
	action, ok := keyBindingActionByID(actions, actionID)
	if !ok {
		return nil, "", fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	prefix := settingsActionPrefixKeymap + action.ID + ":"
	if !c.keybindingPhysicalCaptureAvailable() {
		entries := []intpickercompat.Entry{
			{
				Label: c.rowLabel(settingsGlyphType, settingsColorType, "Enter key name", "type a tmux key name"),
				Value: prefix + "type",
			},
			{
				Label: c.rowLabelDim("Press a key", "physical capture unavailable on this platform - type a key name"),
				Value: settingsNoopValue,
			},
			{
				Label: c.rowLabel(settingsGlyphBack, settingsColorBack, "Cancel", "return to action"),
				Value: settingsBackValue,
			},
			{
				Label: c.rowLabel(settingsGlyphOpen, settingsColorType, "Advanced...", "advanced options"),
				Value: prefix + "advanced",
			},
		}
		return entries, "Add Key - " + keyBindingDisplayName(action), nil
	}
	entries := []intpickercompat.Entry{
		{
			Label: c.rowLabel(settingsGlyphType, settingsColorType, "Press a key", "capture desired key"),
			Value: prefix + "capture",
		},
		{
			Label: c.rowLabel(settingsGlyphBack, settingsColorBack, "Cancel", "return to action"),
			Value: settingsBackValue,
		},
		{
			Label: c.rowLabel(settingsGlyphOpen, settingsColorType, "Advanced...", "advanced options"),
			Value: prefix + "advanced",
		},
	}
	return entries, "Add Key - " + keyBindingDisplayName(action), nil
}

func (c *settingsCommand) keybindingAddAdvancedEntries(actionID string) ([]intpickercompat.Entry, string, error) {
	_, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return nil, "", err
	}
	action, ok := keyBindingActionByID(actions, actionID)
	if !ok {
		return nil, "", fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	prefix := settingsActionPrefixKeymap + action.ID + ":"
	entries := []intpickercompat.Entry{
		c.backEntry(),
		{
			Label: c.rowLabel(settingsGlyphType, settingsColorType, "Enter key name", "type a tmux key name"),
			Value: prefix + "type",
		},
		{
			Label: c.rowLabelInfo("Safe direct keys", keybindingSafeDirectKeyPoolCopy(), "saved as logical tmux key names"),
			Value: settingsNoopValue,
		},
		{
			Label: c.rowLabelInfo("Risky/reserved keys", keybindingRiskyReservedKeyCopy(), "diagnostic-only, not saved to keymap"),
			Value: settingsNoopValue,
		},
		{
			Label: c.rowLabelInfo("Advanced delivery", keybindingAdvancedDeliveryCopy(action), "Projmux action owned"),
			Value: settingsNoopValue,
		},
		{
			Label: c.rowLabel(settingsGlyphOpen, settingsColorType, "Raw diagnostic view", "advanced diagnostics"),
			Value: settingsNoopValue,
		},
	}
	return entries, "Advanced Add Key - " + keyBindingDisplayName(action), nil
}

func (c *settingsCommand) keybindingKeyDetailEntries(actionID, chord string) ([]intpickercompat.Entry, string, error) {
	keymap, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return nil, "", err
	}
	action, ok := keyBindingActionByID(actions, actionID)
	if !ok {
		return nil, "", fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	defaultAction, _ := keyBindingActionByID(defaultKeyBindingCatalog(), actionID)
	prefix := settingsActionPrefixKeymap + action.ID + ":"
	displayKey := keybindingChordDisplay(chord)
	entries := []intpickercompat.Entry{
		c.backEntry(),
		{
			Label: c.rowLabelInfo("Action", keyBindingDisplayName(action), keybindingState(keymap, action, defaultAction)),
			Value: settingsNoopValue,
		},
		{
			Label: c.rowLabelInfo("Key", displayKey, "active key"),
			Value: settingsNoopValue,
		},
	}
	if containsString(removableKeybindingKeys(keymap, action, defaultAction), chord) {
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphRemove, settingsColorRemove, "Remove key", displayKey),
			Value: prefix + "remove:" + chord,
		})
	}
	entries = append(entries, intpickercompat.Entry{
		Label: c.rowLabel(settingsGlyphOpen, settingsColorType, "Test key", "diagnostic"),
		Value: prefix + "test:" + chord,
	})
	return entries, "Key - " + displayKey, nil
}

func keybindingAliasesSummary(action keyBindingAction) string {
	keys := keybindingVisibleChords(action)
	if len(keys) == 0 {
		return "(unbound)"
	}
	labels := make([]string, 0, len(keys))
	for _, key := range keys {
		labels = append(labels, keybindingChordDisplay(key))
	}
	return strings.Join(labels, ", ")
}

func keybindingLocalizedSearchText(locale i18n.Locale, action keyBindingAction) string {
	switch action.ID {
	case "last-pane":
		return settingsCatalogTextLocale(locale, "previously active pane / last pane")
	case "Resources:Open":
		return strings.Join([]string{
			settingsCatalogTextLocale(locale, keyBindingDisplayName(action)),
			settingsCatalogTextLocale(locale, action.Description),
		}, " ")
	default:
		return ""
	}
}

func keybindingChordDisplay(chord string) string {
	chord = strings.TrimSpace(chord)
	if chord == "" {
		return ""
	}
	readable := keybindingReadableChord(chord)
	if readable == "" || readable == chord {
		return chord
	}
	return readable + " (" + chord + ")"
}

func keybindingReadableChord(chord string) string {
	parts := strings.Split(chord, "-")
	if len(parts) < 2 {
		if len(chord) == 1 && chord[0] >= 'a' && chord[0] <= 'z' {
			return strings.ToUpper(chord)
		}
		return chord
	}
	var out []string
	for i, part := range parts {
		switch part {
		case "M":
			out = append(out, "Alt")
		case "C":
			out = append(out, "Ctrl")
		case "S":
			out = append(out, "Shift")
		default:
			if i == len(parts)-1 && len(part) == 1 && part[0] >= 'a' && part[0] <= 'z' {
				part = strings.ToUpper(part)
			}
			out = append(out, part)
		}
	}
	return strings.Join(out, "-")
}

func keybindingVisibleChords(action keyBindingAction) []string {
	if keys := keyBindingEffectivePlainChords(action); len(keys) != 0 {
		return keys
	}
	if action.PlainChords != nil {
		return nil
	}
	if action.Tier == keyBindingTierTransportDependent {
		if chord := keybindingTransportChord(action); chord != "" {
			return []string{chord}
		}
	}
	return nil
}

func keybindingPlainAliasChords(action keyBindingAction) []string {
	keys := keyBindingEffectivePlainChords(action)
	if action.Tier != keyBindingTierTransportDependent {
		return keys
	}
	transportDefault := strings.TrimSpace(keybindingTransportChord(action))
	var aliases []string
	for _, key := range keys {
		if key == "" || key == transportDefault {
			continue
		}
		aliases = append(aliases, key)
	}
	return uniqueNonEmptyStrings(aliases)
}

func keybindingTransportChord(action keyBindingAction) string {
	if chord := firstNonEmptyString(keyBindingEffectivePlainChords(action)); chord != "" {
		return chord
	}
	label := strings.TrimSpace(action.ProbeLabel)
	probeAction := strings.TrimSpace(action.ProbeAction)
	if label == "" || probeAction == "" {
		return ""
	}
	start := strings.LastIndex(probeAction, "(")
	end := strings.LastIndex(probeAction, ")")
	if start < 0 || end <= start+1 {
		return ""
	}
	return strings.TrimSpace(probeAction[start+1 : end])
}

func keybindingState(keymap keymapFile, current, def keyBindingAction) string {
	if len(keyBindingEffectivePlainChords(current)) != 0 {
		if !sameStringSlice(keyBindingEffectivePlainChords(current), keyBindingEffectivePlainChords(def)) {
			return "Custom"
		}
		return "Default"
	}
	if keymapExplicitlyUnbound(keymap, def) {
		return "Unbound"
	}
	if len(keyBindingEffectivePlainChords(def)) == 0 {
		return "Available"
	}
	return "Unbound"
}

func keymapExplicitlyUnbound(keymap keymapFile, action keyBindingAction) bool {
	override, ok := keymapOverrideForAction(keymap, action)
	return ok && override.KeysSet && len(override.Keys) == 0
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func keybindingListSummary(action keyBindingAction, state string) string {
	return strings.Join([]string{"keys " + keybindingListKeysSummary(action), "state " + state}, "  ")
}

func keybindingListKeysSummary(action keyBindingAction) string {
	keys := keybindingVisibleChords(action)
	if len(keys) == 0 {
		return "Not bound"
	}
	summary := keybindingChordDisplay(keys[0])
	if len(keys) > 1 {
		summary += fmt.Sprintf(", +%d", len(keys)-1)
	}
	return summary
}

func keybindingKeysDetail(action keyBindingAction) string {
	if len(keybindingVisibleChords(action)) == 0 {
		return "no active keys"
	}
	return "active keys"
}

func keybindingActionsSummary(keymap keymapFile, action, defaultAction keyBindingAction) string {
	if !keyBindingEditable(action) {
		return "view only"
	}
	var actions []string
	if len(keybindingVisibleChords(action)) != 0 {
		actions = append(actions, "Unbind")
	}
	state := keybindingState(keymap, action, defaultAction)
	if keybindingShowResetAction(state) {
		actions = append(actions, keybindingResetActionLabel(state, defaultAction))
	}
	if len(actions) == 0 {
		return "no action-level options"
	}
	return strings.Join(actions, ", ")
}

func keybindingDefaultKeysSummary(action keyBindingAction) string {
	keys := keyBindingEffectivePlainChords(action)
	if len(keys) == 0 {
		return "(none)"
	}
	var labels []string
	for _, key := range keys {
		labels = append(labels, keybindingChordDisplay(key))
	}
	return strings.Join(labels, ", ")
}

func keybindingResetExplanation(defaultAction keyBindingAction) string {
	keys := keybindingDefaultKeysSummary(defaultAction)
	if keys == "(none)" {
		return "action becomes available without an active key"
	}
	return "restore " + keys
}

func keybindingResetActionLabel(state string, defaultAction keyBindingAction) string {
	if len(keyBindingEffectivePlainChords(defaultAction)) == 0 {
		return "Reset"
	}
	if state == "Unbound" || state == "Available" {
		return "Use default"
	}
	return "Reset to default"
}

func keybindingShowResetAction(state string) bool {
	return state == "Custom" || state == "Unbound"
}

func keybindingSafeDirectKeyPoolCopy() string {
	return "M-letter/M-number, C-letter, C-Space, function/navigation names, and printable keys"
}

func keybindingRiskyReservedKeyCopy() string {
	return "raw escape, CSI-u, xterm modified-key bytes, tmux UserKey/UserSequence"
}

func keybindingAdvancedDeliveryCopy(action keyBindingAction) string {
	var adapters []string
	if strings.TrimSpace(action.GhosttyTrigger) != "" || strings.TrimSpace(action.GhosttyAction) != "" {
		adapters = append(adapters, "projmux setup terminal ghostty")
	}
	if strings.TrimSpace(action.WTID) != "" || strings.TrimSpace(action.WTInput) != "" {
		adapters = append(adapters, "projmux setup terminal windows-terminal")
	}
	if len(adapters) == 0 {
		return "no supported adapter snippet for this Projmux action; choose a safe direct key"
	}
	return strings.Join(adapters, " / ") + " previews and applies Projmux-owned snippets; UserSequence/UserKey stays diagnostic-only"
}

func removableKeybindingKeys(keymap keymapFile, action, defaultAction keyBindingAction) []string {
	keys := keybindingVisibleChords(action)
	if len(keys) == 0 {
		return nil
	}
	if action.Tier != keyBindingTierTransportDependent {
		return keys
	}
	aliases := keymapConfiguredAliasChords(keymap, defaultAction)
	if len(aliases) == 0 {
		return nil
	}
	return aliases
}

func removeString(items []string, target string) []string {
	var out []string
	for _, item := range items {
		if item == target {
			continue
		}
		out = append(out, item)
	}
	return out
}

func (c *settingsCommand) keymapStore() keymapStore {
	return keymapStore{
		homeDir:   c.homeDir,
		lookupEnv: c.lookupEnv,
	}
}

func (c *settingsCommand) saveKeymapKeysAndApply(actionID string, keys []string, stdout io.Writer) error {
	path, err := saveKeymapKeys(c.keymapStore(), actionID, keys)
	return c.finishKeymapApply(path, err, stdout)
}

func (c *settingsCommand) resetKeymapKeysAndApply(actionID string, stdout io.Writer) error {
	path, err := resetKeymapKeys(c.keymapStore(), actionID)
	return c.finishKeymapApply(path, err, stdout)
}

func (c *settingsCommand) removeKeymapKeyAndApply(actionID, chord string, stdout io.Writer) error {
	chord, err := normalizeKeymapTypedChord(chord)
	if err != nil {
		return err
	}
	current, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return err
	}
	action, ok := keyBindingActionByID(actions, actionID)
	if !ok {
		return fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	defaultAction, _ := keyBindingActionByID(defaultKeyBindingCatalog(), action.ID)
	if action.Tier == keyBindingTierTransportDependent {
		keys := removeString(keymapConfiguredAliasChords(current, defaultAction), chord)
		if len(keys) == 0 {
			return c.resetKeymapKeysAndApply(action.ID, stdout)
		}
		return c.saveKeymapKeysAndApply(action.ID, keys, stdout)
	}
	keys := removeString(keyBindingEffectivePlainChords(action), chord)
	return c.saveKeymapKeysAndApply(action.ID, keys, stdout)
}

func (c *settingsCommand) addKeymapAliasAndApply(actionID, chord string, stdout io.Writer) error {
	chord, err := normalizeKeymapTypedChord(chord)
	if err != nil {
		return err
	}
	current, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return err
	}
	action, ok := keyBindingActionByID(actions, actionID)
	if !ok {
		return fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	if action.Tier == keyBindingTierTransportDependent {
		defaultAction, ok := keyBindingActionByID(defaultKeyBindingCatalog(), actionID)
		if !ok {
			return fmt.Errorf("unknown keybinding action: %s", actionID)
		}
		if chord == strings.TrimSpace(defaultAction.PlainChord) {
			return fmt.Errorf("key %q is the transport-dependent default for %s; choose a separate custom key", chord, action.ID)
		}
		keys := append([]string{}, keymapConfiguredAliasChords(current, defaultAction)...)
		keys = append(keys, chord)
		return c.saveKeymapKeysAndApply(action.ID, uniqueNonEmptyStrings(keys), stdout)
	}
	keys := append([]string{}, keyBindingEffectivePlainChords(action)...)
	keys = append(keys, chord)
	return c.saveKeymapKeysAndApply(action.ID, uniqueNonEmptyStrings(keys), stdout)
}

func keymapConfiguredAliasChords(keymap keymapFile, action keyBindingAction) []string {
	override, ok := keymapOverrideForAction(keymap, action)
	if !ok {
		return nil
	}
	if override.KeysSet {
		return keybindingPlainAliasChords(keyBindingAction{
			ID:          action.ID,
			Tier:        action.Tier,
			PlainChord:  action.PlainChord,
			PlainChords: append([]string{action.PlainChord}, override.Keys...),
		})
	}
	if override.Plain != nil {
		return keybindingPlainAliasChords(keyBindingAction{
			ID:          action.ID,
			Tier:        action.Tier,
			PlainChord:  action.PlainChord,
			PlainChords: []string{action.PlainChord, *override.Plain},
		})
	}
	return nil
}

func keymapOverrideForAction(keymap keymapFile, action keyBindingAction) (keymapOverride, bool) {
	for _, id := range keyBindingActionAliases(action) {
		if override, ok := keymap.Bindings[id]; ok {
			return override, true
		}
	}
	return keymapOverride{}, false
}

func (c *settingsCommand) finishKeymapApply(path string, err error, stdout io.Writer) error {
	report := keymapApplyReport{
		Saved:    keymapApplyStage{Status: keymapApplyOK},
		Prepared: keymapApplyStage{Status: keymapApplySkipped, Detail: "waiting for saved keybinding"},
		Live:     keymapApplyStage{Status: keymapApplySkipped, Detail: "waiting for prepared config"},
	}
	if strings.TrimSpace(path) != "" {
		report.Saved.Detail = "keymap.toml: " + path
	}
	if err != nil {
		report.Saved = keymapApplyStage{Status: keymapApplyFailed, Detail: keymapApplyDiagnostic("keymap.toml", err)}
		report.Prepared = keymapApplyStage{Status: keymapApplySkipped, Detail: "keybinding was not saved"}
		report.Live = keymapApplyStage{Status: keymapApplySkipped, Detail: "keybinding was not saved"}
		_ = writeKeymapApplyReport(stdout, report)
		return fmt.Errorf("save keybinding: %w", err)
	}
	prepared, live, applyErr := c.regenerateAndReloadTmuxConfig()
	report.Prepared = prepared
	report.Live = live
	if applyErr != nil {
		if prepared.Status == keymapApplyFailed {
			_ = writeKeymapApplyReport(stdout, report)
			return fmt.Errorf("update keybinding runtime config: %w", applyErr)
		}
		_ = writeKeymapApplyReport(stdout, report)
		return fmt.Errorf("reload active tmux keybindings: %w", applyErr)
	}
	return writeKeymapApplyReport(stdout, report)
}

// regenerateAndReloadTmuxConfig is the shared post-save apply core used by both
// the keybinding and theme Settings paths: it regenerates the generated tmux app
// config and, when running inside tmux with a configured command runner,
// `tmux source-file`-reloads it. It returns the Prepared and Live stages plus a
// fatal error when either stage fails. The no-server / not-inside-tmux cases are
// graceful (Live becomes skipped, err is nil) so the durable save still
// succeeds and callers can surface the "run `projmux tmux apply`" follow-up.
func (c *settingsCommand) regenerateAndReloadTmuxConfig() (prepared keymapApplyStage, live keymapApplyStage, err error) {
	configPath, genErr := c.writeTmuxAppConfig()
	if genErr != nil {
		prepared = keymapApplyStage{Status: keymapApplyFailed, Detail: keymapApplyDiagnostic("generated tmux config", genErr)}
		live = keymapApplyStage{Status: keymapApplySkipped, Detail: "generated tmux config failed"}
		return prepared, live, genErr
	}
	prepared = keymapApplyStage{Status: keymapApplyOK, Detail: "generated tmux config: " + configPath}
	if c.lookupEnv == nil || strings.TrimSpace(c.lookupEnv("TMUX")) == "" {
		live = keymapApplyStage{Status: keymapApplySkipped, Detail: "Settings is not running inside tmux"}
		return prepared, live, nil
	}
	if c.runCommand == nil {
		live = keymapApplyStage{Status: keymapApplySkipped, Detail: "tmux command runner is not configured"}
		return prepared, live, nil
	}
	if reloadErr := c.runCommand("tmux", "source-file", configPath); reloadErr != nil {
		live = keymapApplyStage{Status: keymapApplyFailed, Detail: keymapApplyDiagnostic("live tmux reload", reloadErr)}
		return prepared, live, reloadErr
	}
	live = keymapApplyStage{Status: keymapApplyOK}
	return prepared, live, nil
}

type keymapApplyStatus string

const (
	keymapApplyOK      keymapApplyStatus = "ok"
	keymapApplySkipped keymapApplyStatus = "skipped"
	keymapApplyFailed  keymapApplyStatus = "failed"
)

type keymapApplyStage struct {
	Status keymapApplyStatus
	Detail string
}

type keymapApplyReport struct {
	Saved    keymapApplyStage
	Prepared keymapApplyStage
	Live     keymapApplyStage
}

func keymapApplyDiagnostic(stage string, err error) string {
	if err == nil {
		return stage
	}
	return stage + ": " + err.Error()
}

func writeKeymapApplyReport(w io.Writer, report keymapApplyReport) error {
	if w == nil {
		return nil
	}
	if report.Saved.Status == keymapApplyOK && report.Prepared.Status == keymapApplyOK && report.Live.Status == keymapApplyOK {
		if _, err := fmt.Fprintln(w, "keybinding saved and applied"); err != nil {
			return err
		}
		for _, line := range []string{
			keymapApplyLine("Saved", report.Saved, false),
			keymapApplyLine("Prepared", report.Prepared, false),
			keymapApplyLine("Running session", report.Live, false),
		} {
			if _, err := fmt.Fprintln(w, line); err != nil {
				return err
			}
		}
		return nil
	}

	if _, err := fmt.Fprintln(w, "keybinding apply status"); err != nil {
		return err
	}
	for _, line := range []string{
		keymapApplyLine("Saved", report.Saved, true),
		keymapApplyLine("Prepared", report.Prepared, true),
		keymapApplyLine("Running session", report.Live, true),
	} {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	switch {
	case report.Saved.Status == keymapApplyFailed:
		_, err := fmt.Fprintln(w, "Recovery: fix the keymap.toml problem, then try the Settings change again.")
		return err
	case report.Prepared.Status == keymapApplyFailed:
		_, err := fmt.Fprintln(w, "Recovery: resolve the generated tmux config error, then run `projmux tmux apply`.")
		return err
	case report.Live.Status == keymapApplyFailed:
		_, err := fmt.Fprintln(w, "Recovery: fix the live tmux reload issue, then run `projmux tmux apply`.")
		return err
	case report.Live.Status == keymapApplySkipped:
		_, err := fmt.Fprintln(w, "Next: run `projmux tmux apply` to sync a running projmux tmux server.")
		return err
	default:
		return nil
	}
}

func keymapApplyLine(label string, stage keymapApplyStage, includeDetail bool) string {
	line := "  " + label + ": " + string(stage.Status)
	if includeDetail && strings.TrimSpace(stage.Detail) != "" {
		line += " (" + strings.TrimSpace(stage.Detail) + ")"
	}
	if !includeDetail && label == "Running session" && stage.Status == keymapApplyOK {
		line += " (updated)"
	}
	return line
}

func (c *settingsCommand) probeLabKeybindingContext(ctx context.Context, key probeKey, timeout time.Duration) (probeResult, error) {
	if c.probeKeybinding != nil {
		return c.probeKeybinding(key, timeout)
	}
	cmd := &setupCommand{openTTY: openControllingTTY}
	return cmd.probeControllingTTYKeyContext(ctx, key, timeout)
}

func (c *settingsCommand) writeTmuxAppConfig() (string, error) {
	home := ""
	if c.homeDir != nil {
		got, err := c.homeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		home = got
	}
	env := c.lookupEnv
	if env == nil {
		env = os.Getenv
	}
	paths, err := config.Homes{
		HomeDir:    home,
		ConfigHome: env("XDG_CONFIG_HOME"),
		StateHome:  env("XDG_STATE_HOME"),
	}.Paths()
	if err != nil {
		return "", err
	}
	tmux := newTmuxCommand()
	tmux.homeDir = c.homeDir
	tmux.lookupEnv = c.lookupEnv
	return tmux.writeAppConfig("", filepath.Join(paths.ConfigDir, "tmux.conf"))
}
