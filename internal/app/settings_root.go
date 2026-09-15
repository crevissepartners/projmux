package app

import (
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
		UI: "settings",
		// The visible root rows stay first and keep their order; the global
		// result rows are appended behind them and are invisible until the user
		// types, so Enter's first surviving row at the root stays deterministic.
		Entries:    append(c.rootEntriesForTabLocale(tab, locale), settingsRootSearchResults(tab, ctx, locale)...),
		Title:      localizeText(locale, i18n.KeySettingsRootTitle, "Settings"),
		TitleChips: settingsRootTabChipsLocale(tab, ctx.hasProject(), locale),
		Prompt:     settingsRootPromptLocale(tab, locale),
		Header:     settingsRootContextHeader(tab, ctx),
		Footer:     localizeText(locale, i18n.KeySettingsRootFooter, "Open rows or click a scope chip to switch tabs."),
		ExpectKeys: []string{"enter", "ctrl-g", "ctrl-p", "alt-shift-left", "alt-shift-right"},
		Bindings:   c.settingsCloseBindings(),
	}
}

// settingsRootSearchResults gates the Project tab's results on an actual
// project context: without one the Project tab renders the passive guidance row
// alone, and a result row would name a View the tab cannot open.
func settingsRootSearchResults(tab settingsRootTab, ctx settingsProjectContext, locale i18n.Locale) []intpickercompat.Entry {
	if tab == settingsRootTabProject && !ctx.hasProject() {
		return nil
	}
	return settingsRootResultEntries(tab, locale)
}

func settingsRootTabChipsLocale(active settingsRootTab, hasProject bool, locale i18n.Locale) []projmuxpicker.Chip {
	return []projmuxpicker.Chip{
		{
			Label:      settingsNavLabelLocale(locale, settingsNavScopeGlobal),
			Active:     active == settingsRootTabGlobal,
			ClickValue: settingsRootTabGlobalValue,
		},
		{
			Label:      settingsNavLabelLocale(locale, settingsNavScopeProject),
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

// settingsRootDescriptions is the one-line ownership summary each Global root
// row renders. The keys are navigation node IDs, so a root that exists in the
// tree without a description (or the other way round) is a test failure rather
// than a silently blank row.
var settingsRootDescriptions = map[string]string{
	settingsNavProjects:      "discovery roots, pinned Projects, sidebar policy",
	settingsNavAI:            "default launch target, enabled Providers, Agent Resume Picker",
	settingsNavNotifications: "desktop delivery, Provider Integrations, Agent event behavior",
	settingsNavAutomation:    "Projmux lifecycle scripts and project automation policy",
	settingsNavAppearance:    "Theme, Status Bar, language, Agent attention badge",
	settingsNavKeybindings:   "keys by surface and action category",
	settingsNavAbout:         "version, updates, welcome, and quit",
}

// rootEntriesForAxisLocale renders the Global root from the navigation
// catalog. The row order, labels and destinations come from settingsNodeCatalog
// rather than from a second hand-maintained list, so the visible root cannot
// drift from the tree the golden freezes.
func (c *settingsCommand) rootEntriesForAxisLocale(axis SettingsAxis, locale i18n.Locale) []intpickercompat.Entry {
	children := settingsNavChildren(settingsNavScopeGlobal)
	entries := make([]intpickercompat.Entry, 0, len(children))
	for _, node := range children {
		meta, ok := settingsEntryMetaForValue(node.Value)
		if !ok || meta.Axis&axis == 0 {
			continue
		}
		// Snapshots carries live autosave state in its description, so it
		// keeps its own label builder; every other root is a static summary.
		if node.ID == settingsNavSnapshots {
			entries = append(entries, settingsRootEntryWithSearchKey(intpickercompat.Entry{
				Label: c.sessionStateSettingsRootLabelLocale(locale),
				Value: node.Value,
			}))
			continue
		}
		entries = append(entries, settingsRootEntryWithSearchKey(intpickercompat.Entry{
			Label: settingsNodeRootLabelLocale(locale, node.ID, settingsGlyphOpen, settingsRootDescriptions[node.ID]),
			Value: node.Value,
		}))
	}
	return entries
}

// settingsRootEntryWithSearchKey gives a root row an explicit SearchKey equal
// to its own rendered label with the escapes removed. The appended result rows
// are keyed, which puts the whole root list into the picker's keyed
// (input-order) mode; a root row without a key would then be matched on its
// value instead of on the text the user reads. Giving every row the key it
// already renders keeps the root's order and its matches deterministic, and the
// Phase 0 rendered-label join sees the label already present and adds nothing.
func settingsRootEntryWithSearchKey(entry intpickercompat.Entry) intpickercompat.Entry {
	if strings.TrimSpace(entry.SearchKey) != "" {
		return entry
	}
	entry.SearchKey = stripSettingsLabelANSI(entry.Label)
	return entry
}

// settingsRootColorOpen/Dim default to fallback literals; applyNativeUITheme
// repoints them for an explicit global theme. See theme_render_native.go.
var (
	settingsRootColorOpen = theme.ANSIAccentActionStrongStart
	settingsRootColorDim  = theme.ANSITextMutedStart
)

func settingsRootLabelDim(name, description string) string {
	return settingsRootLabelWithColorLocale(settingsLocale(), settingsGlyphInfo, settingsRootColorDim, name, description)
}

func settingsRootLabelWithColorLocale(locale i18n.Locale, glyph, color, name, description string) string {
	name = settingsCatalogTextLocale(locale, name)
	return settingsResolvedRootLabelWithColorLocale(locale, glyph, color, name, description)
}

func settingsResolvedRootLabelWithColorLocale(locale i18n.Locale, glyph, color, name, description string) string {
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

func settingsNodeRootLabelLocale(locale i18n.Locale, id, glyph, description string) string {
	return settingsResolvedRootLabelWithColorLocale(locale, glyph, settingsRootColorOpen, settingsNavLabelLocale(locale, id), description)
}

func (c *settingsCommand) sessionStateSettingsRootLabelLocale(locale i18n.Locale) string {
	autosave := c.currentSessionStateAutosave()
	interval := c.currentSessionStateAutosaveInterval()
	var mode string
	if autosave.Mode.Enabled() {
		mode = localizeText(locale, "settings.text.state_on", "on")
	} else {
		mode = localizeText(locale, "settings.text.state_off", "off")
	}
	desc := strings.NewReplacer(
		"{mode}", mode,
		"{interval}", formatSessionStateAutosaveInterval(interval.Duration),
	).Replace(localizeText(locale, "settings.text.session_state_summary", "autosave {mode}, interval {interval}"))
	return settingsNodeRootLabelLocale(locale, settingsNavSnapshots, settingsGlyphOpen, desc)
}

func (c *settingsCommand) projectSessionStateSettingsRootLabel(ctx settingsProjectContext) string {
	identity := c.projectSessionStateIdentity(ctx)
	desc := "disabled - no project context"
	if identity.Err == nil {
		desc = identity.Session
	}
	return settingsNodeRootLabelLocale(c.locale(), settingsNavProjectSnapshots, settingsGlyphOpen, desc)
}
