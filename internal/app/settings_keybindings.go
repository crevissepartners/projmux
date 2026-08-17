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
				err = c.runKeybindingRecorder(actionID, stdout, stderr)
			}
			if err != nil {
				return err
			}
		case "capture":
			if err := c.runKeybindingCapture(actionID, stdout, stderr); err != nil {
				return err
			}
		case "type":
			if err := c.runKeybindingTyped(actionID, false, stdout, stderr); err != nil {
				return err
			}
		case "sequence-add":
			if err := c.runKeybindingSequenceEditor(actionID, "", stdout, stderr); err != nil {
				return err
			}
		case "sequence-type":
			if err := c.runKeybindingSequenceTyped(actionID, "", stdout, stderr); err != nil {
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
				return c.resetKeymapBindingAndApply(actionID, out)
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
			if sequence, ok := strings.CutPrefix(op, "sequence:"); ok {
				if err := c.runKeybindingSequenceDetail(actionID, sequence, stdout, stderr); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("unknown keybinding operation: %s", op)
		}
	}
}

func (c *settingsCommand) runKeybindingRecorder(actionID string, stdout, stderr io.Writer) error {
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
		Footer:        projmuxFooter("Enter confirms · Esc cancels · Enter key name manually is available from Action detail."),
		DisableSearch: true,
		Bindings:      c.settingsCloseBindings(),
		Recorder:      recorder,
	})
	if err != nil {
		return err
	}
	if result.Key == "esc" {
		c.setSettingsFeedback("Keybinding cancelled", "recorder closed without adding a key")
		return nil
	}
	if result.Key != "enter" {
		return errSettingsClosed
	}
	chord := strings.TrimSpace(result.Value)
	if chord == "" {
		c.setSettingsFeedback("Keybinding cancelled", "no key was recorded")
		return nil
	}
	return c.runKeybindingWrite(stdout, stderr, func(out io.Writer) error {
		return c.addKeymapAliasAndApply(action.ID, chord, out)
	})
}

// runKeybindingWrite is the single feedback boundary for every keybinding
// write. Recorder, typed entry, and physical capture all pass through it, so a
// rejected chord shows the reason as an in-popup result instead of tearing the
// Settings popup down with a returned error.
func (c *settingsCommand) runKeybindingWrite(stdout, stderr io.Writer, write func(io.Writer) error) error {
	var report strings.Builder
	if stdout == nil {
		stdout = io.Discard
	}
	err := write(io.MultiWriter(stdout, &report))
	detail := keybindingApplyFeedbackDetail(report.String())
	if err != nil {
		if detail == "" {
			detail = err.Error()
		} else {
			detail += " · Error: " + err.Error()
		}
		c.setSettingsFeedback("Keybinding failed", detail)
		return nil
	}
	if detail != "" {
		c.setSettingsFeedback("Keybinding complete", detail)
	}
	return nil
}

// keybindingApplyFeedbackDetail keeps the full three-stage result observable in
// the next Settings frame. The command output remains line-oriented for CLI
// callers, while the popup row uses a compact separator so it cannot inject
// extra rows into the native picker.
func keybindingApplyFeedbackDetail(output string) string {
	var parts []string
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Saved:") ||
			strings.HasPrefix(line, "Prepared:") ||
			strings.HasPrefix(line, "Running session:") ||
			strings.HasPrefix(line, "Recovery:") ||
			strings.HasPrefix(line, "Next:") {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, " · ")
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
			return "", fmt.Errorf("plain %s is a recorder control; use Enter key name manually", baseName)
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
	case op == "add", op == "capture", op == "type", op == "reset", op == "unbind",
		op == "sequence-add", op == "sequence-type", op == "sequence-capture", op == "sequence-save":
		return op, true
	case strings.HasPrefix(op, "key:"):
		return op, true
	case strings.HasPrefix(op, "remove:"):
		return op, true
	case strings.HasPrefix(op, "test:"):
		return op, true
	case strings.HasPrefix(op, "sequence:"):
		return op, true
	case strings.HasPrefix(op, "sequence-replace:"):
		return op, true
	case strings.HasPrefix(op, "sequence-type-replace:"):
		return op, true
	case strings.HasPrefix(op, "sequence-remove:"):
		return op, true
	case strings.HasPrefix(op, "sequence-test:"):
		return op, true
	case strings.HasPrefix(op, "sequence-stroke-remove:"):
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
			return c.runKeybindingCapture(actionID, stdout, stderr)
		case "type":
			return c.runKeybindingTyped(actionID, false, stdout, stderr)
		default:
			return fmt.Errorf("unknown keybinding add operation: %s", op)
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
			if err := c.runKeybindingDeliveryTest(actionID, strings.TrimPrefix(op, "test:"), stdout, stderr); err != nil {
				return err
			}
			continue
		default:
			return fmt.Errorf("unknown keybinding key operation: %s", op)
		}
	}
}

// keybindingDeliveryTestMode is the reader a delivery test can legitimately own
// in this context. Exactly one reader is ever live: either the picker owns the
// terminal (recorder) or the controlling-TTY probe does. They are never started
// together, which is what keeps a tmux popup from fighting the probe for input
// and then hanging until the probe timeout.
type keybindingDeliveryTestMode string

const (
	keybindingDeliveryTestProbe       keybindingDeliveryTestMode = "controlling-tty probe"
	keybindingDeliveryTestRecorder    keybindingDeliveryTestMode = "picker recorder"
	keybindingDeliveryTestUnsupported keybindingDeliveryTestMode = "unavailable"
)

const keybindingDeliveryTestNextStep = "run `projmux setup` in a plain terminal to probe delivery, then `projmux setup terminal <terminal> --apply`"

// keybindingDeliveryTestReader picks the one reader this context can own.
func (c *settingsCommand) keybindingDeliveryTestReader() keybindingDeliveryTestMode {
	if c.keybindingPhysicalCaptureAvailable() {
		return keybindingDeliveryTestProbe
	}
	env := c.lookupEnv
	if env == nil {
		env = os.Getenv
	}
	if strings.TrimSpace(env("TMUX")) != "" {
		// Inside a tmux popup the popup client owns the tty, so the probe
		// cannot read. The picker's own input loop can, and it is already the
		// single reader for this frame.
		return keybindingDeliveryTestRecorder
	}
	return keybindingDeliveryTestUnsupported
}

// keybindingDeliveryTestUnavailable reports why a delivery test cannot produce
// a trustworthy observation here, plus the canonical next step. A true result
// is rendered as a disabled row rather than as an action that would no-op.
func (c *settingsCommand) keybindingDeliveryTestUnavailable(chord string) (reason string, next string, unavailable bool) {
	switch c.keybindingDeliveryTestReader() {
	case keybindingDeliveryTestProbe:
		return "", "", false
	case keybindingDeliveryTestRecorder:
		switch strings.TrimSpace(chord) {
		case "Enter", "Escape":
			return "the recorder consumes plain " + strings.TrimSpace(chord) + " as its own control, so it cannot observe this key",
				keybindingDeliveryTestNextStep, true
		}
		return "", "", false
	default:
		return "no interactive key reader can be owned in this context", keybindingDeliveryTestNextStep, true
	}
}

// keybindingDeliveryObservation is one observable Test delivery result. It is
// a report, never a stored value: nothing here is written to keymap.toml, so a
// raw observation can never become a saved logical key.
type keybindingDeliveryObservation struct {
	Source          keybindingDeliveryTestMode
	LogicalKey      string
	RawObservation  string
	TmuxReceivedKey string
	Status          keybindingDeliveryDiagnosticStatus
	Summary         string
}

// classifyKeybindingDeliveryObservation maps one observation onto the four
// delivery outcomes. Every input combination lands on exactly one of them, so a
// delivery test cannot end without a result.
func classifyKeybindingDeliveryObservation(expected, observed, raw string) keybindingDeliveryObservation {
	obs := keybindingDeliveryObservation{
		LogicalKey:      keybindingChordDisplay(expected),
		RawObservation:  raw,
		TmuxReceivedKey: observed,
	}
	if strings.TrimSpace(obs.RawObservation) == "" {
		obs.RawObservation = "(none)"
	}
	switch {
	case strings.TrimSpace(observed) == "":
		obs.Status = keybindingDeliveryMissing
		obs.TmuxReceivedKey = "(none)"
		obs.Summary = "key did not arrive; the terminal or OS consumed it before Projmux"
	case observed == expected:
		obs.Status = keybindingDeliveryDelivered
		obs.Summary = "the pressed key reached Projmux as " + expected
	case keybindingChordsAreAmbiguousPair(expected, observed):
		obs.Status = keybindingDeliveryAmbiguous
		obs.TmuxReceivedKey = expected + " / " + observed
		obs.Summary = "ambiguous key; " + expected + " and " + observed + " share one byte sequence"
	default:
		obs.Status = keybindingDeliveryAdapterNeeded
		obs.Summary = "adapter-needed; the terminal delivered " + observed + " instead of " + expected
	}
	return obs
}

// keybindingChordsAreAmbiguousPair reports the chord pairs a terminal cannot
// tell apart on the wire.
func keybindingChordsAreAmbiguousPair(a, b string) bool {
	pairs := [][2]string{{"Enter", "C-m"}, {"Tab", "C-i"}, {"Escape", "C-["}, {"BSpace", "C-h"}}
	for _, pair := range pairs {
		if (a == pair[0] && b == pair[1]) || (a == pair[1] && b == pair[0]) {
			return true
		}
	}
	return false
}

func renderKeybindingDeliveryObservation(obs keybindingDeliveryObservation) []string {
	return []string{
		"test delivery result:",
		"  reader: " + string(obs.Source),
		"  logical key: " + obs.LogicalKey,
		"  raw observation: " + obs.RawObservation,
		"  tmux received key: " + obs.TmuxReceivedKey,
		"  delivery status: " + string(obs.Status) + " - " + obs.Summary,
	}
}

// runKeybindingDeliveryTest is the Test delivery Action. It never writes
// keymap.toml and never applies tmux config; its only product is the observed
// result, which is reported through the shared Settings feedback boundary so it
// is visible inside the popup.
func (c *settingsCommand) runKeybindingDeliveryTest(actionID, chord string, stdout, stderr io.Writer) error {
	if reason, next, unavailable := c.keybindingDeliveryTestUnavailable(chord); unavailable {
		c.setSettingsFeedback("Test delivery unavailable", reason+"; next: "+next)
		return nil
	}
	_, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return err
	}
	action, ok := keyBindingActionByID(actions, actionID)
	if !ok {
		return fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	mode := c.keybindingDeliveryTestReader()
	var obs keybindingDeliveryObservation
	var cancelled bool
	switch mode {
	case keybindingDeliveryTestRecorder:
		obs, cancelled, err = c.observeKeybindingDeliveryWithRecorder(action, chord)
	default:
		obs, err = c.observeKeybindingDeliveryWithProbe(action, chord)
	}
	if err != nil {
		c.setSettingsFeedback("Test delivery failed", err.Error())
		return nil
	}
	if cancelled {
		c.setSettingsFeedback("Test delivery cancelled", "no key was pressed; nothing changed")
		return nil
	}
	obs.Source = mode
	return c.runObservedSettingsMutation("Test delivery", stdout, stderr, func(out, _ io.Writer) error {
		for _, line := range renderKeybindingDeliveryObservation(obs) {
			fmt.Fprintln(out, line)
		}
		return nil
	})
}

// observeKeybindingDeliveryWithRecorder reads one chord through the picker's
// own input loop. It reuses the Add recorder's Normalize so the test and the
// Add path agree on what a key is called, and it deliberately passes no
// Validate: a delivery test must not run keymap conflict policy against a chord
// that is already bound to this very action.
func (c *settingsCommand) observeKeybindingDeliveryWithRecorder(action keyBindingAction, chord string) (keybindingDeliveryObservation, bool, error) {
	var raw string
	recorder := &intpicker.RecorderOptions{
		Normalize: func(key intpicker.RecorderKey) (string, error) {
			raw = describeRecorderKeyObservation(key)
			return normalizeKeybindingRecorderKey(key)
		},
	}
	result, err := c.runPicker(intpickercompat.Options{
		UI:            "settings-keybinding-delivery-test",
		Title:         "Test delivery - " + keybindingChordDisplay(chord),
		Header:        "Press " + keybindingChordDisplay(chord) + " once for " + keyBindingDisplayName(action),
		Footer:        projmuxFooter("Press the key · Enter reports the result · Esc cancels."),
		DisableSearch: true,
		Bindings:      c.settingsCloseBindings(),
		Recorder:      recorder,
	})
	if err != nil {
		return keybindingDeliveryObservation{}, false, err
	}
	if result.Key == "esc" {
		return keybindingDeliveryObservation{}, true, nil
	}
	if result.Key != "enter" {
		return keybindingDeliveryObservation{}, false, errSettingsClosed
	}
	return classifyKeybindingDeliveryObservation(chord, strings.TrimSpace(result.Value), raw), false, nil
}

func describeRecorderKeyObservation(key intpicker.RecorderKey) string {
	switch {
	case strings.TrimSpace(key.Name) != "":
		return "picker key name " + strings.TrimSpace(key.Name)
	case key.Text != "":
		return "printable text " + visibleEscape(key.Text)
	default:
		return "(no stable key name)"
	}
}

// observeKeybindingDeliveryWithProbe reads one chord off the controlling tty.
// The read is bounded by defaultProbeTimeout, and a timeout is a reported
// key-did-not-arrive result rather than a hang.
func (c *settingsCommand) observeKeybindingDeliveryWithProbe(action keyBindingAction, chord string) (keybindingDeliveryObservation, error) {
	key := probeKey{
		ActionID:   action.ID,
		Label:      keybindingChordDisplay(chord),
		Action:     "delivery test for " + keyBindingDisplayName(action),
		PlainChord: chord,
	}
	if defaultAction, ok := keyBindingActionByID(defaultKeyBindingCatalog(), action.ID); ok &&
		strings.TrimSpace(defaultAction.PlainChord) == strings.TrimSpace(chord) {
		key.Plain = defaultAction.ProbePlain
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultProbeTimeout)
	defer cancel()
	res, err := c.probeLabKeybindingContext(ctx, key, defaultProbeTimeout)
	if err != nil {
		return keybindingDeliveryObservation{}, err
	}
	raw := visibleEscape(string(res.Sequence))
	observed := ""
	switch {
	case len(res.Sequence) == 0:
		raw = ""
	case isAmbiguousEnterSequence(res.Sequence):
		observed = "C-m"
		if chord == "C-m" {
			observed = "Enter"
		}
	case res.Status == probeStatusPlain:
		observed = chord
	default:
		if suggested, ok := suggestedPlainChordForSequence(res.Sequence); ok {
			observed = suggested
		} else {
			observed = "an unnamed raw sequence"
		}
	}
	return classifyKeybindingDeliveryObservation(chord, observed, raw), nil
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

// keybindingPhysicalCaptureUnavailableReason names why the "Press a key" row
// cannot read here, so the disabled row states a cause instead of only an
// absence.
func (c *settingsCommand) keybindingPhysicalCaptureUnavailableReason() string {
	env := c.lookupEnv
	if env == nil {
		env = os.Getenv
	}
	if strings.TrimSpace(env("TMUX")) != "" {
		return "the tmux popup client owns the terminal input"
	}
	return "no physical key reader is available in this context"
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

func (c *settingsCommand) runKeybindingCapture(actionID string, stdout, stderr io.Writer) error {
	if !c.keybindingPhysicalCaptureAvailable() {
		fmt.Fprintln(stdout, "physical key capture is unavailable in this context; enter a key name instead")
		return c.runKeybindingTyped(actionID, false, stdout, stderr)
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
		return true, c.addCapturedKeybindingChord(action.ID, chord, stdout, stderr)
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
			c.setSettingsFeedback("Keybinding failed", "captured key has no stable tmux key name; use Enter key name manually")
			return nil
		}
		return c.addCapturedKeybindingChord(action.ID, chord, stdout, stderr)
	case probeStatusUnknown:
		chord, ok := suggestedPlainChordForSequence(res.Sequence)
		if !ok {
			fmt.Fprintf(stdout, "captured raw sequence %s is not safe to persist; type a custom key name instead\n", visibleEscape(string(res.Sequence)))
			c.setSettingsFeedback("Keybinding failed", "captured raw sequence is never stored as a key; use Enter key name manually")
			return nil
		}
		return c.addCapturedKeybindingChord(action.ID, chord, stdout, stderr)
	case probeStatusTimeout:
		fmt.Fprintf(stdout, "no key was captured; nothing changed\n")
		c.setSettingsFeedback("Keybinding cancelled", "no key was captured before the read timed out")
		return nil
	default:
		return fmt.Errorf("unknown keybinding capture status: %s", res.Status)
	}
}

// addCapturedKeybindingChord runs the captured chord through the same
// validation and the same write boundary the recorder and typed entry use, so
// the three Add paths cannot diverge on what they accept or on how a rejection
// becomes visible.
func (c *settingsCommand) addCapturedKeybindingChord(actionID, chord string, stdout, stderr io.Writer) error {
	if err := c.validateKeymapAliasForAction(actionID, chord); err != nil {
		c.setSettingsFeedback("Keybinding failed", err.Error())
		return nil
	}
	return c.runKeybindingWrite(stdout, stderr, func(out io.Writer) error {
		return c.addKeymapAliasAndApply(actionID, chord, out)
	})
}

// runKeybindingTyped is the typed half of Add key. It shares the recorder's
// normalization (normalizeKeymapTypedChord) and the recorder's pre-write
// validation (validateKeymapAliasForAction), so a chord rejected in the
// recorder is rejected identically here, with the same reason, and neither path
// writes keymap.toml before validating.
func (c *settingsCommand) runKeybindingTyped(actionID string, replace bool, stdout, stderr io.Writer) error {
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
		c.setSettingsFeedback("Keybinding cancelled", "typed entry closed without adding a key")
		return nil
	}
	chord, normalizeErr := normalizeKeymapTypedChord(result.Query)
	if normalizeErr != nil {
		c.setSettingsFeedback("Keybinding failed", normalizeErr.Error())
		return nil
	}
	if chord == "" {
		c.setSettingsFeedback("Keybinding cancelled", "no key name was entered")
		return nil
	}
	if !replace {
		if validateErr := c.validateKeymapAliasForAction(action.ID, chord); validateErr != nil {
			c.setSettingsFeedback("Keybinding failed", validateErr.Error())
			return nil
		}
	}
	return c.runKeybindingWrite(stdout, stderr, func(out io.Writer) error {
		if replace {
			return c.saveKeymapKeysAndApply(action.ID, []string{chord}, out)
		}
		return c.addKeymapAliasAndApply(action.ID, chord, out)
	})
}

// runKeybindingSequenceEditor captures exactly one logical stroke per recorder
// frame and then returns to this editor. Save is exposed once the shared v2
// grammar can accept the accumulated sequence; four strokes is the hard stop.
// No key is reserved as a sequence-finish key, so Enter remains authorable as
// a logical stroke.
func (c *settingsCommand) runKeybindingSequenceEditor(actionID, replace string, stdout, stderr io.Writer) error {
	var strokes []string
	for {
		entries, title, err := c.keybindingSequenceEditorEntries(actionID, replace, strokes)
		if err != nil {
			return err
		}
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-keybinding-sequence-editor",
			Entries:    entries,
			Title:      title,
			Prompt:     "Settings > Keybindings > Action > Sequence > ",
			Footer:     projmuxFooter("Capture one stroke at a time · Save is available at 2 strokes · maximum 4."),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		})
		if err != nil {
			return err
		}
		action := strings.TrimSpace(result.Value)
		if result.Key != "enter" || action == "" {
			c.setSettingsFeedback("Sequence cancelled", "editor closed without changing keymap.toml or tmux")
			return nil
		}
		if action == settingsBackValue {
			c.setSettingsFeedback("Sequence cancelled", "editor closed without changing keymap.toml or tmux")
			return nil
		}
		if action == settingsNoopValue {
			continue
		}
		op, ok := parseKeymapDetailAction(action, actionID)
		if !ok {
			return fmt.Errorf("unknown sequence editor action: %s", action)
		}
		switch {
		case op == "sequence-capture":
			stroke, cancelled, captureErr := c.captureKeybindingSequenceStroke(actionID, strokes)
			if captureErr != nil {
				return captureErr
			}
			if !cancelled {
				strokes = append(strokes, stroke)
			}
		case op == "sequence-save":
			sequence, normalizeErr := normalizeKeymapSequence(strings.Join(strokes, " "))
			if normalizeErr != nil {
				c.setSettingsFeedback("Sequence failed", normalizeErr.Error())
				continue
			}
			if validateErr := c.validateKeymapSequenceForAction(actionID, sequence, replace); validateErr != nil {
				c.setSettingsFeedback("Sequence failed", validateErr.Error())
				continue
			}
			return c.runKeybindingWrite(stdout, stderr, func(out io.Writer) error {
				if replace != "" {
					return c.replaceKeymapSequenceAndApply(actionID, replace, sequence, out)
				}
				return c.addKeymapSequenceAndApply(actionID, sequence, out)
			})
		case strings.HasPrefix(op, "sequence-stroke-remove:"):
			var index int
			if _, scanErr := fmt.Sscanf(strings.TrimPrefix(op, "sequence-stroke-remove:"), "%d", &index); scanErr != nil || index < 0 || index >= len(strokes) {
				return fmt.Errorf("invalid sequence stroke index in %q", op)
			}
			strokes = append(strokes[:index], strokes[index+1:]...)
		default:
			return fmt.Errorf("unknown sequence editor operation: %s", op)
		}
	}
}

func (c *settingsCommand) captureKeybindingSequenceStroke(actionID string, strokes []string) (string, bool, error) {
	_, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return "", false, err
	}
	action, ok := keyBindingActionByID(actions, actionID)
	if !ok {
		return "", false, fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	recorder := &intpicker.RecorderOptions{
		AutoConfirm:  true,
		CaptureEnter: true,
		Normalize:    normalizeKeybindingSequenceRecorderKey,
		Validate: func(stroke string) error {
			_, err := normalizeKeybindingSequenceStroke(stroke, len(strokes))
			return err
		},
	}
	result, err := c.runPicker(intpickercompat.Options{
		UI:            "settings-keybinding-sequence-stroke",
		Title:         "Record Sequence Stroke - " + keyBindingDisplayName(action),
		Header:        fmt.Sprintf("Accumulated: %s", keybindingSequenceDraftSummary(strokes)),
		Footer:        projmuxFooter("Press one stroke · Enter is a stroke · Esc returns to the editor."),
		DisableSearch: true,
		Bindings:      c.settingsCloseBindings(),
		Recorder:      recorder,
	})
	if err != nil {
		return "", false, err
	}
	if result.Key == "esc" {
		return "", true, nil
	}
	if result.Key != "enter" {
		return "", false, errSettingsClosed
	}
	stroke, err := normalizeKeybindingSequenceStroke(strings.TrimSpace(result.Value), len(strokes))
	if err != nil {
		c.setSettingsFeedback("Sequence stroke failed", err.Error())
		return "", true, nil
	}
	return stroke, false, nil
}

func normalizeKeybindingSequenceRecorderKey(key intpicker.RecorderKey) (string, error) {
	if strings.TrimSpace(key.Name) == "enter" && key.Text == "" {
		return normalizeKeymapTypedChord("Enter")
	}
	return normalizeKeybindingRecorderKey(key)
}

// normalizeKeybindingSequenceStroke delegates the actual stroke policy to the
// Phase 0 sequence normalizer by placing the candidate in a valid two-stroke
// sequence. Settings therefore cannot drift from the installed grammar.
func normalizeKeybindingSequenceStroke(stroke string, index int) (string, error) {
	stroke = strings.TrimSpace(stroke)
	if stroke == "" {
		return "", fmt.Errorf("stroke is empty")
	}
	value := "C-x " + stroke
	candidateIndex := 1
	if index == 0 {
		value = stroke + " Enter"
		candidateIndex = 0
	}
	normalized, err := normalizeKeymapSequence(value)
	if err != nil {
		return "", err
	}
	return strings.Split(normalized, " ")[candidateIndex], nil
}

func (c *settingsCommand) runKeybindingSequenceTyped(actionID, replace string, stdout, stderr io.Writer) error {
	_, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return err
	}
	action, ok := keyBindingActionByID(actions, actionID)
	if !ok {
		return fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	result, err := c.runPicker(intpickercompat.Options{
		UI:          "settings-keybinding-sequence-type",
		Entries:     []intpickercompat.Entry{c.backEntry(), {Label: c.rowLabelInfo("Action", keyBindingDisplayName(action), "2 to 4 logical strokes"), Value: settingsNoopValue}},
		Title:       "Enter Sequence - " + keyBindingDisplayName(action),
		Prompt:      "Enter sequence > ",
		Footer:      projmuxFooter("Enter a sequence such as C-k C-p (2 to 4 strokes)."),
		ExpectKeys:  []string{"enter"},
		Bindings:    c.settingsCloseBindings(),
		AcceptQuery: true,
	})
	if err != nil {
		return err
	}
	if result.Key != "enter" {
		c.setSettingsFeedback("Sequence cancelled", "typed entry closed without changing keymap.toml or tmux")
		return nil
	}
	sequence, normalizeErr := normalizeKeymapSequence(result.Query)
	if normalizeErr != nil {
		c.setSettingsFeedback("Sequence failed", normalizeErr.Error())
		return nil
	}
	if validateErr := c.validateKeymapSequenceForAction(action.ID, sequence, replace); validateErr != nil {
		c.setSettingsFeedback("Sequence failed", validateErr.Error())
		return nil
	}
	return c.runKeybindingWrite(stdout, stderr, func(out io.Writer) error {
		if replace != "" {
			return c.replaceKeymapSequenceAndApply(action.ID, replace, sequence, out)
		}
		return c.addKeymapSequenceAndApply(action.ID, sequence, out)
	})
}

func (c *settingsCommand) runKeybindingSequenceDetail(actionID, sequence string, stdout, stderr io.Writer) error {
	for {
		entries, title, err := c.keybindingSequenceDetailEntries(actionID, sequence)
		if err != nil {
			return err
		}
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-keybinding-sequence-detail",
			Entries:    entries,
			Title:      title,
			Prompt:     "Settings > Keybindings > Action > Sequence > ",
			Footer:     projmuxFooter("Manage this sequence trigger and inspect its delivery contract."),
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
			return fmt.Errorf("unknown sequence detail action: %s", action)
		}
		switch {
		case strings.HasPrefix(op, "sequence-replace:"):
			return c.runKeybindingSequenceEditor(actionID, strings.TrimPrefix(op, "sequence-replace:"), stdout, stderr)
		case strings.HasPrefix(op, "sequence-type-replace:"):
			return c.runKeybindingSequenceTyped(actionID, strings.TrimPrefix(op, "sequence-type-replace:"), stdout, stderr)
		case strings.HasPrefix(op, "sequence-remove:"):
			remove := strings.TrimPrefix(op, "sequence-remove:")
			return c.runKeybindingWrite(stdout, stderr, func(out io.Writer) error {
				return c.removeKeymapSequenceAndApply(actionID, remove, out)
			})
		case strings.HasPrefix(op, "sequence-test:"):
			if err := c.runKeybindingSequenceDeliveryTest(actionID, strings.TrimPrefix(op, "sequence-test:")); err != nil {
				return err
			}
			continue
		default:
			return fmt.Errorf("unknown sequence detail operation: %s", op)
		}
	}
}

// runKeybindingSequenceDeliveryTest observes the configured logical strokes in
// order without touching keymap.toml, generated config, or the live tmux
// server. Each observation uses the same one-stroke recorder as authoring. A
// partial Escape or an unexpected stroke mirrors the Phase 0 runtime contract:
// the partial sequence is cancelled, the cancelling stroke is consumed, and
// no action is dispatched or replayed.
func (c *settingsCommand) runKeybindingSequenceDeliveryTest(actionID, sequence string) error {
	sequence, err := normalizeKeymapSequence(sequence)
	if err != nil {
		return err
	}
	expected := strings.Split(sequence, " ")
	observed := make([]string, 0, len(expected))
	for i, want := range expected {
		stroke, cancelled, captureErr := c.captureKeybindingSequenceStroke(actionID, observed)
		if captureErr != nil {
			return captureErr
		}
		if cancelled {
			c.setSettingsFeedback("Sequence delivery cancelled", fmt.Sprintf(
				"Observed %s; cancelled before stroke %d of %d; partial sequence returns to root with no replay or writes. %s",
				keybindingSequenceDraftSummary(observed), i+1, len(expected), c.keybindingSequenceDeliveryDiagnostic(sequence),
			))
			return nil
		}
		observed = append(observed, stroke)
		if stroke != want {
			c.setSettingsFeedback("Sequence delivery cancelled", fmt.Sprintf(
				"Observed %s; stroke %d expected %s but received %s; unknown continuation returns to root with no replay or writes. %s",
				strings.Join(observed, " "), i+1, want, stroke, c.keybindingSequenceDeliveryDiagnostic(sequence),
			))
			return nil
		}
	}
	c.setSettingsFeedback("Sequence delivery complete", fmt.Sprintf(
		"Observed %s exactly once; all %d logical strokes were delivered without writes. %s",
		strings.Join(observed, " "), len(expected), c.keybindingSequenceDeliveryDiagnostic(sequence),
	))
	return nil
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
			strings.Join(keyBindingEffectiveSequences(action), " "),
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
			SearchKey: strings.Join([]string{action.ID, displayName, action.Surface, action.Description, strings.Join(keybindingVisibleChords(action), " "), strings.Join(keyBindingEffectiveSequences(action), " "), keybindingLocalizedSearchText(locale, action)}, " "),
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
// read-only rows: what it targets, what it produces, where the result lands,
// which anchor it is placed against, and the exact shipped handler the key
// dispatches to. The handler rows come from the shipped CLI command manifest
// (internal/cli) so an action detail cannot advertise a route the binary does
// not have, and so a compatibility route whose canonical projection is
// narrower than its own behavior has to say so on the row.
func (c *settingsCommand) keybindingSemanticEntries(action keyBindingAction) []intpickercompat.Entry {
	entries := make([]intpickercompat.Entry, 0, 6)
	if semantics, ok := keyBindingActionSemanticsFor(action); ok {
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
	}
	handler, ok := keyBindingActionHandlerFor(action)
	if !ok {
		return entries
	}
	entries = append(entries, intpickercompat.Entry{
		Label:     c.rowLabelInfo("Handler", handler.Invocation, keybindingHandlerManifestSummary(handler)),
		Value:     settingsNoopValue,
		SearchKey: strings.ToLower("handler " + handler.Invocation + " " + handler.Manifest),
	})
	if note := strings.TrimSpace(handler.Note); note != "" {
		entries = append(entries, intpickercompat.Entry{
			Label:     c.rowLabelInfo("Handler boundary", note, ""),
			Value:     settingsNoopValue,
			SearchKey: strings.ToLower("handler boundary " + note),
		})
	}
	return entries
}

// keybindingHandlerManifestSummary states how the shipped command manifest
// classifies the handler route. "not a projmux route" is a real answer: direct
// tmux commands and in-picker commands never enter the CLI surface.
func keybindingHandlerManifestSummary(handler keyBindingActionHandler) string {
	if strings.TrimSpace(handler.Manifest) == "" {
		return "not a projmux route"
	}
	summary := "manifest " + handler.Manifest
	if strings.TrimSpace(handler.Disposition) != "" {
		summary += " (" + handler.Disposition + ")"
	}
	if len(handler.Canonical) != 0 {
		summary += "; canonical " + strings.Join(handler.Canonical, ", ")
	}
	return summary
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
	}
	entries = append(entries, c.keybindingSemanticEntries(action)...)
	entries = append(entries, intpickercompat.Entry{
		Label: c.rowLabelInfo("Single Keys", keybindingAliasesSummary(action), keybindingKeysDetail(action)),
		Value: settingsNoopValue,
	})
	prefix := settingsActionPrefixKeymap + action.ID + ":"
	for _, key := range keybindingVisibleChords(action) {
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphOpen, settingsColorType, keybindingChordDisplay(key), "key detail"),
			Value: prefix + "key:" + key,
		})
	}
	if keyBindingEditable(action) {
		addKeyHint := "press desired key"
		if !c.keybindingPhysicalCaptureAvailable() {
			addKeyHint = "record and confirm a key combination"
		}
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphAdd, settingsColorAdd, "+ Add key", addKeyHint),
			Value: prefix + "add",
		})
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphType, settingsColorType, "Enter key name manually", "type a tmux key name such as C-r or M-S-Left"),
			Value: prefix + "type",
		})
	}
	sequences := keyBindingEffectiveSequences(action)
	sequenceSummary := keybindingSequencesSummary(sequences)
	sequenceDetail := "ordered 2 to 4 stroke triggers"
	if action.Kind == keyBindingActionPickerInternal {
		sequenceSummary = "(not available)"
		sequenceDetail = "picker-local actions do not use tmux root-table sequences"
	}
	entries = append(entries, intpickercompat.Entry{
		Label: c.rowLabelInfo("Sequences", sequenceSummary, sequenceDetail),
		Value: settingsNoopValue,
	})
	for _, sequence := range sequences {
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphOpen, settingsColorType, sequence, "sequence detail"),
			Value: prefix + "sequence:" + sequence,
		})
	}
	if keyBindingEditable(action) && action.Kind != keyBindingActionPickerInternal {
		entries = append(entries,
			intpickercompat.Entry{
				Label: c.rowLabel(settingsGlyphAdd, settingsColorAdd, "+ Add sequence", "capture 2 to 4 logical strokes"),
				Value: prefix + "sequence-add",
			},
			intpickercompat.Entry{
				Label: c.rowLabel(settingsGlyphType, settingsColorType, "Enter sequence manually", "type a sequence such as C-k C-p"),
				Value: prefix + "sequence-type",
			},
		)
	}
	if !keyBindingEditable(action) {
		title := "Keybinding - " + keyBindingDisplayName(action)
		return entries, title, nil
	}
	entries = append(entries, intpickercompat.Entry{
		Label: c.rowLabelInfo("Options", keybindingActionsSummary(keymap, action, defaultAction), "choose a row below"),
		Value: settingsNoopValue,
	})
	if len(keybindingVisibleChords(action)) != 0 {
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphRemove, settingsColorRemove, "Unbind single keys", "remove all active keys from the Single Keys list; sequences remain"),
			Value: prefix + "unbind",
		})
	}
	if keybindingShowResetAction(state) {
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphBack, settingsColorBack, keybindingResetActionLabel(state, defaultAction), keybindingResetExplanation(defaultAction)),
			Value: prefix + "reset",
		})
	}
	title := "Keybinding - " + keyBindingDisplayName(action)
	return entries, title, nil
}

func keybindingSequencesSummary(sequences []string) string {
	if len(sequences) == 0 {
		return "(none)"
	}
	return strings.Join(sequences, ", ")
}

func keybindingSequenceDraftSummary(strokes []string) string {
	if len(strokes) == 0 {
		return "(no strokes yet)"
	}
	return strings.Join(strokes, " ")
}

func (c *settingsCommand) keybindingSequenceEditorEntries(actionID, replace string, strokes []string) ([]intpickercompat.Entry, string, error) {
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
		{Label: c.rowLabelInfo("Action", keyBindingDisplayName(action), "existing catalog action"), Value: settingsNoopValue},
		{Label: c.rowLabelInfo("Captured strokes", keybindingSequenceDraftSummary(strokes), fmt.Sprintf("%d of 4", len(strokes))), Value: settingsNoopValue},
	}
	if replace != "" {
		entries = append(entries, intpickercompat.Entry{Label: c.rowLabelInfo("Replacing", replace, "the old sequence remains active until Save succeeds"), Value: settingsNoopValue})
	}
	for i, stroke := range strokes {
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphRemove, settingsColorRemove, fmt.Sprintf("Stroke %d: %s", i+1, stroke), "remove from draft"),
			Value: fmt.Sprintf("%ssequence-stroke-remove:%d", prefix, i),
		})
	}
	if len(strokes) < 4 {
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphAdd, settingsColorAdd, "Record next stroke", "returns here immediately after one logical key"),
			Value: prefix + "sequence-capture",
		})
	} else {
		entries = append(entries, intpickercompat.Entry{Label: c.rowLabelInfo("Maximum length", "4 strokes", "Save or remove a draft stroke"), Value: settingsNoopValue})
	}
	if len(strokes) >= 2 {
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphToggle, settingsColorAdd, "Save sequence", strings.Join(strokes, " ")),
			Value: prefix + "sequence-save",
		})
	} else {
		entries = append(entries, intpickercompat.Entry{Label: c.rowLabelDim("Save sequence", "available after 2 strokes"), Value: settingsNoopValue})
	}
	return entries, "Sequence Editor - " + keyBindingDisplayName(action), nil
}

func (c *settingsCommand) keybindingSequenceDetailEntries(actionID, sequence string) ([]intpickercompat.Entry, string, error) {
	_, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return nil, "", err
	}
	action, ok := keyBindingActionByID(actions, actionID)
	if !ok {
		return nil, "", fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	sequence, err = normalizeKeymapSequence(sequence)
	if err != nil {
		return nil, "", err
	}
	if !containsString(keyBindingEffectiveSequences(action), sequence) {
		return nil, "", fmt.Errorf("sequence %q is not configured for %s", sequence, action.ID)
	}
	prefix := settingsActionPrefixKeymap + action.ID + ":"
	entries := []intpickercompat.Entry{
		c.backEntry(),
		{Label: c.rowLabelInfo("Sequence", sequence, fmt.Sprintf("%d logical strokes", len(strings.Split(sequence, " ")))), Value: settingsNoopValue},
		{Label: c.rowLabelInfo("Cancellation", "Escape or unknown stroke returns to root", "the cancelling stroke is consumed and never replayed"), Value: settingsNoopValue},
		{Label: c.rowLabelInfo("Delivery", c.keybindingSequenceDeliveryDiagnostic(sequence), "authoring and saved bytes are platform-independent"), Value: settingsNoopValue},
		{Label: c.rowLabel(settingsGlyphOpen, settingsColorType, "Test sequence delivery", "show partial/cancel and platform transport diagnostics without writing"), Value: prefix + "sequence-test:" + sequence},
		{Label: c.rowLabel(settingsGlyphType, settingsColorType, "Replace sequence", "capture a new 2 to 4 stroke sequence"), Value: prefix + "sequence-replace:" + sequence},
		{Label: c.rowLabel(settingsGlyphType, settingsColorType, "Enter replacement manually", "type a sequence such as C-k C-p"), Value: prefix + "sequence-type-replace:" + sequence},
		{Label: c.rowLabel(settingsGlyphRemove, settingsColorRemove, "Remove sequence", sequence), Value: prefix + "sequence-remove:" + sequence},
	}
	return entries, "Sequence - " + sequence, nil
}

func (c *settingsCommand) keybindingSequenceDeliveryDiagnostic(sequence string) string {
	enabled, err := c.currentNativeKeysSetting()
	if err != nil {
		return "saved logical strokes; native macOS delivery state unavailable; Linux/WSL use terminal delivery"
	}
	if !nativeKeysEnvEnabled(c.lookupEnv) || !enabled {
		return "saved logical strokes; terminal delivery on Linux/WSL and when Native macOS keybindings are off"
	}
	return "saved logical strokes; Linux/WSL use terminal delivery; Native macOS keybindings may deliver representable modified strokes locally"
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
	entries := make([]intpickercompat.Entry, 0, 6)
	if c.keybindingPhysicalCaptureAvailable() {
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphType, settingsColorType, "Press a key to add", "capture the key you press"),
			Value: prefix + "capture",
		})
	} else {
		// A disabled row with a reason and the working alternative, never a
		// selectable row that would block on a reader it cannot own.
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabelDim("Press a key to add", "unavailable here - "+c.keybindingPhysicalCaptureUnavailableReason()+"; use Enter key name manually"),
			Value: settingsNoopValue,
		})
	}
	entries = append(entries,
		intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphType, settingsColorType, "Enter key name manually", "type a tmux key name such as C-r or M-S-Left"),
			Value: prefix + "type",
		},
		intpickercompat.Entry{
			Label: c.rowLabelInfo("Safe direct keys", keybindingSafeDirectKeyPoolCopy(), "saved as logical tmux key names"),
			Value: settingsNoopValue,
		},
		intpickercompat.Entry{
			Label: c.rowLabelInfo("Never saved as keys", keybindingRiskyReservedKeyCopy(), "observed only, never written to keymap.toml"),
			Value: settingsNoopValue,
		},
		intpickercompat.Entry{
			Label: c.rowLabelInfo("Terminal adapter", keybindingTerminalAdapterCopy(action), "Projmux action owned"),
			Value: settingsNoopValue,
		},
		intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphBack, settingsColorBack, "Cancel", "return to action"),
			Value: settingsBackValue,
		},
	)
	return entries, "Add Key - " + keyBindingDisplayName(action), nil
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
		{
			Label: c.rowLabelInfo("Canonical key", chord, "logical tmux key name stored in keymap.toml"),
			Value: settingsNoopValue,
		},
		{
			Label: c.rowLabelInfo("Delivery path", keybindingDeliveryPathFor(action, chord), ""),
			Value: settingsNoopValue,
		},
	}
	if containsString(removableKeybindingKeys(keymap, action, defaultAction), chord) {
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(settingsGlyphRemove, settingsColorRemove, "Remove key", displayKey),
			Value: prefix + "remove:" + chord,
		})
	}
	// Test delivery is an Action row with an observable result. When no reader
	// can be owned here it renders as a disabled row carrying the reason and
	// the canonical next step instead of a row that does nothing.
	if reason, next, ok := c.keybindingDeliveryTestUnavailable(chord); ok {
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabelDim("Test delivery", "unavailable - "+reason+"; next: "+next),
			Value: settingsNoopValue,
		})
		return entries, "Key - " + displayKey, nil
	}
	entries = append(entries, intpickercompat.Entry{
		Label: c.rowLabel(settingsGlyphOpen, settingsColorType, "Test delivery", "press this key once and read the observed result"),
		Value: prefix + "test:" + chord,
	})
	return entries, "Key - " + displayKey, nil
}

// keybindingDeliveryPathFor states how the key reaches the action. It is
// metadata, not a probe: an in-picker key never enters a tmux key table, a
// transport-dependent default arrives as a terminal-emitted xterm sequence, and
// everything else is an explicit no-prefix tmux root binding.
func keybindingDeliveryPathFor(action keyBindingAction, chord string) string {
	if action.Kind == keyBindingActionPickerInternal {
		return "picker input loop; never entered into a tmux key table"
	}
	if action.Tier == keyBindingTierTransportDependent {
		if defaultAction, ok := keyBindingActionByID(defaultKeyBindingCatalog(), action.ID); ok &&
			strings.TrimSpace(chord) == strings.TrimSpace(defaultAction.PlainChord) {
			return "terminal-emitted xterm sequence to the tmux root key table"
		}
	}
	return "terminal to the tmux root key table (bind-key -n)"
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
	if len(keyBindingEffectiveSequences(current)) != 0 {
		return "Custom"
	}
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
	return strings.Join([]string{
		"keys " + keybindingListKeysSummary(action),
		"state " + state,
		fmt.Sprintf("sequences %d", len(keyBindingEffectiveSequences(action))),
	}, "  ")
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
		return "remove custom single keys and sequences"
	}
	return "restore " + keys + " and remove custom sequences"
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

// keybindingTerminalAdapterCopy names only the shipped `projmux setup terminal`
// adapters. Settings itself neither previews nor applies a terminal snippet, so
// the copy points at the command that does instead of promising an in-Settings
// adapter apply.
func keybindingTerminalAdapterCopy(action keyBindingAction) string {
	var adapters []string
	if strings.TrimSpace(action.GhosttyTrigger) != "" || strings.TrimSpace(action.GhosttyAction) != "" {
		adapters = append(adapters, "projmux setup terminal ghostty")
	}
	if strings.TrimSpace(action.WTID) != "" || strings.TrimSpace(action.WTInput) != "" {
		adapters = append(adapters, "projmux setup terminal windows-terminal")
	}
	if len(adapters) == 0 {
		return "no shipped adapter snippet for this Projmux action; choose a safe direct key"
	}
	return "run " + strings.Join(adapters, " or ") + " --apply from a shell; Settings does not apply terminal snippets"
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
	schema, err := c.migrateKeymapBeforeSave(stdout)
	if err != nil {
		return err
	}
	path, saveErr := saveKeymapKeys(c.keymapStore(), actionID, keys)
	return c.finishKeymapApply(schema, path, saveErr, stdout)
}

func (c *settingsCommand) saveKeymapSequencesAndApply(actionID string, sequences []string, stdout io.Writer) error {
	schema, err := c.migrateKeymapBeforeSave(stdout)
	if err != nil {
		return err
	}
	path, saveErr := saveKeymapSequences(c.keymapStore(), actionID, sequences)
	return c.finishKeymapApply(schema, path, saveErr, stdout)
}

func (c *settingsCommand) resetKeymapSequencesAndApply(actionID string, stdout io.Writer) error {
	schema, err := c.migrateKeymapBeforeSave(stdout)
	if err != nil {
		return err
	}
	path, resetErr := resetKeymapSequences(c.keymapStore(), actionID)
	return c.finishKeymapApply(schema, path, resetErr, stdout)
}

func (c *settingsCommand) resetKeymapBindingAndApply(actionID string, stdout io.Writer) error {
	schema, err := c.migrateKeymapBeforeSave(stdout)
	if err != nil {
		return err
	}
	path, resetErr := resetKeymapBinding(c.keymapStore(), actionID)
	return c.finishKeymapApply(schema, path, resetErr, stdout)
}

func (c *settingsCommand) validateKeymapSequenceForAction(actionID, sequence, replace string) error {
	sequence, err := normalizeKeymapSequence(sequence)
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
	sequences := append([]string(nil), keyBindingEffectiveSequences(action)...)
	if replace != "" {
		replace, err = normalizeKeymapSequence(replace)
		if err != nil {
			return err
		}
		sequences = removeString(sequences, replace)
	}
	sequences = append(sequences, sequence)
	if current.Bindings == nil {
		current.Bindings = map[string]keymapOverride{}
	}
	id := keymapBindingKeyForAction(current, defaultAction)
	override := current.Bindings[id]
	override.SequencesSet = true
	override.Sequences = sequences
	current.Bindings[id] = override
	_, err = mergeKeymapOverrides(defaultKeyBindingCatalog(), current)
	return err
}

func (c *settingsCommand) addKeymapSequenceAndApply(actionID, sequence string, stdout io.Writer) error {
	if err := c.validateKeymapSequenceForAction(actionID, sequence, ""); err != nil {
		return err
	}
	_, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return err
	}
	action, ok := keyBindingActionByID(actions, actionID)
	if !ok {
		return fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	sequences := append([]string(nil), keyBindingEffectiveSequences(action)...)
	sequences = append(sequences, sequence)
	return c.saveKeymapSequencesAndApply(action.ID, sequences, stdout)
}

func (c *settingsCommand) replaceKeymapSequenceAndApply(actionID, oldSequence, newSequence string, stdout io.Writer) error {
	if err := c.validateKeymapSequenceForAction(actionID, newSequence, oldSequence); err != nil {
		return err
	}
	_, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return err
	}
	action, ok := keyBindingActionByID(actions, actionID)
	if !ok {
		return fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	oldSequence, err = normalizeKeymapSequence(oldSequence)
	if err != nil {
		return err
	}
	sequences := keyBindingEffectiveSequences(action)
	found := false
	for i := range sequences {
		if sequences[i] == oldSequence {
			sequences[i] = newSequence
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("sequence %q is not configured for %s", oldSequence, action.ID)
	}
	return c.saveKeymapSequencesAndApply(action.ID, sequences, stdout)
}

func (c *settingsCommand) removeKeymapSequenceAndApply(actionID, sequence string, stdout io.Writer) error {
	sequence, err := normalizeKeymapSequence(sequence)
	if err != nil {
		return err
	}
	_, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return err
	}
	action, ok := keyBindingActionByID(actions, actionID)
	if !ok {
		return fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	sequences := keyBindingEffectiveSequences(action)
	if !containsString(sequences, sequence) {
		return fmt.Errorf("sequence %q is not configured for %s", sequence, action.ID)
	}
	sequences = removeString(sequences, sequence)
	if len(sequences) == 0 {
		return c.resetKeymapSequencesAndApply(action.ID, stdout)
	}
	return c.saveKeymapSequencesAndApply(action.ID, sequences, stdout)
}

func (c *settingsCommand) resetKeymapKeysAndApply(actionID string, stdout io.Writer) error {
	schema, err := c.migrateKeymapBeforeSave(stdout)
	if err != nil {
		return err
	}
	path, resetErr := resetKeymapKeys(c.keymapStore(), actionID)
	return c.finishKeymapApply(schema, path, resetErr, stdout)
}

// migrateKeymapBeforeSave brings the keymap to the current schema before a
// Settings write touches it, and returns the Schema stage to report.
//
// This is the lazy-convergence leg of the migration contract. A user who
// installed by unpacking a tarball over the old binary never ran an updater, so
// the first Settings key save is where their v0 file meets the new schema. It
// runs before the save rather than after so that the save writes canonical table
// ids directly instead of adding a v0 table that the next migration would have
// to rename.
//
// A *preflight* failure is deliberately not fatal here and returns a nil error.
// The preflight fails on exactly the conditions the save is about to fail on
// anyway — an unreadable home, an unparseable keymap, a chord conflict — and the
// pre-existing Saved-stage diagnostic names those better than a schema-stage one
// would. Only a failure of the migration itself (an irreconcilable table pair, a
// backup that could not be created, a verification that did not hold) abandons
// the save, because those are the ones where continuing would write into a file
// whose shape is no longer known.
func (c *settingsCommand) migrateKeymapBeforeSave(stdout io.Writer) (keymapApplyStage, error) {
	store := c.keymapStore()
	plan, err := planKeymapMigration(store)
	if err != nil {
		return keymapApplyStage{Status: keymapApplySkipped, Detail: "keymap could not be read"}, nil
	}
	result, err := applyKeymapMigration(store, plan)
	if err != nil {
		report := keymapApplyReport{
			Migrated: keymapApplyStage{Status: keymapApplyFailed, Detail: keymapApplyDiagnostic("keymap schema", err)},
			Saved:    keymapApplyStage{Status: keymapApplySkipped, Detail: "keymap schema was not migrated"},
			Prepared: keymapApplyStage{Status: keymapApplySkipped, Detail: "keymap schema was not migrated"},
			Live:     keymapApplyStage{Status: keymapApplySkipped, Detail: "keymap schema was not migrated"},
		}
		_ = writeKeymapApplyReport(stdout, report)
		return report.Migrated, fmt.Errorf("migrate keymap schema: %w", err)
	}
	return keymapSchemaStage(result), nil
}

// keymapSchemaStage describes the schema the keymap carries after a migration.
//
// An already-current file reports ok without claiming a migration happened;
// only a run that actually rewrote bytes names the backup it left behind.
func keymapSchemaStage(result keymapMigrationResult) keymapApplyStage {
	detail := fmt.Sprintf("schema_version %d", keymapSchemaVersion)
	if result.Migrated {
		detail = fmt.Sprintf("migrated schema_version %d -> %d, backup: %s",
			result.Plan.FromVersion, keymapSchemaVersion, result.BackupPath)
	}
	return keymapApplyStage{Status: keymapApplyOK, Detail: detail}
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

func (c *settingsCommand) finishKeymapApply(schema keymapApplyStage, path string, err error, stdout io.Writer) error {
	report := keymapApplyReport{
		Migrated: schema,
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
	if retireErr := c.retireCurrentTmuxKeySequenceState(); retireErr != nil {
		live = keymapApplyStage{Status: keymapApplyFailed, Detail: keymapApplyDiagnostic("live tmux sequence cleanup", retireErr)}
		return prepared, live, retireErr
	}
	if reloadErr := c.runCommand("tmux", "source-file", configPath); reloadErr != nil {
		live = keymapApplyStage{Status: keymapApplyFailed, Detail: keymapApplyDiagnostic("live tmux reload", reloadErr)}
		return prepared, live, reloadErr
	}
	live = keymapApplyStage{Status: keymapApplyOK}
	return prepared, live, nil
}

// retireCurrentTmuxKeySequenceState brings the Settings save path under the
// same Phase 0 stale-cleanup ordering as `tmux apply`: read the state recorded
// by the previous successful source, unbind exactly those roots/tables, then
// let the caller source the newly generated config. Production Settings always
// has runOutput; nil remains a compatibility seam for narrow unit commands
// that only record the historical source-file call.
func (c *settingsCommand) retireCurrentTmuxKeySequenceState() error {
	if c.runOutput == nil {
		return nil
	}
	read := func(option string) (string, error) {
		out, err := c.runOutput("tmux", "show-options", "-gqv", option)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", option, err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	roots, err := read(tmuxSequenceRootsOption)
	if err != nil {
		return err
	}
	tables, err := read(tmuxSequenceTablesOption)
	if err != nil {
		return err
	}
	for _, command := range keySequenceRetireCommandsWithPrefix([]string{"tmux"}, roots, tables) {
		if err := c.runCommand(command[0], command[1:]...); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(command[1:], " "), err)
		}
	}
	return nil
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
	// Migrated is the keymap schema stage. It leads because a v0 file must
	// reach v1 before a save writes into it, and because a migration failure
	// has to be reportable as its own stage: "the schema did not move, so
	// nothing else did either" is a different problem from a save that failed
	// on its own terms.
	Migrated keymapApplyStage
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
			keymapApplyLine("Schema", report.Migrated, false),
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
		keymapApplyLine("Schema", report.Migrated, true),
		keymapApplyLine("Saved", report.Saved, true),
		keymapApplyLine("Prepared", report.Prepared, true),
		keymapApplyLine("Running session", report.Live, true),
	} {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	switch {
	case report.Migrated.Status == keymapApplyFailed:
		_, err := fmt.Fprintln(w, "Recovery: resolve the keymap schema problem, then try the Settings change again.")
		return err
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
