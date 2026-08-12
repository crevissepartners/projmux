package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/i18n"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func (c *settingsCommand) runNotificationsSection(stdout, stderr io.Writer) error {
	for {
		options, err := c.sectionOptions(settingsSectionNotifications)
		if err != nil {
			return err
		}
		result, err := c.runPicker(options)
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
		case action == settingsNotificationsDesktop:
			if err := c.runNotificationsDesktopSection(stdout, stderr); err != nil {
				return err
			}
		case action == settingsNotificationsAIDedupe:
			if err := c.runNotificationsAIDedupeSection(stdout, stderr); err != nil {
				return err
			}
		case action == settingsNotificationsDelivery:
			if err := c.runNotificationsDeliverySourcesSection(stdout, stderr); err != nil {
				return err
			}
		case action == settingsNotificationsHookActions:
			if err := c.runNotificationsHookActionsSection(stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown notifications settings action: %s", action)
		}
	}
}

func (c *settingsCommand) runNotificationsAIDedupeSection(stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-notifications-ai-dedupe",
			Entries:    c.aiNotifyDedupeEntries(),
			Title:      "Notifications - AI dedupe window",
			Prompt:     "Settings > Notifications > AI dedupe > ",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent "),
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
		case action == settingsActionPrefixAINotifyDedupe+"custom":
			if err := c.runNotificationsAIDedupeCustom(stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixAINotifyDedupe):
			seconds, err := parseAINotifyDedupeSeconds(strings.TrimPrefix(action, settingsActionPrefixAINotifyDedupe))
			if err != nil {
				return err
			}
			if err := c.runSettingsMutation("AI notification dedupe", stdout, stderr, func(out, _ io.Writer) error {
				return c.setAINotifyDedupeSeconds(seconds, out)
			}); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown AI notification dedupe settings action: %s", action)
		}
	}
}

func (c *settingsCommand) runNotificationsAIDedupeCustom(stdout, stderr io.Writer) error {
	current := c.currentAINotifyDedupeSeconds()
	result, err := c.runPicker(intpickercompat.Options{
		UI:           "settings-notifications-ai-dedupe-custom",
		Entries:      nil,
		AcceptQuery:  true,
		InitialQuery: strconv.Itoa(current.Seconds),
		Title:        "Notifications - Custom AI dedupe",
		Prompt:       "AI dedupe seconds > ",
		Footer:       projmuxFooter("Enter: save  |  Example: 120 "),
		ExpectKeys:   []string{"enter"},
		Bindings:     c.settingsCloseBindings(),
	})
	if err != nil {
		return err
	}
	if result.Key != "enter" {
		return nil
	}
	seconds, err := parseAINotifyDedupeSeconds(result.Query)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		c.setSettingsFeedback("AI notification dedupe failed", err.Error())
		return nil
	}
	return c.runSettingsMutation("AI notification dedupe", stdout, stderr, func(out, _ io.Writer) error {
		return c.setAINotifyDedupeSeconds(seconds, out)
	})
}

func (c *settingsCommand) runNotificationsDesktopSection(stdout, stderr io.Writer) error {
	locale := appLocale(c.homeDir, c.lookupEnv)
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-notifications-desktop",
			Entries:    c.desktopNotifyEntries(),
			Title:      settingsNotificationsDesktopTitle(locale),
			Prompt:     settingsNotificationsDesktopPrompt(locale),
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent "),
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
		case strings.HasPrefix(action, settingsActionPrefixDesktopNotifyMode):
			if err := c.executeWithFeedback(action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown desktop notification settings action: %s", action)
		}
	}
}

func (c *settingsCommand) notificationsEntries() []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	notifyMode, notifySource := settingsDesktopNotifyResolver(c.homeDir, c.lookupEnv).resolveMode()
	dedupe := c.currentAINotifyDedupeSeconds()
	return []intpickercompat.Entry{
		settingsBackEntryLocale(locale),
		{
			Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, settingsNotificationsDesktopLabel(locale), desktopNotifyDisplayName(notifyMode)+" - "+string(notifySource)),
			Value:     settingsNotificationsDesktop,
			SearchKey: "desktop notifications none notify raise toast osfocus",
		},
		{
			Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "AI notification dedupe", fmt.Sprintf("%ds - %s", dedupe.Seconds, dedupe.Source)),
			Value:     settingsNotificationsAIDedupe,
			SearchKey: "AI notification dedupe seconds duration duplicate collapse desktop",
		},
		{
			Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Delivery sources", c.aiNotifyDiagnosticsSummary()),
			Value:     settingsNotificationsDelivery,
			SearchKey: "delivery sources producer setup doctor codex claude tmux bell hooks diagnostics",
		},
		{
			Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Hook quiet policy", c.aiHookActionsSummary()),
			Value:     settingsNotificationsHookActions,
			SearchKey: "hook quiet policy runtime action codex claude notify state quiet",
		},
	}
}

func settingsNotificationsDesktopLabel(locale i18n.Locale) string {
	return localizeText(locale, i18n.KeySettingsNotificationsDesktop, string(i18n.KeySettingsNotificationsDesktop))
}

func settingsNotificationsDesktopTitle(locale i18n.Locale) string {
	return localizeText(locale, i18n.Key("settings.title.notifications_desktop"), "settings.title.notifications_desktop")
}

func settingsNotificationsDesktopPrompt(locale i18n.Locale) string {
	return localizeText(locale, i18n.Key("settings.prompt.settings_desktop_notifications"), "settings.prompt.settings_desktop_notifications")
}

func (c *settingsCommand) runNotificationsHookActionsSection(stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-notifications-hook-actions",
			Entries:    c.aiHookProviderEntries(),
			Title:      "Notifications - Hook quiet policy",
			Prompt:     "Settings > Notifications > Hook quiet policy > ",
			Footer:     projmuxFooter("Enter: view hooks  |  Back row: parent "),
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
		case strings.HasPrefix(action, settingsActionPrefixAIHookProvider):
			provider := strings.TrimPrefix(action, settingsActionPrefixAIHookProvider)
			if err := c.runAIHookProviderActionSection(provider, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown AI hook policy action: %s", action)
		}
	}
}

func (c *settingsCommand) runAIHookProviderActionSection(provider string, stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-notifications-hook-provider",
			Entries:    c.aiHookEventEntries(provider),
			Title:      "Hook quiet policy - " + aiHookProviderLabel(provider),
			Prompt:     "Settings > Notifications > Hook quiet policy > " + aiHookProviderLabel(provider) + " > ",
			Footer:     projmuxFooter("Enter: change action  |  Back row: parent "),
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
		case strings.HasPrefix(action, settingsActionPrefixAIHookEvent):
			p, event, ok := parseAIHookSettingsPair(strings.TrimPrefix(action, settingsActionPrefixAIHookEvent))
			if !ok || p != provider {
				return fmt.Errorf("unknown AI hook event action: %s", action)
			}
			if err := c.runAIHookEventActionSection(provider, event, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown AI hook provider action: %s", action)
		}
	}
}

func (c *settingsCommand) runAIHookEventActionSection(provider, event string, stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-notifications-hook-event",
			Entries:    c.aiHookActionChoiceEntries(provider, event),
			Title:      "Hook quiet policy - " + aiHookProviderLabel(provider) + " " + event,
			Prompt:     "Settings > Notifications > Hook quiet policy > " + aiHookProviderLabel(provider) + " > " + event + " > ",
			Footer:     projmuxFooter("Enter: save  |  Back row: parent "),
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
		case strings.HasPrefix(action, settingsActionPrefixAIHookSet):
			p, e, next, ok := parseAIHookSettingsTriple(strings.TrimPrefix(action, settingsActionPrefixAIHookSet))
			if !ok || p != provider || e != event {
				return fmt.Errorf("unknown AI hook action choice: %s", action)
			}
			if next == "default" {
				if err := c.runSettingsMutation("Hook quiet policy", stdout, stderr, func(out, _ io.Writer) error {
					return c.clearAIHookAction(provider, event, out)
				}); err != nil {
					return err
				}
				continue
			}
			if err := c.runSettingsMutation("Hook quiet policy", stdout, stderr, func(out, _ io.Writer) error {
				return c.setAIHookAction(provider, event, next, out)
			}); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown AI hook event action: %s", action)
		}
	}
}

func (c *settingsCommand) runNotificationsDeliverySourcesSection(stdout, stderr io.Writer) error {
	_ = stdout
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-notifications-delivery",
			Entries:    c.aiNotifyDiagnosticEntries(),
			Title:      "Notifications - Delivery sources",
			Prompt:     "Settings > Notifications > Delivery sources > ",
			Footer:     projmuxFooter("Enter: view details  |  Back row: parent "),
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
		case strings.HasPrefix(action, settingsActionPrefixAINotifyDiagnostic):
			id := strings.TrimPrefix(action, settingsActionPrefixAINotifyDiagnostic)
			if err := c.runAINotifyDiagnosticDetail(id, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown AI notify diagnostics action: %s", action)
		}
	}
}

func (c *settingsCommand) runAINotifyDiagnosticsSection(stdout, stderr io.Writer) error {
	return c.runNotificationsDeliverySourcesSection(stdout, stderr)
}

func (c *settingsCommand) runAINotifyDiagnosticDetail(id string, stderr io.Writer) error {
	for {
		diag, ok := c.aiNotifyDiagnosticByID(id)
		if !ok {
			return fmt.Errorf("unknown AI notify integration: %s", id)
		}
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-notifications-delivery-detail",
			Entries:    aiNotifyDiagnosticDetailEntriesLocale(c.locale(), diag),
			Title:      "AI Notify - " + diag.Name,
			Prompt:     "Settings > Notifications > Delivery sources > " + diag.Name + " > ",
			Footer:     projmuxFooter("Enter: copy command  |  Back row: parent "),
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
		if strings.HasPrefix(action, settingsActionPrefixAINotifyCommand) {
			command, label, ok := aiNotifyDiagnosticCommandAction(diag, action)
			if !ok {
				return fmt.Errorf("unknown AI notify diagnostic command action: %s", action)
			}
			c.copySettingsCommand(diag.Name+" "+label, command, stderr)
			continue
		}
		return fmt.Errorf("unknown AI notify diagnostic detail action: %s", action)
	}
}

func (c *settingsCommand) currentAINotifyDiagnostics() []doctorAINotifyIntegration {
	var diagnostics []doctorAINotifyIntegration
	if c.aiNotifyDiagnostics != nil {
		diagnostics = c.aiNotifyDiagnostics()
	} else {
		// The doctor diagnostics need the full *aiCommand. Recover it when
		// the injected dependency is the concrete command (production
		// wiring); otherwise pass nil so doctorAINotifyDiagnostics builds
		// its own default, matching the previous nil-ai handling.
		ai, _ := c.ai.(*aiCommand)
		diagnostics = doctorAINotifyDiagnostics(ai)
	}
	return diagnostics
}

func (c *settingsCommand) copySettingsCommand(label, command string, stderr io.Writer) {
	command = strings.TrimSpace(command)
	if command == "" {
		return
	}
	runner := c.tmuxRunner
	if runner == nil {
		runner = inttmux.ExecRunner{}
	}
	if _, err := runner.Run(context.Background(), "tmux", "set-buffer", "-w", "--", command); err != nil {
		if stderr != nil {
			_, _ = fmt.Fprintf(stderr, "warning: copy %s to clipboard: %v\n", label, err)
		}
		return
	}
	_, _ = runner.Run(context.Background(), "tmux", "display-message", label+" copied to clipboard")
}

func (c *settingsCommand) aiNotifyDiagnosticsSummary() string {
	counts := map[doctorAINotifyStatus]int{}
	for _, diag := range c.currentAINotifyDiagnostics() {
		counts[diag.Status]++
	}
	parts := make([]string, 0, 3)
	for _, status := range []doctorAINotifyStatus{
		doctorAINotifyStatusInstalled,
		doctorAINotifyStatusConflict,
		doctorAINotifyStatusMissing,
		doctorAINotifyStatusSkip,
	} {
		if counts[status] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[status], status))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, "read-only doctor status")
	}
	return strings.Join(append(parts, c.notifyHookOverrideSummary()), ", ")
}

func (c *settingsCommand) notifyHookOverrideSummary() string {
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("PROJMUX_NOTIFY_HOOK")) != "" {
		return "hook override set"
	}
	return "built-in desktop sender"
}

func (c *settingsCommand) aiNotifyDiagnosticEntries() []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	diagnostics := c.currentAINotifyDiagnostics()
	entries := make([]intpickercompat.Entry, 0, len(diagnostics)+2)
	entries = append(entries, settingsBackEntryLocale(locale))
	hookValue := "not set"
	hookSource := "built-in platform sender"
	if c.lookupEnv != nil {
		if value := strings.TrimSpace(c.lookupEnv("PROJMUX_NOTIFY_HOOK")); value != "" {
			hookValue = value
			hookSource = "PROJMUX_NOTIFY_HOOK env"
		}
	}
	entries = append(entries, intpickercompat.Entry{
		Label:     settingsLabelInfoLocale(locale, "Desktop sender override", hookValue, hookSource),
		Value:     settingsNoopValue,
		SearchKey: "PROJMUX_NOTIFY_HOOK desktop sender override env",
	})
	for _, diag := range diagnostics {
		entries = append(entries, aiNotifyDiagnosticEntryLocale(locale, diag))
	}
	return entries
}

func (c *settingsCommand) aiNotifyDiagnosticByID(id string) (doctorAINotifyIntegration, bool) {
	for _, diag := range c.currentAINotifyDiagnostics() {
		if diag.ID == id {
			return diag, true
		}
	}
	return doctorAINotifyIntegration{}, false
}

func aiNotifyDiagnosticEntryLocale(locale i18n.Locale, diag doctorAINotifyIntegration) intpickercompat.Entry {
	glyph, color := aiNotifyDiagnosticTone(diag.Status)
	desc := string(diag.Status)
	if diag.TestedVersion != "" {
		desc += " - tested with " + diag.TestedVersion
	}
	if diag.ConfigPath != "" {
		desc += " - " + diag.ConfigPath
	}
	if diag.ConflictReason != "" {
		desc += " - " + diag.ConflictReason
	}
	return intpickercompat.Entry{
		Label:     settingsLabelLocale(locale, glyph, color, diag.Name, desc),
		Value:     settingsActionPrefixAINotifyDiagnostic + diag.ID,
		SearchKey: strings.Join([]string{diag.Name, string(diag.Status), diag.TestedVersion, diag.Guidance, diag.ConfigPath, diag.ConflictReason, diag.InstallCommand, diag.RemoveCommand, diag.DryRunCommand}, " "),
	}
}

func aiNotifyDiagnosticTone(status doctorAINotifyStatus) (string, string) {
	switch status {
	case doctorAINotifyStatusInstalled:
		return settingsGlyphToggle, settingsColorAdd
	case doctorAINotifyStatusConflict:
		return settingsGlyphInfo, settingsColorRemove
	default:
		return settingsGlyphInactive, settingsColorDim
	}
}

func aiNotifyDiagnosticDetailEntriesLocale(locale i18n.Locale, diag doctorAINotifyIntegration) []intpickercompat.Entry {
	entries := []intpickercompat.Entry{
		settingsBackEntryLocale(locale),
		{Label: settingsLabelInfoLocale(locale, "Status", string(diag.Status), "doctor"), Value: settingsNoopValue},
	}
	if diag.ConfigPath != "" {
		entries = append(entries, intpickercompat.Entry{Label: settingsLabelInfoLocale(locale, "Config path", diag.ConfigPath, ""), Value: settingsNoopValue})
	}
	if diag.ConflictReason != "" {
		entries = append(entries, intpickercompat.Entry{Label: settingsLabelInfoLocale(locale, "Conflict", diag.ConflictReason, ""), Value: settingsNoopValue})
	}
	if diag.TestedVersion != "" {
		entries = append(entries, intpickercompat.Entry{Label: settingsLabelInfoLocale(locale, "Tested version", diag.TestedVersion, "catalog"), Value: settingsNoopValue})
	}
	if diag.Guidance != "" {
		entries = append(entries, intpickercompat.Entry{Label: settingsLabelInfoLocale(locale, "Notice", diag.Guidance, ""), Value: settingsNoopValue})
	}
	entries = append(entries,
		aiNotifyDiagnosticCommandEntryLocale(locale, diag, "install", "Install command", diag.InstallCommand),
		aiNotifyDiagnosticCommandEntryLocale(locale, diag, "remove", "Remove command", diag.RemoveCommand),
		aiNotifyDiagnosticCommandEntryLocale(locale, diag, "dry-run", "Dry-run command", diag.DryRunCommand),
		intpickercompat.Entry{Label: settingsLabelDimLocale(locale, "Copy only", "Settings copies command text and does not execute these commands"), Value: settingsNoopValue},
	)
	return entries
}

func aiNotifyDiagnosticCommandEntryLocale(locale i18n.Locale, diag doctorAINotifyIntegration, kind, label, command string) intpickercompat.Entry {
	if strings.TrimSpace(command) == "" {
		return intpickercompat.Entry{Label: settingsLabelInfoLocale(locale, label, "", "unavailable"), Value: settingsNoopValue}
	}
	return intpickercompat.Entry{
		Label:     settingsLabelInfoLocale(locale, label, command, "Enter copies"),
		Value:     settingsActionPrefixAINotifyCommand + diag.ID + ":" + kind,
		SearchKey: strings.Join([]string{label, command, "copy clipboard"}, " "),
	}
}

func aiNotifyDiagnosticCommandAction(diag doctorAINotifyIntegration, action string) (command, label string, ok bool) {
	rest := strings.TrimPrefix(action, settingsActionPrefixAINotifyCommand)
	id, kind, found := strings.Cut(rest, ":")
	if !found || id != diag.ID {
		return "", "", false
	}
	switch kind {
	case "install":
		return diag.InstallCommand, "install command", strings.TrimSpace(diag.InstallCommand) != ""
	case "remove":
		return diag.RemoveCommand, "remove command", strings.TrimSpace(diag.RemoveCommand) != ""
	case "dry-run":
		return diag.DryRunCommand, "dry-run command", strings.TrimSpace(diag.DryRunCommand) != ""
	default:
		return "", "", false
	}
}

// setDesktopNotifyMode writes the user-facing 3-way choice into the durable
// Settings config and mirrors it into the live `@projmux_desktop_notify_mode`
// tmux user-option when a tmux server is available. The env variables
// (`PROJMUX_DESKTOP_NOTIFY_MODE`, plus the legacy `PROJMUX_DESKTOP_NOTIFY`)
// continue to take priority at resolve time, so toggling here when an env is
// set will appear to "do nothing" — the Settings info row surfaces the source
// so users see why.
//
// The legacy `@projmux_desktop_notify` option is intentionally NOT
// rewritten here. Read-time migration keeps honoring it for users who
// never opened the Settings row; once they pick a value via this code
// path the new option pins the resolution and the legacy one is
// effectively orphaned.
//
// When projmux runs outside tmux the saved config still changes; only the live
// tmux mirror is skipped.
func (c *settingsCommand) setDesktopNotifyMode(value string) error {
	mode, ok := parseDesktopNotifyMode(value)
	if !ok {
		return fmt.Errorf("unknown desktop notification mode: %s", value)
	}
	saved := desktopNotifyConfigValue(mode)
	paths, err := pickerBackendConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return err
	}
	if err := config.SaveDesktopNotifyModeFile(paths.DesktopNotifyModeFile(), saved); err != nil {
		return err
	}
	if c.lookupEnv == nil || strings.TrimSpace(c.lookupEnv("TMUX")) == "" {
		return nil
	}
	if c.runCommand == nil {
		return errors.New("settings runner is not configured")
	}
	if err := c.runCommand("tmux", "set-option", "-g", desktopNotifyModeTmuxOption, string(saved)); err != nil {
		return fmt.Errorf("set live tmux desktop-notify-mode option: %w", err)
	}
	_ = c.runCommand("tmux", "display-message", "desktop notifications: "+string(saved))
	return nil
}

func (c *settingsCommand) desktopNotifyEntries() []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	notifyMode, notifySource := settingsDesktopNotifyResolver(c.homeDir, c.lookupEnv).resolveMode()
	entries := []intpickercompat.Entry{
		settingsBackEntryLocale(locale),
		{
			Label: settingsLabelInfoLocale(locale, settingsNotificationsDesktopLabel(locale), desktopNotifyDisplayName(notifyMode), string(notifySource)),
			Value: settingsNoopValue,
		},
	}
	for _, item := range []struct {
		mode desktopNotifyMode
		name string
		desc string
	}{
		{desktopNotifyModeNone, string(config.DesktopNotifyModeOff), "silence OS notifications; in-app notify queue is unaffected"},
		{desktopNotifyModeNotify, string(config.DesktopNotifyModeNotify), "fire toast / notify-send for AI reply-ready without click-to-focus"},
		{desktopNotifyModeRaise, string(config.DesktopNotifyModeRaise), "fire toast with click-to-focus and auto-raise host terminal via osfocus chain"},
	} {
		glyph := settingsGlyphInactive
		color := settingsColorDim
		if item.mode == notifyMode {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
		}
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelLocale(locale, glyph, color, item.name, item.desc),
			Value: settingsActionPrefixDesktopNotifyMode + item.name,
		})
	}
	return entries
}

func desktopNotifyDisplayName(mode desktopNotifyMode) string {
	if mode == desktopNotifyModeNone {
		return string(config.DesktopNotifyModeOff)
	}
	return string(desktopNotifyConfigValue(mode))
}

func (c *settingsCommand) aiNotifyDedupeEntries() []intpickercompat.Entry {
	current := c.currentAINotifyDedupeSeconds()
	entries := []intpickercompat.Entry{
		c.backEntry(),
		{
			Label: c.rowLabelInfo("AI notification dedupe", fmt.Sprintf("%ds", current.Seconds), string(current.Source)),
			Value: settingsNoopValue,
		},
		{
			Label: c.rowLabelInfo("Scope", "desktop AI notifications", "tmux bell fallback stays 5s"),
			Value: settingsNoopValue,
		},
	}
	for _, seconds := range []int{30, 60, 120, 300} {
		glyph := settingsGlyphInactive
		color := settingsColorDim
		if seconds == current.Seconds {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
		}
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(glyph, color, fmt.Sprintf("%ds", seconds), "collapse duplicate desktop AI notifications"),
			Value: settingsActionPrefixAINotifyDedupe + strconv.Itoa(seconds),
		})
	}
	entries = append(entries, intpickercompat.Entry{
		Label: c.rowLabel(settingsGlyphType, settingsColorType, "Custom seconds", "store a positive seconds value"),
		Value: settingsActionPrefixAINotifyDedupe + "custom",
	})
	return entries
}

func (c *settingsCommand) aiHookActionsSummary() string {
	file := c.currentAIHookActionsFile()
	count := 0
	for _, provider := range file.Providers {
		count += len(provider.Events)
	}
	if count == 0 {
		return "catalog defaults"
	}
	return fmt.Sprintf("%d runtime override(s)", count)
}

func (c *settingsCommand) aiHookProviderEntries() []intpickercompat.Entry {
	entries := []intpickercompat.Entry{
		c.backEntry(),
		{
			Label: c.rowLabelInfo("Runtime config", c.aiHookActionsSummary(), "install events stay in catalog"),
			Value: settingsNoopValue,
		},
	}
	providers := []string{aiHookProviderCodex, aiHookProviderClaude}
	file := c.currentAIHookActionsFile()
	for provider := range file.Providers {
		if provider != aiHookProviderCodex && provider != aiHookProviderClaude {
			providers = append(providers, provider)
		}
	}
	sort.Strings(providers[2:])
	for _, provider := range providers {
		entries = append(entries, intpickercompat.Entry{
			Label:     c.rowLabel(settingsGlyphOpen, settingsColorType, aiHookProviderLabel(provider)+" hooks", c.aiHookProviderSummary(provider)),
			Value:     settingsActionPrefixAIHookProvider + provider,
			SearchKey: provider + " hooks quiet notify state runtime catalog",
		})
	}
	return entries
}

func (c *settingsCommand) aiHookProviderSummary(provider string) string {
	cmd := c.aiForSettings()
	catalog, err := cmd.loadAIHookCatalog(provider)
	if err != nil {
		file := c.currentAIHookActionsFile()
		if actions, ok := file.Providers[provider]; ok && len(actions.Events) > 0 {
			return fmt.Sprintf("%d runtime event(s)", len(actions.Events))
		}
		return "no catalog"
	}
	counts := map[string]int{}
	for _, event := range catalog.Events {
		counts[cmd.aiHookEffectiveAction(provider, event.Name).Action]++
	}
	return fmt.Sprintf("%d notify, %d state, %d quiet", counts[aiHookActionNotify], counts[aiHookActionState], counts[aiHookActionQuiet])
}

func (c *settingsCommand) aiHookEventEntries(provider string) []intpickercompat.Entry {
	entries := []intpickercompat.Entry{
		c.backEntry(),
		{
			Label: c.rowLabelInfo("Scope", "runtime action only", "install field is unchanged"),
			Value: settingsNoopValue,
		},
	}
	cmd := c.aiForSettings()
	seen := map[string]bool{}
	if catalog, err := cmd.loadAIHookCatalog(provider); err == nil {
		for _, event := range catalog.Events {
			seen[event.Name] = true
			resolution := cmd.aiHookEffectiveAction(provider, event.Name)
			desc := resolution.Action + " - " + resolution.Source
			if resolution.Action == aiHookActionNotify {
				desc += " - " + aiHookNotifyDeliveryDescription(provider, event.Name)
			}
			if event.Install {
				desc += " - install=true"
			} else {
				desc += " - install=false"
			}
			entries = append(entries, intpickercompat.Entry{
				Label:     c.rowLabel(aiHookActionGlyph(resolution.Action), aiHookActionColor(resolution.Action), event.Name, desc),
				Value:     settingsActionPrefixAIHookEvent + provider + ":" + event.Name,
				SearchKey: strings.Join([]string{provider, event.Name, resolution.Action, resolution.Source, "quiet notify state"}, " "),
			})
		}
	}
	file := c.currentAIHookActionsFile()
	if actions, ok := file.Providers[provider]; ok {
		extras := make([]string, 0, len(actions.Events))
		for event := range actions.Events {
			if !seen[event] {
				extras = append(extras, event)
			}
		}
		sort.Strings(extras)
		for _, event := range extras {
			action := actions.Events[event]
			entries = append(entries, intpickercompat.Entry{
				Label:     c.rowLabel(aiHookActionGlyph(action), aiHookActionColor(action), event, action+" - runtime - install not managed"),
				Value:     settingsActionPrefixAIHookEvent + provider + ":" + event,
				SearchKey: strings.Join([]string{provider, event, action, "runtime quiet notify state"}, " "),
			})
		}
	}
	return entries
}

func (c *settingsCommand) aiHookActionChoiceEntries(provider, event string) []intpickercompat.Entry {
	cmd := c.aiForSettings()
	resolution := cmd.aiHookEffectiveAction(provider, event)
	entries := []intpickercompat.Entry{
		c.backEntry(),
		{
			Label: c.rowLabelInfo("Current", resolution.Action, resolution.Source),
			Value: settingsNoopValue,
		},
		{
			Label: c.rowLabelInfo("Install", "unchanged", "Settings only changes runtime action"),
			Value: settingsNoopValue,
		},
	}
	choices := []struct {
		action string
		desc   string
	}{
		{"default", "use embedded or local catalog action"},
		{aiHookActionNotify, aiHookNotifyDeliveryDescription(provider, event)},
		{aiHookActionState, "update pane state without notification delivery"},
		{aiHookActionQuiet, "mark hook-active and log only"},
	}
	for _, choice := range choices {
		glyph := settingsGlyphInactive
		color := settingsColorDim
		if (choice.action == "default" && resolution.Source == aiHookActionSourceCatalog) || choice.action == resolution.Action && resolution.Source == aiHookActionSourceRuntime {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
		}
		entries = append(entries, intpickercompat.Entry{
			Label:     c.rowLabel(glyph, color, choice.action, choice.desc),
			Value:     settingsActionPrefixAIHookSet + provider + ":" + event + ":" + choice.action,
			SearchKey: provider + " " + event + " " + choice.action + " " + choice.desc,
		})
	}
	return entries
}

func aiHookProviderLabel(provider string) string {
	switch provider {
	case aiHookProviderCodex:
		return "Codex"
	case aiHookProviderClaude:
		return "Claude"
	default:
		return provider
	}
}

func aiHookNotifyDeliveryDescription(provider, event string) string {
	if aiHookHasDesktopNotifyHandler(provider, event) {
		return "in-app queue + OS toast supported by specialized handler"
	}
	if aiHookHasGenericNotifyHandler(provider, event) {
		return "generic in-app queue only; OS toast unsupported"
	}
	return "no notify handler; falls back to hook-active log only"
}

func aiHookHasDesktopNotifyHandler(provider, event string) bool {
	switch provider {
	case aiHookProviderCodex:
		switch event {
		case "PermissionRequest", "Stop":
			return true
		}
	case aiHookProviderClaude:
		switch event {
		case "Notification", "PermissionRequest", "Stop", "StopFailure", "SubagentStop", "TeammateIdle":
			return true
		}
	}
	return false
}

func aiHookHasGenericNotifyHandler(provider, event string) bool {
	if provider != aiHookProviderCodex {
		return false
	}
	switch event {
	case "PreToolUse", "PostToolUse", "PreCompact", "PostCompact", "SessionStart":
		return true
	default:
		return false
	}
}

func aiHookActionGlyph(action string) string {
	switch action {
	case aiHookActionNotify:
		return settingsGlyphToggle
	case aiHookActionState:
		return settingsGlyphInfo
	default:
		return settingsGlyphInactive
	}
}

func aiHookActionColor(action string) string {
	switch action {
	case aiHookActionNotify:
		return settingsColorAdd
	case aiHookActionState:
		return settingsColorType
	default:
		return settingsColorDim
	}
}

func parseAIHookSettingsPair(value string) (provider, event string, ok bool) {
	provider, event, ok = strings.Cut(value, ":")
	return provider, event, ok && provider != "" && event != ""
}

func parseAIHookSettingsTriple(value string) (provider, event, action string, ok bool) {
	provider, rest, ok := strings.Cut(value, ":")
	if !ok {
		return "", "", "", false
	}
	event, action, ok = strings.Cut(rest, ":")
	return provider, event, action, ok && provider != "" && event != "" && action != ""
}

func parseAINotifyDedupeSeconds(raw string) (int, error) {
	seconds, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || seconds <= 0 {
		return 0, fmt.Errorf("AI notification dedupe %q must be positive seconds", raw)
	}
	return seconds, nil
}
