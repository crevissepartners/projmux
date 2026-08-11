package app

import (
	"fmt"
	"strings"

	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/theme"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

func (c *settingsCommand) rootOptions(tab settingsRootTab) intpickercompat.Options {
	if tab != settingsRootTabProject {
		tab = settingsRootTabGlobal
	}
	ctx := c.resolveSettingsProjectContext()
	locale := appLocale(c.homeDir, c.lookupEnv)
	return intpickercompat.Options{
		UI:         "settings",
		Entries:    c.rootEntriesForTabLocale(tab, locale),
		Title:      localizeText(locale, i18n.KeySettingsRootTitle, "Settings"),
		TitleChips: settingsRootTabChipsLocale(tab, ctx.hasProject(), locale),
		Prompt:     settingsRootPromptLocale(tab, locale),
		Header:     settingsRootContextHeader(tab, ctx),
		Footer:     localizeText(locale, i18n.KeySettingsRootFooter, "Open rows or click a scope chip to switch tabs."),
		ExpectKeys: []string{"enter", "ctrl-g", "ctrl-p", "alt-shift-left", "alt-shift-right"},
		Bindings:   c.settingsCloseBindings(),
	}
}

func settingsRootTabChipsLocale(active settingsRootTab, hasProject bool, locale i18n.Locale) []projmuxpicker.Chip {
	return []projmuxpicker.Chip{
		{
			Label:      settingsCatalogTextLocale(locale, "Global"),
			Active:     active == settingsRootTabGlobal,
			ClickValue: settingsRootTabGlobalValue,
		},
		{
			Label:      settingsCatalogTextLocale(locale, "Project"),
			Active:     active == settingsRootTabProject,
			Disabled:   !hasProject,
			ClickValue: settingsRootTabProjectValue,
		},
	}
}

func settingsPassiveRootTabChipsLocale(active settingsRootTab, hasProject bool, locale i18n.Locale) []projmuxpicker.Chip {
	chips := settingsRootTabChipsLocale(active, hasProject, locale)
	for i := range chips {
		chips[i].ClickValue = ""
	}
	return chips
}

// settingsRootContextHeader returns the popup header text above the
// search bar. Phase 2.5 ships the titlebar chip strip whose labels (and
// the Project chip's disabled/active state) already announce the active
// scope and whether a project context exists. The dedicated
// "Project context: (...)" header line was redundant with that chip
// metaphor, so Phase 2.7 drops it entirely and returns the empty string
// — the chip strip is the source of truth.
func settingsRootContextHeader(tab settingsRootTab, ctx settingsProjectContext) string {
	_ = tab
	_ = ctx
	return ""
}

func settingsRootPromptLocale(tab settingsRootTab, locale i18n.Locale) string {
	if tab == settingsRootTabProject {
		return localizeText(locale, i18n.KeySettingsRootPromptProject, "Settings > Project > ")
	}
	return localizeText(locale, i18n.KeySettingsRootPromptGlobal, "Settings > ")
}

// settingsRootTabFromResultWithCurrent resolves which tab the popup should
// show next. Alt-Shift-Left and Alt-Shift-Right cycle between Global and
// Project, while the legacy Ctrl-G / Ctrl-P bindings remain as direct
// selectors so muscle memory does not regress. Primary-button chip clicks
// resolve through the Value sentinels emitted by the chip strip so click
// and chord follow the same tab-resolution path.
func settingsRootTabFromResultWithCurrent(result intpickercompat.Result, current settingsRootTab) (settingsRootTab, bool) {
	switch strings.TrimSpace(result.Key) {
	case "ctrl-g":
		return settingsRootTabGlobal, true
	case "ctrl-p":
		return settingsRootTabProject, true
	case "alt-shift-left", "alt-shift-right":
		return settingsRootTabToggle(current), true
	}
	switch strings.TrimSpace(result.Value) {
	case settingsRootTabGlobalValue:
		return settingsRootTabGlobal, true
	case settingsRootTabProjectValue:
		return settingsRootTabProject, true
	}
	return "", false
}

func settingsRootTabToggle(current settingsRootTab) settingsRootTab {
	if current == settingsRootTabProject {
		return settingsRootTabGlobal
	}
	return settingsRootTabProject
}

func (c *settingsCommand) rootEntries() []intpickercompat.Entry {
	return c.rootEntriesForAxis(settingsAxisGlobal)
}

func (c *settingsCommand) rootEntriesForTabLocale(tab settingsRootTab, locale i18n.Locale) []intpickercompat.Entry {
	if tab == settingsRootTabProject {
		return c.projectTabEntries()
	}
	return c.rootEntriesForAxisLocale(settingsAxisGlobal, locale)
}

func (c *settingsCommand) rootEntriesForAxis(axis SettingsAxis) []intpickercompat.Entry {
	return c.rootEntriesForAxisLocale(axis, settingsLocale())
}

func (c *settingsCommand) rootEntriesForAxisLocale(axis SettingsAxis, locale i18n.Locale) []intpickercompat.Entry {
	all := []intpickercompat.Entry{
		{
			Label: settingsRootLabelLocale(locale, settingsGlyphOpen, "Project Picker", "project roots, workdirs, and pins"),
			Value: settingsSectionProject,
		},
		{
			Label: settingsRootLabelLocale(locale, settingsGlyphOpen, "AI Settings", "default split mode, enabled agents"),
			Value: settingsSectionAI,
		},
		{
			Label: settingsRootLabelLocale(locale, settingsGlyphOpen, "Notifications", "desktop mode, delivery sources, and hook quiet policy"),
			Value: settingsSectionNotifications,
		},
		{
			Label: settingsRootLabelLocale(locale, settingsGlyphOpen, "Hooks", "global lifecycle hook paths"),
			Value: settingsSectionGlobalHooks,
		},
		{
			Label: settingsRootLabelLocale(locale, settingsGlyphOpen, "Appearance", "theme font status and icon decoration"),
			Value: settingsSectionStatusbar,
		},
		{
			Label: settingsRootLabelLocale(locale, settingsGlyphOpen, "Theme", "global preset, color tokens, and font hints"),
			Value: settingsSectionGlobalTheme,
		},
		{
			Label: c.sessionStateSettingsRootLabelLocale(locale),
			Value: settingsSectionSessionState,
		},
		{
			Label: settingsRootLabelLocale(locale, settingsGlyphOpen, "Keybindings", "edit tmux plain and prefix chords"),
			Value: settingsSectionKeybindings,
		},
		{
			Label: settingsRootLabelLocale(locale, settingsGlyphOpen, "Labs", "experimental features"),
			Value: settingsSectionLabs,
		},
		{
			Label: settingsRootLabelLocale(locale, settingsGlyphOpen, "About", "version, updates, key setup"),
			Value: settingsSectionAbout,
		},
	}
	entries := make([]intpickercompat.Entry, 0, len(all))
	for _, entry := range all {
		meta, ok := settingsEntryMetaForValue(entry.Value)
		if !ok || meta.Axis&axis == 0 {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

// settingsRootColorOpen/Dim default to fallback literals; applyNativeUITheme
// repoints them for an explicit global theme. See theme_render_native.go.
var (
	settingsRootColorOpen = theme.ANSIAccentActionStrongStart
	settingsRootColorDim  = theme.ANSITextMutedStart
)

func settingsRootLabel(glyph, name, description string) string {
	return settingsRootLabelLocale(settingsLocale(), glyph, name, description)
}

func settingsRootLabelLocale(locale i18n.Locale, glyph, name, description string) string {
	return settingsRootLabelWithColorLocale(locale, glyph, settingsRootColorOpen, name, description)
}

func settingsRootLabelDim(name, description string) string {
	return settingsRootLabelWithColorLocale(settingsLocale(), settingsGlyphInfo, settingsRootColorDim, name, description)
}

func settingsRootLabelWithColorLocale(locale i18n.Locale, glyph, color, name, description string) string {
	name = settingsCatalogTextLocale(locale, name)
	description = settingsCatalogTextLocale(locale, description)
	var b strings.Builder
	if glyph == "" {
		b.WriteString(" ")
	} else {
		b.WriteString(glyph)
	}
	b.WriteString("  ")
	b.WriteString(color)
	b.WriteString(padRight(name, settingsLabelNameWidth))
	b.WriteString(settingsColorReset)
	if description != "" {
		b.WriteString("  ")
		b.WriteString(settingsRootColorDim)
		b.WriteString(description)
		b.WriteString(settingsColorReset)
	}
	return b.String()
}

func (c *settingsCommand) sessionStateSettingsRootLabelLocale(locale i18n.Locale) string {
	autosave := c.currentSessionStateAutosave()
	interval := c.currentSessionStateAutosaveInterval()
	desc := fmt.Sprintf("autosave %s, interval %s", autosave.Mode, formatSessionStateAutosaveInterval(interval.Duration))
	return settingsRootLabelLocale(locale, settingsGlyphOpen, "Session State", desc)
}

func (c *settingsCommand) projectSessionStateSettingsRootLabel(ctx settingsProjectContext) string {
	identity := c.projectSessionStateIdentity(ctx)
	desc := "disabled - no project context"
	if identity.Err == nil {
		desc = identity.Session
	}
	return settingsRootLabel(settingsGlyphOpen, "Session State", desc)
}
