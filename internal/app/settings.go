package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/aiprovider"
	"github.com/crevissepartners/projmux/internal/i18n"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/theme"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
	intrender "github.com/crevissepartners/projmux/internal/ui/render"
	"github.com/crevissepartners/projmux/internal/version"
)

// osStat is a package-level indirection so tests can stub filesystem checks.
var osStat = os.Stat

// osLstat mirrors osStat for symlink-aware checks (e.g. the hook maker has
// to distinguish a dotfiles-managed symlink from a regular legacy script).
var osLstat = os.Lstat

type settingsCommand struct {
	ai                  *aiCommand
	switcher            *switchCommand
	update              *updateCommand
	quit                *quitCommand
	runner              intpickercompat.Runner
	nativePicker        intpicker.Runner
	homeDir             func() (string, error)
	lookupEnv           func(string) string
	runCommand          func(name string, args ...string) error
	runOutput           func(name string, args ...string) ([]byte, error)
	tmuxRunner          tmuxRunner
	probeKeybinding     func(probeKey, time.Duration) (probeResult, error)
	aiNotifyDiagnostics func() []doctorAINotifyIntegration
}

var errSettingsClosed = errors.New("settings closed")

type settingsRootTab string

const (
	settingsRootTabGlobal  settingsRootTab = "global"
	settingsRootTabProject settingsRootTab = "project"
)

type SettingsAxis uint8

const (
	settingsAxisGlobal SettingsAxis = 1 << iota
	settingsAxisProject
	settingsAxisBoth = settingsAxisGlobal | settingsAxisProject
)

type settingsEntryMeta struct {
	Name string
	Axis SettingsAxis
}

var settingsEntryCatalog = map[string]settingsEntryMeta{
	settingsBackValue:                  {Name: "Back", Axis: settingsAxisBoth},
	settingsNoopValue:                  {Name: "Info", Axis: settingsAxisBoth},
	settingsRootTabGlobalValue:         {Name: "Global Settings", Axis: settingsAxisBoth},
	settingsRootTabProjectValue:        {Name: "Project Settings", Axis: settingsAxisBoth},
	settingsSectionProject:             {Name: "Project Picker", Axis: settingsAxisGlobal},
	settingsSectionGlobalHooks:         {Name: "Hooks", Axis: settingsAxisGlobal},
	settingsSectionProjectHooks:        {Name: "Hooks", Axis: settingsAxisProject},
	settingsSectionProjectConfig:       {Name: "Project recipe", Axis: settingsAxisProject},
	settingsSectionProjectTrust:        {Name: "Trust", Axis: settingsAxisProject},
	settingsSectionEffectiveMerge:      {Name: "Effective merge view", Axis: settingsAxisProject},
	settingsSectionGlobalTheme:         {Name: "Theme", Axis: settingsAxisGlobal},
	settingsSectionProjectTheme:        {Name: "Theme override", Axis: settingsAxisProject},
	settingsSectionEffectiveTheme:      {Name: "Effective theme", Axis: settingsAxisProject},
	settingsSectionProjectSessionState: {Name: "Session State", Axis: settingsAxisProject},
	settingsSectionAI:                  {Name: "AI Settings", Axis: settingsAxisGlobal},
	settingsSectionNotifications:       {Name: "Notifications", Axis: settingsAxisGlobal},
	settingsSectionStatusbar:           {Name: "Appearance", Axis: settingsAxisGlobal},
	settingsSectionSessionState:        {Name: "Session State", Axis: settingsAxisGlobal},
	settingsSectionKeybindings:         {Name: "Keybindings", Axis: settingsAxisGlobal},
	settingsSectionLabs:                {Name: "Labs", Axis: settingsAxisGlobal},
	settingsSectionAbout:               {Name: "About", Axis: settingsAxisGlobal},
	settingsProjectAdd:                 {Name: "Add Project", Axis: settingsAxisGlobal},
	settingsProjectPins:                {Name: "Pinned Projects", Axis: settingsAxisGlobal},
	settingsProjectRootManage:          {Name: "Project Root", Axis: settingsAxisGlobal},
	settingsProjdirClear:               {Name: "Clear Project Root", Axis: settingsAxisGlobal},
	settingsProjdirSetCurrent:          {Name: "Use Current Project as Root", Axis: settingsAxisGlobal},
	settingsProjdirSetTyped:            {Name: "Set Project Root", Axis: settingsAxisGlobal},
	settingsWorkdirAdd:                 {Name: "Add Workdir", Axis: settingsAxisGlobal},
	settingsWorkdirList:                {Name: "Workdirs", Axis: settingsAxisGlobal},
	settingsWorkdirTyped:               {Name: "Type Workdir", Axis: settingsAxisGlobal},
	settingsKeybindingsDiagnostic:      {Name: "Keybinding Diagnostic", Axis: settingsAxisGlobal},
	settingsKeybindingsProbe:           {Name: "Keybinding Probe", Axis: settingsAxisGlobal},
	settingsKeybindingsInit:            {Name: "Keybinding Init", Axis: settingsAxisGlobal},
	settingsAIDefaultMode:              {Name: "Default split mode", Axis: settingsAxisGlobal},
	settingsAIEnabledAgents:            {Name: "Enabled agents", Axis: settingsAxisGlobal},
	settingsAINotifyDiagnostics:        {Name: "AI notify diagnostics", Axis: settingsAxisGlobal},
	settingsNotificationsDesktop:       {Name: "Desktop notification settings", Axis: settingsAxisGlobal},
	settingsNotificationsAIDedupe:      {Name: "AI notification dedupe", Axis: settingsAxisGlobal},
	settingsNotificationsDelivery:      {Name: "Delivery sources", Axis: settingsAxisGlobal},
	settingsNotificationsHookActions:   {Name: "Hook quiet policy", Axis: settingsAxisGlobal},
	settingsNotificationsQueue:         {Name: "In-app queue", Axis: settingsAxisGlobal},
	settingsNotificationsHookOverride:  {Name: "Notification hook override", Axis: settingsAxisGlobal},
	settingsAppearanceLanguage:         {Name: "Language / Locale", Axis: settingsAxisGlobal},
	settingsLabsProjectHooks:           {Name: "Project Hooks", Axis: settingsAxisGlobal},
	settingsSessionStateDelete:         {Name: "Delete session snapshot", Axis: settingsAxisGlobal},
	settingsLabKeybindings:             {Name: "Keybindings", Axis: settingsAxisGlobal},
	settingsUpdateApply:                {Name: "Update Now", Axis: settingsAxisGlobal},
	settingsUpdateCheck:                {Name: "Check Updates", Axis: settingsAxisGlobal},
	settingsWelcomeShow:                {Name: "Welcome", Axis: settingsAxisGlobal},
	settingsQuitOpen:                   {Name: "Quit projmux", Axis: settingsAxisGlobal},
}

var settingsEntryPrefixCatalog = []struct {
	prefix string
	meta   settingsEntryMeta
}{
	{settingsActionPrefixAI, settingsEntryMeta{Name: "AI Settings", Axis: settingsAxisGlobal}},
	{settingsActionPrefixAIEnabledAgent, settingsEntryMeta{Name: "Enabled agents", Axis: settingsAxisGlobal}},
	{settingsActionPrefixAINotifyDiagnostic, settingsEntryMeta{Name: "AI notify diagnostics", Axis: settingsAxisGlobal}},
	{settingsActionPrefixAIBadgeStyle, settingsEntryMeta{Name: "AI badge style", Axis: settingsAxisGlobal}},
	{settingsActionPrefixDesktopNotifyMode, settingsEntryMeta{Name: "Desktop notification mode", Axis: settingsAxisGlobal}},
	{settingsActionPrefixAINotifyDedupe, settingsEntryMeta{Name: "AI notification dedupe", Axis: settingsAxisGlobal}},
	{settingsActionPrefixAIHookProvider, settingsEntryMeta{Name: "Hook quiet policy", Axis: settingsAxisGlobal}},
	{settingsActionPrefixAIHookEvent, settingsEntryMeta{Name: "Hook quiet policy", Axis: settingsAxisGlobal}},
	{settingsActionPrefixAIHookSet, settingsEntryMeta{Name: "Hook quiet policy", Axis: settingsAxisGlobal}},
	{settingsActionPrefixHooks, settingsEntryMeta{Name: "Project hook policy", Axis: settingsAxisGlobal}},
	{settingsActionPrefixHookAdd, settingsEntryMeta{Name: "Hook maker - add", Axis: settingsAxisBoth}},
	{settingsActionPrefixHookEdit, settingsEntryMeta{Name: "Hook maker - edit", Axis: settingsAxisBoth}},
	{settingsActionPrefixHookRemove, settingsEntryMeta{Name: "Hook maker - remove", Axis: settingsAxisBoth}},
	{settingsActionPrefixHookView, settingsEntryMeta{Name: "Hook maker - view", Axis: settingsAxisBoth}},
	{settingsActionPrefixKeymap, settingsEntryMeta{Name: "Keybindings", Axis: settingsAxisGlobal}},
	{settingsActionPrefixLocale, settingsEntryMeta{Name: "Language / Locale", Axis: settingsAxisGlobal}},
	{settingsActionPrefixPicker, settingsEntryMeta{Name: "Picker backend", Axis: settingsAxisGlobal}},
	{settingsActionPrefixProjectConfig, settingsEntryMeta{Name: "Project recipe", Axis: settingsAxisProject}},
	{settingsActionPrefixWelcome, settingsEntryMeta{Name: "Welcome", Axis: settingsAxisGlobal}},
	{settingsActionPrefixTrust, settingsEntryMeta{Name: "Trust", Axis: settingsAxisProject}},
	{settingsActionPrefixProjdir, settingsEntryMeta{Name: "Project Root", Axis: settingsAxisGlobal}},
	{settingsActionPrefixSessionState, settingsEntryMeta{Name: "Session State", Axis: settingsAxisGlobal}},
	{settingsActionPrefixStatusbar, settingsEntryMeta{Name: "Appearance", Axis: settingsAxisGlobal}},
	{settingsActionPrefixSwitch, settingsEntryMeta{Name: "Pinned Projects", Axis: settingsAxisGlobal}},
	{settingsActionPrefixTheme, settingsEntryMeta{Name: "Theme", Axis: settingsAxisBoth}},
	{settingsActionPrefixUpdate, settingsEntryMeta{Name: "About", Axis: settingsAxisGlobal}},
	{settingsActionPrefixWorkdir, settingsEntryMeta{Name: "Workdirs", Axis: settingsAxisGlobal}},
}

func settingsEntryMetaForValue(value string) (settingsEntryMeta, bool) {
	if meta, ok := settingsEntryCatalog[value]; ok {
		return meta, true
	}
	for _, candidate := range settingsEntryPrefixCatalog {
		if strings.HasPrefix(value, candidate.prefix) {
			return candidate.meta, true
		}
	}
	return settingsEntryMeta{}, false
}

const (
	settingsBackValue                      = "__settings_back__"
	settingsNoopValue                      = "__settings_noop__"
	settingsRootTabGlobalValue             = "__settings_tab_global__"
	settingsRootTabProjectValue            = "__settings_tab_project__"
	settingsSectionAI                      = "section:ai"
	settingsSectionGlobalHooks             = "section:hooks-global"
	settingsSectionProjectHooks            = "section:hooks-project"
	settingsSectionProjectConfig           = "section:project-config"
	settingsSectionProjectTrust            = "section:project-trust"
	settingsSectionEffectiveMerge          = "section:effective-merge"
	settingsSectionGlobalTheme             = "section:theme-global"
	settingsSectionProjectTheme            = "section:theme-project"
	settingsSectionEffectiveTheme          = "section:theme-effective"
	settingsSectionProjectSessionState     = "section:project-sessionstate"
	settingsSectionKeybindings             = "section:keybindings"
	settingsSectionProject                 = "section:project-picker"
	settingsSectionNotifications           = "section:notifications"
	settingsSectionStatusbar               = "section:statusbar"
	settingsSectionSessionState            = "section:sessionstate"
	settingsSectionLabs                    = "section:labs"
	settingsSectionAbout                   = "section:about"
	settingsActionPrefixAI                 = "ai:"
	settingsActionPrefixAIEnabledAgent     = "ai-enabled-agent:"
	settingsActionPrefixAIBadgeStyle       = "ai-badge-style:"
	settingsActionPrefixAINotifyDiagnostic = "ai-notify:"
	settingsActionPrefixAINotifyCommand    = "ai-notify-command:"
	settingsActionPrefixAINotifyDedupe     = "ai-notify-dedupe:"
	settingsActionPrefixAIHookProvider     = "ai-hook-provider:"
	settingsActionPrefixAIHookEvent        = "ai-hook-event:"
	settingsActionPrefixAIHookSet          = "ai-hook-set:"
	settingsActionPrefixDesktopNotifyMode  = "desktop-notify-mode:"
	settingsActionPrefixHooks              = "project-hooks:"
	settingsActionPrefixKeymap             = "keymap:"
	settingsActionPrefixLocale             = "locale:"
	settingsActionPrefixPicker             = "picker-backend:"
	settingsActionPrefixProjectConfig      = "project-config:"
	settingsActionPrefixWelcome            = "welcome:"
	settingsActionPrefixTrust              = "trust:"
	settingsActionPrefixProjdir            = "projdir:"
	settingsActionPrefixSessionState       = "sessionstate:"
	settingsActionPrefixStatusbar          = "statusbar-decoration:"
	settingsActionPrefixSwitch             = "switch:"
	settingsActionPrefixTheme              = "theme:"
	settingsActionPrefixUpdate             = "update:"
	settingsActionPrefixWorkdir            = "workdir:"
	settingsActionPrefixQuit               = "quit:"
	settingsProjectAdd                     = "project:add"
	settingsProjectPins                    = "project:pins"
	settingsProjectRootManage              = "project-root:manage"
	settingsProjdirClear                   = "projdir:clear"
	settingsProjdirSetCurrent              = "projdir:set-current"
	settingsProjdirSetTyped                = "projdir:set-typed"
	settingsUpdateApply                    = "update:apply"
	settingsUpdateCheck                    = "update:check"
	settingsQuitOpen                       = "quit:open"
	settingsWorkdirAdd                     = "workdir:add"
	settingsWorkdirList                    = "workdir:list"
	settingsWorkdirTyped                   = "workdir:typed"
	settingsKeybindingsBindings            = "keybindings:bindings"
	settingsKeybindingsDiagnostic          = "keybindings:diagnostic"
	settingsKeybindingsProbe               = "keybindings:probe"
	settingsKeybindingsInit                = "keybindings:init"
	settingsAIDefaultMode                  = "ai-default-mode"
	settingsAIEnabledAgents                = "ai-enabled-agents"
	settingsAINotifyDiagnostics            = "ai-notify-diagnostics"
	settingsNotificationsDesktop           = "notifications:desktop"
	settingsNotificationsAIDedupe          = "notifications:ai-dedupe"
	settingsNotificationsDelivery          = "notifications:delivery"
	settingsNotificationsHookActions       = "notifications:hook-actions"
	settingsNotificationsQueue             = "notifications:queue"
	settingsNotificationsHookOverride      = "notifications:hook-override"
	settingsAppearanceLanguage             = "appearance:language"
	settingsLabsProjectHooks               = "labs:project-hooks"
	settingsLabKeybindings                 = "labs:keybindings"
	settingsSessionStateDelete             = "sessionstate:delete"
	settingsWelcomeShow                    = "welcome:show"
	settingsKeymapFieldPlain               = "plain"
	settingsKeymapFieldKeys                = "keys"
	settingsKeymapFieldPrefix              = "prefix"
)

func newSettingsCommand(ai *aiCommand, switcher *switchCommand, update *updateCommand, quit *quitCommand) *settingsCommand {
	return &settingsCommand{
		ai:           ai,
		switcher:     switcher,
		update:       update,
		quit:         quit,
		nativePicker: intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout},
		homeDir:      os.UserHomeDir,
		lookupEnv:    os.Getenv,
		runCommand: func(name string, args ...string) error {
			return exec.Command(name, args...).Run()
		},
		runOutput: func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).Output()
		},
		tmuxRunner: inttmux.ExecRunner{},
	}
}

func (c *settingsCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) != 0 {
		printSettingsUsage(stderr)
		return errors.New("settings does not accept positional arguments")
	}
	if c.nativePicker == nil {
		return errors.New("native picker is not configured")
	}

	tab := settingsRootTabGlobal
	for {
		result, err := c.runPicker(c.rootOptions(tab))
		if err != nil {
			if errors.Is(err, errSettingsClosed) {
				return nil
			}
			return err
		}
		if next, ok := settingsRootTabFromResultWithCurrent(result, tab); ok {
			ctx := c.resolveSettingsProjectContext()
			if next == settingsRootTabProject && !ctx.hasProject() {
				// Single-tab environment (no project context): the Project
				// chip renders disabled and Alt-Shift-Left/Alt-Shift-Right
				// (or a chip click) is a no-op. We stay on the current tab
				// rather than navigating into an empty Project scope.
				tab = settingsRootTabGlobal
				continue
			}
			tab = next
			continue
		}
		section := strings.TrimSpace(result.Value)
		if result.Key != "enter" || section == "" {
			return nil
		}
		if section == settingsNoopValue {
			continue
		}

		if err := c.runSection(section, stdout, stderr); err != nil {
			if errors.Is(err, errSettingsClosed) {
				return nil
			}
			return err
		}
	}
}

func (c *settingsCommand) runSection(section string, stdout, stderr io.Writer) error {
	if section == settingsSectionProject {
		return c.runProjectPickerSection(stdout, stderr)
	}
	if section == settingsSectionProjectHooks {
		return c.runProjectHooksSection(stdout, stderr)
	}
	if section == settingsSectionGlobalHooks {
		return c.runGlobalHooksSection(stdout, stderr)
	}
	if section == settingsSectionProjectConfig {
		return c.runProjectConfigSection(stdout, stderr)
	}
	if section == settingsSectionProjectTrust {
		return c.runProjectTrustSection(stdout, stderr)
	}
	if section == settingsSectionEffectiveMerge {
		return c.runEffectiveMergeSection(stdout, stderr)
	}
	if section == settingsSectionGlobalTheme {
		return c.runThemeLayerSection(themeLayerGlobal, stdout, stderr)
	}
	if section == settingsSectionProjectTheme {
		return c.runThemeLayerSection(themeLayerProject, stdout, stderr)
	}
	if section == settingsSectionEffectiveTheme {
		return c.runEffectiveThemeSection(stdout, stderr)
	}
	if section == settingsSectionProjectSessionState {
		return c.runProjectSessionStateSection(stdout, stderr)
	}
	if section == settingsSectionAI {
		return c.runAISection(stdout, stderr)
	}
	if section == settingsSectionNotifications {
		return c.runNotificationsSection(stdout, stderr)
	}
	if section == settingsSectionKeybindings {
		return c.runKeybindingsSection(stdout, stderr)
	}
	if section == settingsSectionSessionState {
		return c.runSessionStateSection(stdout, stderr)
	}
	if section == settingsSectionStatusbar {
		return c.runAppearanceSection(stdout, stderr)
	}
	if section == settingsSectionLabs {
		return c.runLabsSection(stdout, stderr)
	}

	for {
		options, err := c.sectionOptions(section)
		if err != nil {
			printSettingsUsage(stderr)
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
		if action == settingsBackValue {
			return nil
		}
		if action == settingsNoopValue {
			continue
		}
		if err := c.execute(action, stdout, stderr); err != nil {
			return err
		}
	}
}

func (c *settingsCommand) runPicker(options intpickercompat.Options) (intpickercompat.Result, error) {
	options = c.withSettingsScopeTabs(options)
	options = c.localizeSettingsOptions(options)
	if options.Theme == nil {
		if source, err := configRenderThemeSource(c.homeDir, c.lookupEnv, c.resolveSettingsProjectContext().Path); err == nil {
			options = source.pickerCompatOptions(options)
		}
	}
	result, err := runPickerOptionBackend(c.lookupEnv, c.nativePicker, c.runner, options)
	if err != nil {
		if isNoSelectionExit(err) {
			return intpickercompat.Result{}, errSettingsClosed
		}
		return intpickercompat.Result{}, fmt.Errorf("run settings picker: %w", err)
	}
	return result, nil
}

func (c *settingsCommand) localizeSettingsOptions(options intpickercompat.Options) intpickercompat.Options {
	locale := c.locale()
	options.Locale = locale
	options.Title = settingsCatalogTextLocale(locale, options.Title)
	options.Prompt = settingsCatalogTextLocale(locale, options.Prompt)
	options.Header = settingsCatalogTextLocale(locale, options.Header)
	options.Footer = settingsCatalogTextLocale(locale, options.Footer)
	for i := range options.TitleChips {
		options.TitleChips[i].Label = settingsCatalogTextLocale(locale, options.TitleChips[i].Label)
	}
	return options
}

func (c *settingsCommand) locale() i18n.Locale {
	return appLocale(c.homeDir, c.lookupEnv)
}

func (c *settingsCommand) backEntry() intpickercompat.Entry {
	return settingsBackEntryLocale(c.locale())
}

func (c *settingsCommand) rowLabel(glyph, color, name, description string) string {
	return settingsLabelLocale(c.locale(), glyph, color, name, description)
}

func (c *settingsCommand) rowLabelDim(name, description string) string {
	return settingsLabelDimLocale(c.locale(), name, description)
}

func (c *settingsCommand) rowLabelInfo(name, value, source string) string {
	return settingsLabelInfoLocale(c.locale(), name, value, source)
}

func (c *settingsCommand) withSettingsScopeTabs(options intpickercompat.Options) intpickercompat.Options {
	if !strings.HasPrefix(strings.TrimSpace(options.UI), "settings") || len(options.TitleChips) != 0 {
		return options
	}
	active := settingsRootTabGlobal
	if strings.HasPrefix(strings.TrimSpace(options.Prompt), "Settings > Project >") || strings.HasPrefix(strings.TrimSpace(options.UI), "settings-project") {
		active = settingsRootTabProject
	}
	options.TitleChips = settingsPassiveRootTabChipsLocale(active, c.resolveSettingsProjectContext().hasProject(), c.locale())
	return options
}

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

// settingsRootTabChips returns the chip strip rendered in the popup
// titlebar so the active scope reads as a real tab metaphor instead of an
// inline list entry. When no project context is available, the Project
// chip renders as disabled so the user still sees that the tab exists.
// ClickValue mirrors the sentinel emitted by Alt-Shift-Left/Right chord
// handling so a primary-button click on the chip resolves through the
// same tab-resolution path as the keyboard chord.
func settingsRootTabChips(active settingsRootTab, hasProject bool) []projmuxpicker.Chip {
	return settingsRootTabChipsLocale(active, hasProject, settingsLocaleFromEnv())
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

func settingsPassiveRootTabChips(active settingsRootTab, hasProject bool) []projmuxpicker.Chip {
	chips := settingsPassiveRootTabChipsLocale(active, hasProject, settingsLocaleFromEnv())
	return chips
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

func settingsRootPrompt(tab settingsRootTab) string {
	return settingsRootPromptLocale(tab, settingsLocaleFromEnv())
}

func settingsRootPromptLocale(tab settingsRootTab, locale i18n.Locale) string {
	if tab == settingsRootTabProject {
		return localizeText(locale, i18n.KeySettingsRootPromptProject, "Settings > Project > ")
	}
	return localizeText(locale, i18n.KeySettingsRootPromptGlobal, "Settings > ")
}

func settingsRootTabFromResult(result intpickercompat.Result) (settingsRootTab, bool) {
	return settingsRootTabFromResultWithCurrent(result, settingsRootTabGlobal)
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

func (c *settingsCommand) rootEntriesForTab(tab settingsRootTab) []intpickercompat.Entry {
	return c.rootEntriesForTabLocale(tab, settingsLocaleFromEnv())
}

func (c *settingsCommand) rootEntriesForTabLocale(tab settingsRootTab, locale i18n.Locale) []intpickercompat.Entry {
	if tab == settingsRootTabProject {
		return c.projectTabEntries()
	}
	return c.rootEntriesForAxisLocale(settingsAxisGlobal, locale)
}

func (c *settingsCommand) rootEntriesForAxis(axis SettingsAxis) []intpickercompat.Entry {
	return c.rootEntriesForAxisLocale(axis, settingsLocaleFromEnv())
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
			Label: settingsRootLabelLocale(locale, settingsGlyphOpen, "Notifications", "desktop mode, delivery sources, queue surfaces"),
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
			Label: settingsRootLabelLocale(locale, settingsGlyphOpen, "Labs", "experimental picker engine"),
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

const (
	settingsRootColorOpen = theme.ANSIAccentActionStrongStart
	settingsRootColorDim  = theme.ANSITextMutedStart
)

func settingsRootLabel(glyph, name, description string) string {
	return settingsRootLabelLocale(settingsLocaleFromEnv(), glyph, name, description)
}

func settingsRootLabelLocale(locale i18n.Locale, glyph, name, description string) string {
	return settingsRootLabelWithColorLocale(locale, glyph, settingsRootColorOpen, name, description)
}

func settingsRootLabelDim(name, description string) string {
	return settingsRootLabelWithColorLocale(settingsLocaleFromEnv(), settingsGlyphInfo, settingsRootColorDim, name, description)
}

func settingsRootLabelWithColor(glyph, color, name, description string) string {
	return settingsRootLabelWithColorLocale(settingsLocaleFromEnv(), glyph, color, name, description)
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

func (c *settingsCommand) sessionStateSettingsRootLabel() string {
	return c.sessionStateSettingsRootLabelLocale(settingsLocaleFromEnv())
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

type settingsProjectContext struct {
	Path   string
	Name   string
	Source string
}

func (ctx settingsProjectContext) hasProject() bool {
	return strings.TrimSpace(ctx.Path) != ""
}

func (c *settingsCommand) projectTabEntries() []intpickercompat.Entry {
	ctx := c.resolveSettingsProjectContext()
	if !ctx.hasProject() {
		// The titlebar chip strip already advertises the active Project
		// scope (and the popup header carries the "no project" hint), so
		// the picker entry list skips the redundant "Project context"
		// placeholder row that lived above the search bar in Phase 1/2.
		return []intpickercompat.Entry{
			{
				Label: settingsRootLabelDim("Trust", "disabled - no project context"),
				Value: settingsNoopValue,
			},
			{
				Label: settingsRootLabelDim("Hooks (project)", "disabled - no project context"),
				Value: settingsNoopValue,
			},
			{
				Label:     settingsRootLabelDim("Project recipe", "disabled - no project context"),
				Value:     settingsNoopValue,
				SearchKey: "Project recipe config.toml",
			},
			{
				Label: settingsRootLabelDim("Effective merge view", "disabled - no project context"),
				Value: settingsNoopValue,
			},
			{
				Label: settingsRootLabelDim("Theme override", "disabled - no project context"),
				Value: settingsNoopValue,
			},
			{
				Label: settingsRootLabelDim("Effective theme", "disabled - no project context"),
				Value: settingsNoopValue,
			},
		}
	}

	// The project context label is conveyed by the chip strip plus the
	// popup header — keep the picker entries focused on actionable rows.
	// The Trust row is rendered with state-aware tone (untrusted /
	// trusted / stale / absent) and routes Enter into the trust subsection
	// that decides whether to register, refresh, or untrust the
	// project-local .projmux/config.toml hash.
	return []intpickercompat.Entry{
		c.projectTrustEntry(ctx),
		{
			Label: settingsRootLabel(settingsGlyphOpen, "Hooks (project)", filepath.Join(ctx.Path, ".projmux")),
			Value: settingsSectionProjectHooks,
		},
		{
			Label:     settingsRootLabel(settingsGlyphOpen, "Project recipe", "declare env, kube, startup"),
			Value:     settingsSectionProjectConfig,
			SearchKey: "Project recipe config.toml project config env kube startup",
		},
		{
			Label: settingsRootLabel(settingsGlyphOpen, "Theme override", "project preset, colors, and inherit global fields"),
			Value: settingsSectionProjectTheme,
		},
		{
			Label: settingsRootLabel(settingsGlyphOpen, "Effective theme", "final resolved values with source labels"),
			Value: settingsSectionEffectiveTheme,
		},
		{
			Label: settingsRootLabel(settingsGlyphOpen, "Effective merge view", "global + project merge with source labels"),
			Value: settingsSectionEffectiveMerge,
		},
		{
			Label: c.projectSessionStateSettingsRootLabel(ctx),
			Value: settingsSectionProjectSessionState,
		},
	}
}

func (c *settingsCommand) resolveSettingsProjectContext() settingsProjectContext {
	if c.lookupEnv != nil {
		if raw := strings.TrimSpace(c.lookupEnv("PROJMUX_CWD")); raw != "" {
			return newSettingsProjectContext(filepath.Clean(raw), "PROJMUX_CWD env")
		}
	}
	if c.switcher == nil {
		return settingsProjectContext{}
	}

	currentPath, err := c.switcher.resolveWorkingDir()
	if err == nil && currentPath != "" {
		homeDir, _ := c.switcher.resolveHomeDir()
		if root := nearestSettingsProjectRoot(currentPath, homeDir); root != "" {
			return newSettingsProjectContext(root, "pane_current_path")
		}
		if target, err := c.switcher.resolveSwitchTargetNoMemoize(nil, "settings project context"); err == nil && settingsContextTargetMatches(currentPath, target) {
			return newSettingsProjectContext(target, "switch context")
		}
	}

	return settingsProjectContext{}
}

func newSettingsProjectContext(path, source string) settingsProjectContext {
	path = filepath.Clean(path)
	return settingsProjectContext{
		Path:   path,
		Name:   settingsProjectContextName(path),
		Source: source,
	}
}

func settingsProjectContextName(path string) string {
	name := filepath.Base(filepath.Clean(path))
	if name == "." || name == string(filepath.Separator) {
		return path
	}
	return name
}

func nearestSettingsProjectRoot(path, boundary string) string {
	path = filepath.Clean(path)
	boundary = filepath.Clean(strings.TrimSpace(boundary))
	for {
		if boundary != "" && path == boundary {
			return ""
		}
		if settingsProjectMarkerExists(filepath.Join(path, ".projmux")) || settingsProjectMarkerExists(filepath.Join(path, ".git")) {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return ""
		}
		path = parent
	}
}

func settingsProjectMarkerExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := osStat(path)
	return err == nil
}

func settingsContextTargetMatches(currentPath, target string) bool {
	target = filepath.Clean(strings.TrimSpace(target))
	currentPath = filepath.Clean(strings.TrimSpace(currentPath))
	if target == "" || target == switchSettingsSentinel || currentPath == "" {
		return false
	}
	return pathContains(target, currentPath)
}

func pathContains(base, path string) bool {
	if base == "" || path == "" {
		return false
	}
	base = filepath.Clean(base)
	path = filepath.Clean(path)
	if base == path {
		return true
	}
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (c *settingsCommand) sectionOptions(section string) (intpickercompat.Options, error) {
	switch section {
	case settingsSectionAI:
		return intpickercompat.Options{
			UI:         "settings-ai",
			Entries:    c.aiRootEntries(),
			Title:      "AI Settings",
			Prompt:     "Settings > AI Settings > ",
			Footer:     projmuxFooter("Enter: open  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionNotifications:
		return intpickercompat.Options{
			UI:         "settings-notifications",
			Entries:    c.notificationsEntries(),
			Title:      "Notifications - Delivery, desktop, and queue surfaces",
			Prompt:     "Settings > Notifications > ",
			Footer:     projmuxFooter("Enter: open  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionGlobalHooks:
		return intpickercompat.Options{
			UI:         "settings-hooks-global",
			Entries:    c.globalHookEntries(),
			Title:      "Hooks - Global lifecycle hook paths",
			Prompt:     "Settings > Hooks > ",
			Footer:     projmuxFooter("Enter: edit/add  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionProjectHooks:
		ctx := c.resolveSettingsProjectContext()
		return intpickercompat.Options{
			UI:         "settings-hooks-project",
			Entries:    c.projectHookEntries(ctx),
			Title:      "Hooks - Project lifecycle hook paths",
			Prompt:     "Settings > Project > Hooks > ",
			Footer:     projmuxFooter("Enter: edit/add  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionProject:
		return intpickercompat.Options{
			UI:         "settings-project-picker",
			Entries:    c.projectPickerEntries(),
			Title:      "Project Picker - Project roots, workdirs, and pinned projects",
			Prompt:     "Settings > Project Picker > ",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionStatusbar:
		ctx := c.resolveSettingsProjectContext()
		return intpickercompat.Options{
			UI:         "settings-statusbar",
			Entries:    c.statusbarEntries(),
			Title:      "Appearance - Theme font and icon decoration",
			TitleChips: settingsPassiveRootTabChipsLocale(settingsRootTabGlobal, ctx.hasProject(), c.locale()),
			Prompt:     "Settings > Appearance > ",
			Footer:     projmuxFooter("Enter: open  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionSessionState:
		return intpickercompat.Options{
			UI:         "settings-sessionstate",
			Entries:    c.sessionStateEntries(),
			Title:      "Session State - Restore and autosave controls",
			Prompt:     "Settings > Session State > ",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionProjectSessionState:
		return intpickercompat.Options{
			UI:         "settings-project-sessionstate",
			Entries:    c.projectSessionStateEntries(),
			Title:      c.projectSessionStateTitle(),
			Prompt:     "Settings > Project > Session State > ",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionKeybindings:
		return c.keybindingsOptions(settingsKeybindingsBindings), nil
	case settingsSectionLabs:
		return intpickercompat.Options{
			UI:         "settings-labs",
			Entries:    c.labsEntries(),
			Title:      "Labs - Experimental features",
			Prompt:     "Settings > Labs > ",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	case settingsSectionAbout:
		return intpickercompat.Options{
			UI:         "settings-about",
			Entries:    c.aboutEntries(),
			Title:      "About - Version, updates, key setup",
			Prompt:     "Settings > About > ",
			Footer:     projmuxFooter("Enter: action  |  Back row: parent "),
			ExpectKeys: []string{"enter"},
			Bindings:   c.settingsCloseBindings(),
		}, nil
	default:
		return intpickercompat.Options{}, fmt.Errorf("unknown settings section: %s", section)
	}
}

func (c *settingsCommand) runProjectPickerSection(stdout, stderr io.Writer) error {
	for {
		options, err := c.sectionOptions(settingsSectionProject)
		if err != nil {
			printSettingsUsage(stderr)
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
		case action == settingsProjectAdd:
			if err := c.runAddProject(stdout, stderr); err != nil {
				return err
			}
		case action == settingsProjectPins:
			if err := c.runPinnedProjects(stdout, stderr); err != nil {
				return err
			}
		case action == settingsProjectRootManage:
			if err := c.runProjectRootSettings(stdout, stderr); err != nil {
				return err
			}
		case action == settingsWorkdirAdd:
			if err := c.runAddWorkdir(stdout, stderr); err != nil {
				return err
			}
		case action == settingsWorkdirList:
			if err := c.runWorkdirsList(stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixSwitch):
			if err := c.execute(action, stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixProjdir):
			if err := c.execute(action, stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixWorkdir):
			if err := c.execute(action, stdout, stderr); err != nil {
				return err
			}
		default:
			printSettingsUsage(stderr)
			return fmt.Errorf("unknown project picker settings action: %s", action)
		}
	}
}

func (c *settingsCommand) runAddProject(stdout, stderr io.Writer) error {
	if c.switcher == nil {
		return errors.New("project picker settings are not configured")
	}

	entries, err := c.switcher.filesystemPinEntries()
	if err != nil {
		return err
	}
	locale := appLocale(c.homeDir, c.lookupEnv)
	entries = append([]intpickercompat.Entry{settingsBackEntryLocale(locale)}, entries...)

	result, err := c.runPicker(intpickercompat.Options{
		UI:         "settings-project-add",
		Entries:    entries,
		Title:      "Add Project - Choose a filesystem directory",
		Prompt:     "Settings > Project Picker > Add Project > ",
		Footer:     projmuxFooter("Enter: add  |  Back row: parent "),
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
	return c.execute(action, stdout, stderr)
}

func (c *settingsCommand) runProjectRootSettings(stdout, stderr io.Writer) error {
	for {
		entries, err := c.projectRootEntries()
		if err != nil {
			return err
		}

		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-project-root",
			Entries:    entries,
			Title:      "Project Root - Effective and saved root",
			Prompt:     "Settings > Project Picker > Project Root > ",
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
		if action == settingsBackValue {
			return nil
		}
		if action == settingsNoopValue {
			continue
		}
		if action == settingsProjdirSetTyped {
			if err := c.runSetProjectRootTyped(stdout, stderr); err != nil {
				return err
			}
			continue
		}
		if err := c.execute(action, stdout, stderr); err != nil {
			return err
		}
	}
}

func (c *settingsCommand) runSetProjectRootTyped(stdout, stderr io.Writer) error {
	if c.switcher == nil {
		return errors.New("project root settings are not configured")
	}

	initialQuery := c.projectRootTypedInitialQuery()
	result, err := c.runPicker(intpickercompat.Options{
		UI:           "settings-project-root-typed",
		Entries:      nil,
		AcceptQuery:  true,
		InitialQuery: initialQuery,
		Title:        "Set Project Root - Type one absolute primary root path",
		Prompt:       "Type project root path > ",
		Footer:       projmuxFooter("Enter: save "),
		ExpectKeys:   []string{"enter"},
		Bindings:     c.settingsCloseBindings(),
	})
	if err != nil {
		return err
	}

	typed := strings.TrimSpace(result.Query)
	if typed == "" {
		return nil
	}

	expanded, err := c.expandTypedPath(typed, "project root")
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return nil
	}
	if !filepath.IsAbs(expanded) {
		fmt.Fprintf(stderr, "project root must be an absolute path: %s\n", typed)
		return nil
	}
	return c.switcher.saveSavedProjdir(expanded, stdout)
}

func (c *settingsCommand) projectRootTypedInitialQuery() string {
	if c.switcher == nil {
		return ""
	}
	value, _, err := c.switcher.currentProjdirInfo()
	if err == nil && strings.TrimSpace(value) != "" {
		return value
	}
	homeDir, err := c.switcher.resolveHomeDir()
	if err != nil {
		return ""
	}
	return homeDir
}

func (c *settingsCommand) runAddWorkdir(stdout, stderr io.Writer) error {
	if c.switcher == nil {
		return errors.New("project picker settings are not configured")
	}

	entries, err := c.switcher.filesystemWorkdirEntries()
	if err != nil {
		return err
	}
	locale := appLocale(c.homeDir, c.lookupEnv)
	entries = append([]intpickercompat.Entry{
		settingsBackEntryLocale(locale),
		settingsWorkdirTypedEntryLocale(locale),
	}, entries...)

	result, err := c.runPicker(intpickercompat.Options{
		UI:         "settings-workdir-add",
		Entries:    entries,
		Title:      "Add Workdir - Choose or type a directory to scan",
		Prompt:     "Settings > Project Picker > Add Workdir > ",
		Footer:     projmuxFooter("Enter: add  |  Back row: parent "),
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
	if action == settingsWorkdirTyped {
		return c.runAddWorkdirTyped(stdout, stderr)
	}
	return c.execute(action, stdout, stderr)
}

// settingsWorkdirTypedEntry surfaces the "Type path manually..." row that
// bypasses the filesystem scan and lets the user type an absolute path
// directly. Useful for heavy WSL mounts (/mnt/c/Users/...), large NFS, etc.
func settingsWorkdirTypedEntry() intpickercompat.Entry {
	return settingsWorkdirTypedEntryLocale(settingsLocaleFromEnv())
}

func settingsWorkdirTypedEntryLocale(locale i18n.Locale) intpickercompat.Entry {
	return intpickercompat.Entry{
		Label: settingsLabelLocale(locale, settingsGlyphType, settingsColorType, "Type path manually...", "skip filesystem scan"),
		Value: settingsWorkdirTyped,
	}
}

// runAddWorkdirTyped opens a typed-entry picker that surfaces the user-typed
// query as the workdir path, skipping the filesystem scan. Empty input is
// treated as a quiet close. Validation: must be an absolute path; "~" is
// expanded via the home resolver. A failing os.Stat is logged as a warning
// but does not block the add (WSL mounts may be temporarily unmounted).
func (c *settingsCommand) runAddWorkdirTyped(stdout, stderr io.Writer) error {
	if c.switcher == nil {
		return errors.New("project picker settings are not configured")
	}

	result, err := c.runPicker(intpickercompat.Options{
		UI:          "settings-workdir-typed",
		Entries:     nil,
		AcceptQuery: true,
		Title:       "Type Workdir - Absolute path",
		Prompt:      "Type workdir path > ",
		Footer:      projmuxFooter("Enter: add "),
		ExpectKeys:  []string{"enter"},
		Bindings:    c.settingsCloseBindings(),
	})
	if err != nil {
		return err
	}

	typed := strings.TrimSpace(result.Query)
	if typed == "" {
		// Empty input: treat as a quiet close, no error.
		return nil
	}

	expanded, err := c.expandTypedWorkdir(typed)
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return nil
	}

	if !filepath.IsAbs(expanded) {
		fmt.Fprintf(stderr, "workdir must be an absolute path: %s\n", typed)
		return nil
	}

	if info, statErr := osStat(expanded); statErr != nil {
		fmt.Fprintf(stderr, "warning: cannot stat workdir (continuing): %s: %v\n", expanded, statErr)
	} else if !info.IsDir() {
		fmt.Fprintf(stderr, "warning: workdir is not a directory (continuing): %s\n", expanded)
	}

	return c.switcher.addWorkdir(expanded, stdout)
}

// expandTypedWorkdir trims and home-expands a typed workdir path. The home
// expansion mirrors how the typed flow's UX hint advertises "~" support.
func (c *settingsCommand) expandTypedWorkdir(typed string) (string, error) {
	return c.expandTypedPath(typed, "workdir")
}

func (c *settingsCommand) expandTypedPath(typed, label string) (string, error) {
	typed = strings.TrimSpace(typed)
	if typed == "" {
		return "", fmt.Errorf("%s path is empty", label)
	}
	if typed == "~" || strings.HasPrefix(typed, "~/") {
		homeDir, err := c.switcher.resolveHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory for %q: %w", typed, err)
		}
		if typed == "~" {
			return homeDir, nil
		}
		return filepath.Join(homeDir, strings.TrimPrefix(typed, "~/")), nil
	}
	return typed, nil
}

func (c *settingsCommand) runWorkdirsList(stdout, stderr io.Writer) error {
	for {
		entries, err := c.workdirListEntries()
		if err != nil {
			return err
		}

		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-workdirs",
			Entries:    entries,
			Title:      "Workdirs - Saved and inherited scan roots",
			Prompt:     "Settings > Project Picker > Workdirs > ",
			Footer:     projmuxFooter("Enter: open/add/remove  |  Back row: parent "),
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
		if action == settingsWorkdirAdd {
			if err := c.runAddWorkdir(stdout, stderr); err != nil {
				return err
			}
			continue
		}
		if err := c.execute(action, stdout, stderr); err != nil {
			return err
		}
	}
}

func (c *settingsCommand) workdirListEntries() ([]intpickercompat.Entry, error) {
	locale := appLocale(c.homeDir, c.lookupEnv)
	entries := []intpickercompat.Entry{settingsBackEntryLocale(locale)}
	if c.switcher == nil {
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Saved workdirs", "unavailable"),
			Value: settingsNoopValue,
		}), nil
	}

	saved, err := c.switcher.loadSavedWorkdirs()
	if err != nil {
		return nil, err
	}

	if len(saved) == 0 {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Saved workdirs", "(none)", "~/.config/projmux/workdirs"),
			Value: settingsNoopValue,
		})
	} else {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Saved workdirs", strconv.Itoa(len(saved)), "~/.config/projmux/workdirs"),
			Value: settingsNoopValue,
		})
		for _, dir := range saved {
			entries = append(entries, intpickercompat.Entry{
				Label: settingsLabelLocale(locale, settingsGlyphRemove, settingsColorRemove, "Remove", dir+"  (saved)"),
				Value: settingsActionPrefixWorkdir + "remove:" + dir,
			})
		}
	}

	for _, src := range c.switcher.envWorkdirSources() {
		if strings.TrimSpace(src.Value) == "" {
			continue
		}
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, src.Name, src.Value, "env, read-only"),
			Value: settingsNoopValue,
		})
	}
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabelLocale(locale, settingsGlyphAdd, settingsColorAdd, "Add Workdir...", "append a directory to the saved workdirs list"),
		Value: settingsWorkdirAdd,
	})
	return entries, nil
}

func (c *settingsCommand) runPinnedProjects(stdout, stderr io.Writer) error {
	for {
		entries, err := c.pinnedProjectEntries()
		if err != nil {
			return err
		}

		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-project-pins",
			Entries:    entries,
			Title:      "Pinned Projects - Add or remove pins",
			Prompt:     "Settings > Project Picker > Pinned Projects > ",
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
		if action == settingsBackValue {
			return nil
		}
		if action == settingsNoopValue {
			continue
		}
		if action == settingsProjectAdd {
			if err := c.runAddProject(stdout, stderr); err != nil {
				return err
			}
			continue
		}
		if err := c.execute(action, stdout, stderr); err != nil {
			return err
		}
	}
}

func (c *settingsCommand) projectPickerEntries() []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	entries := []intpickercompat.Entry{
		settingsBackEntryLocale(locale),
	}

	entries = append(entries, c.projectRootEntryLocale(locale))
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Workdirs", "add or remove scan roots"),
		Value: settingsWorkdirList,
	})
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Pinned Projects", "add or remove pins"),
		Value: settingsProjectPins,
	})
	return entries
}

// projectRootEntry renders the resolved primary root with its source label.
// Opening it manages the saved project root; rendering never memoizes env state.
func (c *settingsCommand) projectRootEntry() intpickercompat.Entry {
	return c.projectRootEntryLocale(appLocale(c.homeDir, c.lookupEnv))
}

func (c *settingsCommand) projectRootEntryLocale(locale i18n.Locale) intpickercompat.Entry {
	if c.switcher == nil {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Project Root", "unavailable"),
			Value: settingsNoopValue,
		}
	}
	value, source, err := c.switcher.currentProjdirInfo()
	if err != nil || value == "" {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Project Root", "not configured"),
			Value: settingsProjectRootManage,
		}
	}
	return intpickercompat.Entry{
		Label: settingsLabelInfoLocale(locale, "Project Root", value, source),
		Value: settingsProjectRootManage,
	}
}

func (c *settingsCommand) projectRootHintEntry() intpickercompat.Entry {
	return c.projectRootHintEntryLocale(settingsLocaleFromEnv())
}

func (c *settingsCommand) projectRootHintEntryLocale(locale i18n.Locale) intpickercompat.Entry {
	// Keep the entire hint in one dim run so search substrings such as
	// "Set PROJMUX_PROJDIR" stay contiguous in the rendered label.
	return intpickercompat.Entry{
		Label: "  " + settingsColorDim + settingsCatalogTextLocale(locale, "Project Root is the primary root. Workdirs are extra search roots. Set PROJMUX_PROJDIR, @projmux_projdir, or the saved ~/.config/projmux/projdir value.") + settingsColorReset,
		Value: settingsNoopValue,
	}
}

func (c *settingsCommand) projectRootEntries() ([]intpickercompat.Entry, error) {
	locale := appLocale(c.homeDir, c.lookupEnv)
	entries := []intpickercompat.Entry{settingsBackEntryLocale(locale)}
	if c.switcher == nil {
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Project Root", "unavailable"),
			Value: settingsNoopValue,
		}), nil
	}

	info, err := c.switcher.projdirSettingsInfo()
	if err != nil {
		return nil, err
	}

	if info.EffectiveValue == "" {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Effective Project Root", "not configured", "no env, tmux option, or saved value"),
			Value: settingsNoopValue,
		})
	} else {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Effective Project Root", info.EffectiveValue, info.EffectiveSource),
			Value: settingsNoopValue,
		})
	}

	switch {
	case info.SavedValue == "":
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Saved Project Root", "not set", "~/.config/projmux/projdir"),
			Value: settingsNoopValue,
		})
	case info.EffectiveSource == projdirSourceSaved:
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Saved Project Root", info.SavedValue, "active"),
			Value: settingsNoopValue,
		})
	case info.EffectiveSource == projdirSourceUnresolved:
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Saved Project Root", info.SavedValue, "saved"),
			Value: settingsNoopValue,
		})
	default:
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Saved Project Root", info.SavedValue, "shadowed by "+info.EffectiveSource),
			Value: settingsNoopValue,
		})
	}

	entries = append(entries,
		intpickercompat.Entry{
			Label: settingsLabelLocale(locale, settingsGlyphAdd, settingsColorAdd, "Set Project Root...", "save one primary root path directly"),
			Value: settingsProjdirSetTyped,
		},
		c.setCurrentProjectRootEntryLocale(locale),
		intpickercompat.Entry{
			Label: settingsLabelLocale(locale, settingsGlyphRemove, settingsColorRemove, "Clear Saved Project Root", "remove ~/.config/projmux/projdir"),
			Value: settingsProjdirClear,
		},
		c.projectRootHintEntryLocale(locale),
		intpickercompat.Entry{
			Label: "  " + settingsColorDim + settingsCatalogTextLocale(locale, "Env PROJMUX_PROJDIR and tmux @projmux_projdir override the saved value until unset.") + settingsColorReset,
			Value: settingsNoopValue,
		},
	)
	return entries, nil
}

func (c *settingsCommand) setCurrentProjectRootEntry() intpickercompat.Entry {
	return c.setCurrentProjectRootEntryLocale(appLocale(c.homeDir, c.lookupEnv))
}

func (c *settingsCommand) setCurrentProjectRootEntryLocale(locale i18n.Locale) intpickercompat.Entry {
	if c.switcher == nil {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Use Current Project as Root", "unavailable"),
			Value: settingsNoopValue,
		}
	}

	homeDir, err := c.switcher.resolveHomeDir()
	if err != nil {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Use Current Project as Root", "home unavailable"),
			Value: settingsNoopValue,
		}
	}
	repoRoot, _, _ := c.switcher.currentProjdirInfo()
	currentTarget, err := c.switcher.resolveSwitchTargetNoMemoize(nil, "settings project root")
	if err != nil || currentTarget == "" || currentTarget == switchSettingsSentinel {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Use Current Project as Root", "no project context"),
			Value: settingsNoopValue,
		}
	}
	return intpickercompat.Entry{
		Label: settingsLabelLocale(locale, settingsGlyphAdd, settingsColorAdd, "Use Current Project as Root", intrender.PrettyPath(currentTarget, homeDir, repoRoot)),
		Value: settingsProjdirSetCurrent,
	}
}

func (c *settingsCommand) addCurrentProjectEntry() intpickercompat.Entry {
	return c.addCurrentProjectEntryLocale(appLocale(c.homeDir, c.lookupEnv))
}

func (c *settingsCommand) addCurrentProjectEntryLocale(locale i18n.Locale) intpickercompat.Entry {
	if c.switcher == nil {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Add Current Project", "unavailable"),
			Value: settingsNoopValue,
		}
	}

	pins, err := c.switcher.loadPins()
	if err != nil {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Add Current Project", "pins unavailable"),
			Value: settingsNoopValue,
		}
	}
	homeDir, err := c.switcher.resolveHomeDir()
	if err != nil {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Add Current Project", "home unavailable"),
			Value: settingsNoopValue,
		}
	}
	repoRoot := c.switcher.switchRepoRoot(homeDir)
	currentTarget, err := c.switcher.resolveSwitchTarget(nil, "settings project picker")
	if err != nil || currentTarget == "" || currentTarget == switchSettingsSentinel {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Add Current Project", "no project context"),
			Value: settingsNoopValue,
		}
	}
	if containsString(pins, currentTarget) {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Add Current Project", "already pinned  "+intrender.PrettyPath(currentTarget, homeDir, repoRoot)),
			Value: settingsNoopValue,
		}
	}
	return intpickercompat.Entry{
		Label: settingsLabelLocale(locale, settingsGlyphAdd, settingsColorAdd, "Add Current Project", intrender.PrettyPath(currentTarget, homeDir, repoRoot)),
		Value: settingsActionPrefixSwitch + "add:" + currentTarget,
	}
}

func (c *settingsCommand) pinnedProjectEntries() ([]intpickercompat.Entry, error) {
	locale := appLocale(c.homeDir, c.lookupEnv)
	entries := []intpickercompat.Entry{
		settingsBackEntryLocale(locale),
		{
			Label: settingsLabelLocale(locale, settingsGlyphAdd, settingsColorAdd, "Add Project...", "scan filesystem roots"),
			Value: settingsProjectAdd,
		},
		c.addCurrentProjectEntryLocale(locale),
	}
	if c.switcher == nil {
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "(no pinned projects)", ""),
			Value: settingsNoopValue,
		}), nil
	}

	pins, err := c.switcher.loadPins()
	if err != nil {
		return nil, err
	}
	homeDir, err := c.switcher.resolveHomeDir()
	if err != nil {
		return nil, err
	}
	repoRoot := c.switcher.switchRepoRoot(homeDir)

	if len(pins) == 0 {
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "(no pinned projects)", ""),
			Value: settingsNoopValue,
		}), nil
	}

	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabelLocale(locale, settingsGlyphRemove, settingsColorRemove, "Clear all pins", ""),
		Value: settingsActionPrefixSwitch + "clear",
	})
	for _, pin := range pins {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelLocale(locale, settingsGlyphRemove, settingsColorRemove, "Remove", intrender.PrettyPath(pin, homeDir, repoRoot)),
			Value: settingsActionPrefixSwitch + "pin:" + pin,
		})
	}
	return entries, nil
}

func (c *settingsCommand) runAISection(stdout, stderr io.Writer) error {
	for {
		options, err := c.sectionOptions(settingsSectionAI)
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
		case action == settingsAIDefaultMode:
			if err := c.runAIDefaultModeSection(stdout, stderr); err != nil {
				return err
			}
		case action == settingsAIEnabledAgents:
			if err := c.runAIEnabledAgentsSection(stdout, stderr); err != nil {
				return err
			}
		case action == settingsAINotifyDiagnostics:
			if err := c.runAINotifyDiagnosticsSection(stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixAI):
			if err := c.execute(action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown AI settings action: %s", action)
		}
	}
}

func (c *settingsCommand) runAIEnabledAgentsSection(stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-ai-enabled-agents",
			Entries:    c.aiEnabledAgentEntries(),
			Title:      "AI Settings - Enabled agents",
			Prompt:     "Settings > AI Settings > Enabled agents > ",
			Footer:     projmuxFooter("Enter: toggle  |  Back row: parent "),
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
		case strings.HasPrefix(action, settingsActionPrefixAIEnabledAgent):
			provider := strings.TrimPrefix(action, settingsActionPrefixAIEnabledAgent)
			if err := c.toggleAIEnabledAgent(provider); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown AI enabled agents action: %s", action)
		}
	}
}

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
			if err := c.setAINotifyDedupeSeconds(seconds, stdout); err != nil {
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
		return nil
	}
	return c.setAINotifyDedupeSeconds(seconds, stdout)
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
			if err := c.execute(action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown desktop notification settings action: %s", action)
		}
	}
}

func (c *settingsCommand) runAIDefaultModeSection(stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-ai-default-mode",
			Entries:    c.aiEntries(),
			Title:      "AI Settings - Default split mode",
			Prompt:     "Settings > AI Settings > Default split mode > ",
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
		case strings.HasPrefix(action, settingsActionPrefixAI):
			if err := c.execute(action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown AI default mode action: %s", action)
		}
	}
}

func (c *settingsCommand) aiRootEntries() []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	entries := []intpickercompat.Entry{settingsBackEntryLocale(locale)}
	if c.ai == nil {
		return entries
	}
	current := c.ai.getMode()
	defaultDesc := current
	if warning := c.aiDefaultModeDisabledWarning(); warning != "" {
		defaultDesc += " - " + warning
	}
	return append(entries,
		intpickercompat.Entry{
			Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Default split mode", defaultDesc),
			Value:     settingsAIDefaultMode,
			SearchKey: "default split mode claude codex antigravity shell selective",
		},
		intpickercompat.Entry{
			Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Enabled agents", c.aiEnabledAgentsSummary()),
			Value:     settingsAIEnabledAgents,
			SearchKey: "enabled agents claude codex antigravity",
		},
	)
}

func (c *settingsCommand) notificationsEntries() []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	notifyMode, notifySource := settingsDesktopNotifyResolver(c.homeDir, c.lookupEnv).resolveMode()
	dedupe := c.currentAINotifyDedupeSeconds()
	hookSummary := "not set"
	if c.lookupEnv != nil {
		if value := strings.TrimSpace(c.lookupEnv("PROJMUX_NOTIFY_HOOK")); value != "" {
			hookSummary = value
		}
	}
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
		{
			Label:     settingsLabelInfoLocale(locale, "In-app queue", "statusbar/sidebar", "consume pending notify rows"),
			Value:     settingsNotificationsQueue,
			SearchKey: "in app queue notify sidebar statusbar pending",
		},
		{
			Label:     settingsLabelInfoLocale(locale, "Notification hook override", hookSummary, "PROJMUX_NOTIFY_HOOK env"),
			Value:     settingsNotificationsHookOverride,
			SearchKey: "PROJMUX_NOTIFY_HOOK notification hook override env",
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
				if err := c.clearAIHookAction(provider, event, stdout); err != nil {
					return err
				}
				continue
			}
			if err := c.setAIHookAction(provider, event, next, stdout); err != nil {
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
		diagnostics = doctorAINotifyDiagnostics(c.ai)
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
		return "read-only doctor status"
	}
	return strings.Join(parts, ", ")
}

func (c *settingsCommand) aiNotifyDiagnosticEntries() []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	diagnostics := c.currentAINotifyDiagnostics()
	entries := make([]intpickercompat.Entry, 0, len(diagnostics)+1)
	entries = append(entries, settingsBackEntryLocale(locale))
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

func aiNotifyDiagnosticEntry(diag doctorAINotifyIntegration) intpickercompat.Entry {
	return aiNotifyDiagnosticEntryLocale(settingsLocaleFromEnv(), diag)
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

func aiNotifyDiagnosticDetailEntries(diag doctorAINotifyIntegration) []intpickercompat.Entry {
	return aiNotifyDiagnosticDetailEntriesLocale(settingsLocaleFromEnv(), diag)
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

func aiNotifyDiagnosticCommandEntry(diag doctorAINotifyIntegration, kind, label, command string) intpickercompat.Entry {
	return aiNotifyDiagnosticCommandEntryLocale(settingsLocaleFromEnv(), diag, kind, label, command)
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

func (c *settingsCommand) aiEntries() []intpickercompat.Entry {
	if c.ai == nil {
		return nil
	}

	current := c.ai.getMode()
	enabled := c.currentAIEnabledAgents()
	modes := []struct {
		mode string
		desc string
	}{
		{aiModeSelective, "show picker each time"},
	}
	for _, provider := range aiprovider.SettingsVisible() {
		modes = append(modes, struct {
			mode string
			desc string
		}{
			mode: string(provider.ID),
			desc: "always run " + provider.DisplayName + " split",
		})
	}
	modes = append(modes, struct {
		mode string
		desc string
	}{aiModeShell, "always open plain shell split"})

	entries := make([]intpickercompat.Entry, 0, len(modes)+1)
	entries = append(entries, c.backEntry())
	if warning := c.aiDefaultModeDisabledWarning(); warning != "" {
		entries = append(entries, intpickercompat.Entry{
			Label:     c.rowLabelDim("Warning", "saved Default split mode "+warning),
			Value:     settingsNoopValue,
			SearchKey: "default split mode disabled enabled agents",
		})
	}
	for _, item := range modes {
		if provider, ok := aiModeProvider(item.mode); ok && !aiEnabledAgentsContains(enabled, provider) {
			continue
		}
		glyph := settingsGlyphInactive
		color := settingsColorDim
		if item.mode == current {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
		}
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabel(glyph, color, item.mode, item.desc),
			Value: settingsActionPrefixAI + item.mode,
		})
	}
	return entries
}

func (c *settingsCommand) aiEnabledAgentEntries() []intpickercompat.Entry {
	enabled := c.currentAIEnabledAgents()
	entries := []intpickercompat.Entry{
		c.backEntry(),
		{
			Label: c.rowLabelInfo("Enabled agents", c.aiEnabledAgentsSummary(), "~/.config/projmux/"+config.AIEnabledAgentsFileName),
			Value: settingsNoopValue,
		},
	}
	if warning := c.aiDefaultModeDisabledWarning(); warning != "" {
		entries = append(entries, intpickercompat.Entry{
			Label: c.rowLabelDim("Warning", "saved Default split mode "+warning),
			Value: settingsNoopValue,
		})
	}

	for _, provider := range aiprovider.SettingsVisible() {
		configProvider := config.AIAgentProvider(provider.ID)
		on := aiEnabledAgentsContains(enabled, configProvider)
		glyph := settingsGlyphInactive
		color := settingsColorDim
		state := "disabled"
		if on {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
			state = "enabled"
		}
		entries = append(entries, intpickercompat.Entry{
			Label:     c.rowLabel(glyph, color, provider.DisplayName, state+" - "+provider.DisplayName+" split"),
			Value:     settingsActionPrefixAIEnabledAgent + string(provider.ID),
			SearchKey: strings.Join([]string{"enabled agents", provider.DisplayName, string(provider.ID), state}, " "),
		})
	}
	return entries
}

func (c *settingsCommand) currentAIEnabledAgents() []config.AIAgentProvider {
	paths, err := pickerBackendConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return append([]config.AIAgentProvider(nil), config.DefaultAIEnabledAgents...)
	}
	agents, err := config.LoadAIEnabledAgentsFile(paths.AIEnabledAgentsFile())
	if err != nil {
		return append([]config.AIAgentProvider(nil), config.DefaultAIEnabledAgents...)
	}
	return agents
}

func (c *settingsCommand) toggleAIEnabledAgent(provider string) error {
	normalized := config.NormalizeAIEnabledAgents([]string{provider})
	if len(normalized) != 1 {
		return fmt.Errorf("unknown AI agent provider: %s", provider)
	}
	target := normalized[0]
	paths, err := pickerBackendConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return err
	}
	current, err := config.LoadAIEnabledAgentsFile(paths.AIEnabledAgentsFile())
	if err != nil {
		return err
	}
	enabled := map[config.AIAgentProvider]bool{}
	for _, agent := range current {
		enabled[agent] = true
	}
	enabled[target] = !enabled[target]

	next := make([]config.AIAgentProvider, 0, len(config.DefaultAIEnabledAgents))
	for _, agent := range config.KnownAIAgentProviders() {
		if enabled[agent] {
			next = append(next, agent)
		}
	}
	return config.SaveAIEnabledAgentsFile(paths.AIEnabledAgentsFile(), next)
}

func (c *settingsCommand) aiEnabledAgentsSummary() string {
	enabled := c.currentAIEnabledAgents()
	if len(enabled) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(enabled))
	for _, agent := range enabled {
		names = append(names, aiEnabledAgentDisplayName(agent))
	}
	return strings.Join(names, ", ")
}

func (c *settingsCommand) aiDefaultModeDisabledWarning() string {
	if c.ai == nil {
		return ""
	}
	mode := config.NormalizeAIEnabledAgents([]string{c.ai.getMode()})
	if len(mode) != 1 {
		return ""
	}
	if aiEnabledAgentsContains(c.currentAIEnabledAgents(), mode[0]) {
		return ""
	}
	return string(mode[0]) + " disabled"
}

func aiEnabledAgentsContains(agents []config.AIAgentProvider, provider config.AIAgentProvider) bool {
	return slices.Contains(agents, provider)
}

func aiEnabledAgentDisplayName(provider config.AIAgentProvider) string {
	if meta, ok := aiprovider.Lookup(string(provider)); ok && meta.DisplayName != "" {
		return meta.DisplayName
	}
	return string(provider)
}

func (c *settingsCommand) statusbarEntries() []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	current := c.currentStatusbarDecorations()
	badgeStyle := loadAIBadgeStyle(c.homeDir, c.lookupEnv)
	targets := []statusbarDecorationTarget{
		statusbarDecorationTargetCwd,
		statusbarDecorationTargetGit,
		statusbarDecorationTargetNotify,
	}

	entries := make([]intpickercompat.Entry, 0, len(targets)+3)
	entries = append(entries, settingsBackEntryLocale(locale))
	entries = append(entries, c.localeSettingsEntry())
	entries = append(entries, c.themeFontStatusEntry())
	entries = append(entries, intpickercompat.Entry{
		Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "AI badge style", string(badgeStyle)+" - "+aiBadgeStylePreview(badgeStyle)),
		Value:     settingsActionPrefixAIBadgeStyle,
		SearchKey: "appearance ai badge style semantic pane border " + string(badgeStyle),
	})
	for _, target := range targets {
		meta, ok := statusbarDecorationTargetMeta(target)
		if !ok {
			continue
		}
		mode := current.modeForTarget(target)
		entries = append(entries, intpickercompat.Entry{
			Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, meta.Name, string(mode)+" - "+statusbarDecorationPreview(target, mode)),
			Value:     settingsActionPrefixStatusbar + string(target),
			SearchKey: "appearance decoration statusbar " + string(target) + " " + string(mode) + " " + meta.Name + " " + meta.Description,
		})
	}
	return entries
}

func (s statusbarDecorationSet) modeForTarget(target statusbarDecorationTarget) config.StatusbarDecoration {
	switch target {
	case statusbarDecorationTargetCwd:
		return s.Cwd
	case statusbarDecorationTargetGit:
		return s.Git
	case statusbarDecorationTargetNotify:
		return s.Notify
	default:
		return config.StatusbarDecorationOff
	}
}

type statusbarDecorationTargetDetails struct {
	Name        string
	Title       string
	Description string
}

func statusbarDecorationTargetMeta(target statusbarDecorationTarget) (statusbarDecorationTargetDetails, bool) {
	switch target {
	case statusbarDecorationTargetCwd:
		return statusbarDecorationTargetDetails{
			Name:        "Path icon",
			Title:       "Appearance - Path icon",
			Description: "folder marker before cwd",
		}, true
	case statusbarDecorationTargetGit:
		return statusbarDecorationTargetDetails{
			Name:        "Git icon",
			Title:       "Appearance - Git icon",
			Description: "provider marker before branch",
		}, true
	case statusbarDecorationTargetNotify:
		return statusbarDecorationTargetDetails{
			Name:        "Notify icon",
			Title:       "Appearance - Notify icon",
			Description: "bell marker in notification sidebar",
		}, true
	default:
		return statusbarDecorationTargetDetails{}, false
	}
}

func (c *settingsCommand) runAppearanceSection(stdout, stderr io.Writer) error {
	for {
		options, err := c.sectionOptions(settingsSectionStatusbar)
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
		case action == settingsAppearanceLanguage:
			if err := c.runLocaleSection(stdout, stderr); err != nil {
				return err
			}
		case action == settingsActionPrefixAIBadgeStyle:
			if err := c.runAIBadgeStyleSection(stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixStatusbar):
			target, ok := parseStatusbarDecorationTarget(strings.TrimPrefix(action, settingsActionPrefixStatusbar))
			if !ok {
				return fmt.Errorf("unknown appearance target: %s", action)
			}
			if err := c.runAppearanceTargetSection(target, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown appearance action: %s", action)
		}
	}
}

func (c *settingsCommand) runAIBadgeStyleSection(stdout, stderr io.Writer) error {
	for {
		options := c.aiBadgeStyleOptions()
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
		case strings.HasPrefix(action, settingsActionPrefixAIBadgeStyle):
			style := strings.TrimPrefix(action, settingsActionPrefixAIBadgeStyle)
			if !isAIBadgeStyle(style) {
				return fmt.Errorf("unknown AI badge style action: %s", action)
			}
			if err := c.setAIBadgeStyle(style); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown AI badge style action: %s", action)
		}
	}
}

func (c *settingsCommand) aiBadgeStyleOptions() intpickercompat.Options {
	locale := appLocale(c.homeDir, c.lookupEnv)
	return intpickercompat.Options{
		UI:         "settings-ai-badge-style",
		Entries:    c.aiBadgeStyleEntries(),
		Title:      "Appearance - AI badge style",
		TitleChips: settingsPassiveRootTabChipsLocale(settingsRootTabGlobal, c.resolveSettingsProjectContext().hasProject(), locale),
		Prompt:     "Settings > Appearance > AI badge style > ",
		Footer:     projmuxFooter("Enter: apply  |  Back row: parent "),
		ExpectKeys: []string{"enter"},
		Bindings:   c.settingsCloseBindings(),
	}
}

func (c *settingsCommand) aiBadgeStyleEntries() []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	current := loadAIBadgeStyle(c.homeDir, c.lookupEnv)
	entries := []intpickercompat.Entry{
		settingsBackEntryLocale(locale),
		{Label: settingsLabelInfoLocale(locale, "Current", string(current), "pane border live AI marker"), Value: settingsNoopValue},
	}
	for _, style := range aiBadgeStyles() {
		glyph := settingsGlyphInactive
		color := settingsColorDim
		desc := aiBadgeStylePreview(style) + " - " + aiBadgeStyleDescription(style)
		if style == current {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
			desc += " - current"
		}
		entries = append(entries, intpickercompat.Entry{
			Label:     settingsLabelLocale(locale, glyph, color, "Preview "+string(style), desc),
			Value:     settingsActionPrefixAIBadgeStyle + string(style),
			SearchKey: "ai badge style " + string(style) + " " + aiBadgeStylePreview(style),
		})
	}
	return entries
}

func aiBadgeStyles() []config.AIBadgeStyle {
	return []config.AIBadgeStyle{
		config.AIBadgeStyleDot,
		config.AIBadgeStyleEmoji,
		config.AIBadgeStyleOff,
	}
}

func isAIBadgeStyle(value string) bool {
	switch config.AIBadgeStyle(strings.TrimSpace(value)) {
	case config.AIBadgeStyleDot, config.AIBadgeStyleEmoji, config.AIBadgeStyleOff:
		return true
	default:
		return false
	}
}

func aiBadgeStyleDescription(style config.AIBadgeStyle) string {
	switch config.NormalizeAIBadgeStyle(string(style)) {
	case config.AIBadgeStyleEmoji:
		return "emoji marker"
	case config.AIBadgeStyleOff:
		return "preserve spacing without marker"
	default:
		return "colored dot marker"
	}
}

func aiBadgeStylePreview(style config.AIBadgeStyle) string {
	switch config.NormalizeAIBadgeStyle(string(style)) {
	case config.AIBadgeStyleEmoji:
		return "⏳ prompt  ✅ complete  🔄 working"
	case config.AIBadgeStyleOff:
		return "prompt  complete  working"
	default:
		return "● prompt  ● complete  ● working"
	}
}

func (c *settingsCommand) runAppearanceTargetSection(target statusbarDecorationTarget, stdout, stderr io.Writer) error {
	for {
		options, err := c.statusbarDecorationTargetOptions(target)
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
		case strings.HasPrefix(action, settingsActionPrefixStatusbar):
			raw := strings.TrimPrefix(action, settingsActionPrefixStatusbar)
			actionTarget, mode, ok := parseStatusbarDecorationDetailAction(raw)
			if !ok || actionTarget != target || !isStatusbarDecorationMode(mode) {
				return fmt.Errorf("unknown appearance detail action: %s", action)
			}
			if err := c.setStatusbarDecoration(string(actionTarget) + ":" + mode); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown appearance detail action: %s", action)
		}
	}
}

func (c *settingsCommand) statusbarDecorationTargetOptions(target statusbarDecorationTarget) (intpickercompat.Options, error) {
	locale := appLocale(c.homeDir, c.lookupEnv)
	meta, ok := statusbarDecorationTargetMeta(target)
	if !ok {
		return intpickercompat.Options{}, fmt.Errorf("unknown appearance target: %s", target)
	}
	return intpickercompat.Options{
		UI:         "settings-statusbar-detail",
		Entries:    c.statusbarDecorationTargetEntries(target),
		Title:      meta.Title,
		TitleChips: settingsPassiveRootTabChipsLocale(settingsRootTabGlobal, c.resolveSettingsProjectContext().hasProject(), locale),
		Prompt:     "Settings > Appearance > " + meta.Name + " > ",
		Footer:     projmuxFooter("Enter: apply  |  Back row: parent "),
		ExpectKeys: []string{"enter"},
		Bindings:   c.settingsCloseBindings(),
	}, nil
}

func (c *settingsCommand) statusbarDecorationTargetEntries(target statusbarDecorationTarget) []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	current := c.currentStatusbarDecorations().modeForTarget(target)
	meta, _ := statusbarDecorationTargetMeta(target)
	entries := []intpickercompat.Entry{
		settingsBackEntryLocale(locale),
		{Label: settingsLabelInfoLocale(locale, "Current", string(current), meta.Description), Value: settingsNoopValue},
	}
	for _, mode := range statusbarDecorationModes() {
		glyph := settingsGlyphInactive
		color := settingsColorDim
		desc := statusbarDecorationPreview(target, mode) + " - " + statusbarDecorationModeDescription(mode)
		if mode == current {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
			desc += " - current"
		}
		entries = append(entries, intpickercompat.Entry{
			Label:     settingsLabelLocale(locale, glyph, color, "Preview "+string(mode), desc),
			Value:     settingsActionPrefixStatusbar + string(target) + ":" + string(mode),
			SearchKey: string(target) + " " + string(mode) + " preview " + statusbarDecorationPreview(target, mode),
		})
	}
	return entries
}

func statusbarDecorationModes() []config.StatusbarDecoration {
	return []config.StatusbarDecoration{
		config.StatusbarDecorationOff,
		config.StatusbarDecorationSymbol,
		config.StatusbarDecorationEmoji,
	}
}

func isStatusbarDecorationMode(value string) bool {
	switch config.StatusbarDecoration(strings.TrimSpace(value)) {
	case config.StatusbarDecorationOff, config.StatusbarDecorationSymbol, config.StatusbarDecorationEmoji:
		return true
	default:
		return false
	}
}

func statusbarDecorationModeDescription(mode config.StatusbarDecoration) string {
	switch mode {
	case config.StatusbarDecorationSymbol:
		return "Nerd Font-style marker"
	case config.StatusbarDecorationEmoji:
		return "emoji marker"
	default:
		return "no icon prefix"
	}
}

func statusbarDecorationPreview(target statusbarDecorationTarget, mode config.StatusbarDecoration) string {
	mode = config.NormalizeStatusbarDecoration(string(mode))
	switch target {
	case statusbarDecorationTargetCwd:
		switch mode {
		case config.StatusbarDecorationSymbol:
			return " ~/source/repos/projmux"
		case config.StatusbarDecorationEmoji:
			return "📁 ~/source/repos/projmux"
		default:
			return "~/source/repos/projmux"
		}
	case statusbarDecorationTargetGit:
		switch mode {
		case config.StatusbarDecorationSymbol:
			return " main * ↑1"
		case config.StatusbarDecorationEmoji:
			return "🐱 main * ↑1"
		default:
			return "main * ↑1"
		}
	case statusbarDecorationTargetNotify:
		switch mode {
		case config.StatusbarDecorationSymbol:
			return " Pending Notifications"
		case config.StatusbarDecorationEmoji:
			return "🔔 Pending Notifications"
		default:
			return "Pending Notifications"
		}
	default:
		return string(mode)
	}
}

func parseStatusbarDecorationTarget(value string) (statusbarDecorationTarget, bool) {
	target := statusbarDecorationTarget(strings.TrimSpace(value))
	switch target {
	case statusbarDecorationTargetCwd, statusbarDecorationTargetGit, statusbarDecorationTargetNotify:
		return target, true
	default:
		return "", false
	}
}

func parseStatusbarDecorationDetailAction(value string) (statusbarDecorationTarget, string, bool) {
	targetRaw, op, ok := strings.Cut(strings.TrimSpace(value), ":")
	if !ok {
		return "", "", false
	}
	target, ok := parseStatusbarDecorationTarget(targetRaw)
	if !ok {
		return "", "", false
	}
	op = strings.TrimSpace(op)
	if op == "" {
		return "", "", false
	}
	return target, op, true
}

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
		if after, ok := strings.CutPrefix(action, settingsActionPrefixKeymap); ok {
			id := after
			if err := c.runKeybindingDetail(id, stdout, stderr); err != nil {
				return err
			}
			continue
		}
		return fmt.Errorf("unknown keybinding settings action: %s", action)
	}
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
			if err := c.runKeybindingAdd(actionID, stdout, stderr); err != nil {
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
		case "unbind":
			if err := c.saveKeymapKeysAndApply(actionID, nil, stdout); err != nil {
				return err
			}
		case "reset":
			if err := c.resetKeymapKeysAndApply(actionID, stdout); err != nil {
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
				if err := c.removeKeymapKeyAndApply(actionID, chord, stdout); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("unknown keybinding operation: %s", op)
		}
	}
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
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-keybinding-add",
			Entries:    entries,
			Title:      title,
			Prompt:     "Settings > Keybindings > Action > Add key > ",
			Footer:     projmuxFooter("Press a key to add it to this action."),
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
			return c.removeKeymapKeyAndApply(actionID, removeChord, stdout)
		case strings.HasPrefix(op, "test:"):
			continue
		default:
			return fmt.Errorf("unknown keybinding key operation: %s", op)
		}
	}
}

func (c *settingsCommand) runKeybindingCapture(actionID string, stdout io.Writer) error {
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
	res, err := c.probeLabKeybinding(key, defaultProbeTimeout)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "capture custom key: %s\n", renderProbeStatus(res))

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
		UI:            "settings-keybinding-type",
		Entries:       []intpickercompat.Entry{c.backEntry(), {Label: c.rowLabelInfo("Action", keyBindingDisplayName(action), keybindingAliasesSummary(action)), Value: settingsNoopValue}},
		Title:         mode + " - " + keyBindingDisplayName(action),
		Prompt:        "Enter key > ",
		Footer:        projmuxFooter("Enter a key such as C-r, M-a, M-S-Left, or C-Space."),
		ExpectKeys:    []string{"enter"},
		Bindings:      c.settingsCloseBindings(),
		AcceptQuery:   true,
		DisableSearch: true,
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

func (c *settingsCommand) keybindingEntries() ([]intpickercompat.Entry, error) {
	keymap, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return nil, err
	}
	defaults := defaultKeyBindingCatalog()
	entries := make([]intpickercompat.Entry, 0, len(actions)+2)
	entries = append(entries, c.backEntry())
	entries = append(entries, intpickercompat.Entry{
		Label: "  " + settingsColorDim + settingsCatalogTextLocale(c.locale(), "Actions are listed with active keys and state.") + settingsColorReset,
		Value: settingsNoopValue,
	})
	locale := c.locale()
	for _, action := range actions {
		defaultAction, _ := keyBindingActionByID(defaults, action.ID)
		state := keybindingState(keymap, action, defaultAction)
		desc := keybindingListSummary(action, state)
		displayName := keyBindingDisplayName(action)
		entries = append(entries, intpickercompat.Entry{
			Label:     c.rowLabel(settingsGlyphOpen, settingsColorType, displayName, desc),
			Value:     settingsActionPrefixKeymap + action.ID,
			SearchKey: strings.Join([]string{action.ID, displayName, action.Surface, action.Description, strings.Join(keybindingVisibleChords(action), " "), keybindingLocalizedSearchText(locale, action)}, " "),
		})
	}
	return entries, nil
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
	entries = append(entries, intpickercompat.Entry{
		Label: c.rowLabel(settingsGlyphAdd, settingsColorAdd, "+ Add key", "press desired key"),
		Value: prefix + "add",
	})
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

func keybindingPlainAliasesSummary(action keyBindingAction) string {
	keys := keybindingPlainAliasChords(action)
	if len(keys) == 0 {
		return "(none)"
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

func keybindingCanCapture(action keyBindingAction) bool {
	if action.Tier == keyBindingTierTransportDependent {
		return false
	}
	return strings.TrimSpace(action.ProbeLabel) != ""
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

func keybindingSource(current, def keyBindingAction) string {
	if len(keyBindingEffectivePlainChords(current)) == 0 {
		return "Unbound"
	}
	if !sameStringSlice(keyBindingEffectivePlainChords(current), keyBindingEffectivePlainChords(def)) {
		return "Custom"
	}
	return "Default"
}

func keybindingTransportAliasSource(current, def keyBindingAction) string {
	if current.Tier == keyBindingTierTransportDependent && len(keybindingPlainAliasChords(current)) != 0 {
		return "Custom"
	}
	if current.Tier == keyBindingTierTransportDependent {
		return "none saved"
	}
	return keybindingSource(current, def)
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

func keybindingDeliveryPath(action keyBindingAction) string {
	switch action.Tier {
	case keyBindingTierNativePickerInternal:
		return "picker-local"
	case keyBindingTierTransportDependent:
		return "transport-dependent"
	}
	if len(keyBindingEffectivePlainChords(action)) == 0 && strings.TrimSpace(action.PrefixChord) != "" {
		return "prefix-backed; direct key unassigned"
	}
	return "plain tmux"
}

func keybindingDeliveryHint(action keyBindingAction) string {
	switch action.Tier {
	case keyBindingTierNativePickerInternal:
		if action.Surface != "" {
			return action.Surface + " picker action"
		}
		return "native picker action"
	case keyBindingTierTransportDependent:
		if chord := keybindingTransportChord(action); chord != "" {
			return keybindingChordDisplay(chord) + " depends on terminal/tmux transport; custom keys are saved separately"
		}
		return "no default direct key; configure a custom key if needed"
	}
	if len(keyBindingEffectivePlainChords(action)) == 0 && strings.TrimSpace(action.PrefixChord) != "" {
		return "prefix " + action.PrefixChord + " exists, but no direct key is assigned"
	}
	return "direct keybinding"
}

func keybindingSurfaceSummary(action keyBindingAction) string {
	if surface := strings.TrimSpace(action.Surface); surface != "" {
		return surface
	}
	switch action.Scope {
	case keyBindingScopeStandalone:
		return "Standalone tmux"
	case keyBindingScopeApp:
		return "App tmux"
	default:
		return "Global"
	}
}

func keybindingKindSummary(action keyBindingAction) string {
	switch action.Kind {
	case keyBindingActionTogglePopup:
		return "popup toggle"
	case keyBindingActionCommand:
		return "tmux command"
	case keyBindingActionPickerInternal:
		return "picker-local action"
	default:
		if action.Kind != "" {
			return string(action.Kind)
		}
		return "keybinding action"
	}
}

func keybindingTierSummary(action keyBindingAction) string {
	switch action.Tier {
	case keyBindingTierGuaranteedLaunchDefault:
		return "Guaranteed launch default"
	case keyBindingTierUserConfigurableDirect:
		return "User configurable direct alias"
	case keyBindingTierTransportDependent:
		return "Transport dependent"
	case keyBindingTierAmbiguousTerminalChord:
		return "Ambiguous terminal chord"
	case keyBindingTierNativePickerInternal:
		return "Picker local"
	case keyBindingTierPopupLaunchCloseAlias:
		return "Popup launch close alias"
	default:
		if action.Tier != "" {
			return string(action.Tier)
		}
		return "Unclassified"
	}
}

func keybindingEditabilitySummary(action keyBindingAction) string {
	if keyBindingEditable(action) {
		if action.Tier == keyBindingTierNativePickerInternal {
			return "editable picker-local"
		}
		if action.Tier == keyBindingTierTransportDependent {
			return "additive plain aliases"
		}
		return "editable direct alias"
	}
	switch action.Tier {
	case keyBindingTierNativePickerInternal:
		return "view-only picker-local"
	case keyBindingTierTransportDependent:
		return "view-only transport-dependent"
	default:
		return "view-only"
	}
}

func keybindingNonEditableReason(action keyBindingAction) string {
	switch action.Tier {
	case keyBindingTierNativePickerInternal:
		return "handled inside the native picker surface, not the direct tmux alias editor"
	case keyBindingTierTransportDependent:
		return "depends on terminal/tmux transport; view-only in Settings"
	default:
		return "not part of the direct tmux alias editor"
	}
}

func keybindingPrimaryAndAdditional(action keyBindingAction) (string, string) {
	keys := keybindingVisibleChords(action)
	if len(keys) == 0 {
		return "(none)", "(none)"
	}
	primary := keybindingChordDisplay(keys[0])
	if len(keys) == 1 {
		return primary, "(none)"
	}
	var additional []string
	for _, key := range keys[1:] {
		additional = append(additional, keybindingChordDisplay(key))
	}
	return primary, strings.Join(additional, ", ")
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

func keybindingSourceDetail(current, def keyBindingAction) string {
	switch keybindingSource(current, def) {
	case "Custom":
		return "saved in keymap.toml"
	case "Unbound":
		if len(keyBindingEffectivePlainChords(def)) == 0 {
			return "no default fallback"
		}
		return "explicit no-bind override or no fallback"
	default:
		return "catalog fallback"
	}
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

func keybindingAdvancedDeliveryHint(action keyBindingAction) string {
	if action.Tier == keyBindingTierTransportDependent {
		return "raw sequences and terminal adapter diagnostics stay advanced"
	}
	if strings.TrimSpace(action.PrefixChord) != "" && len(keyBindingEffectivePlainChords(action)) == 0 {
		return "prefix-backed action; add a direct key here when wanted"
	}
	return "raw sequences/UserKey/terminal adapters are diagnostic-only"
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

func (c *settingsCommand) saveKeymapAndApply(actionID, field string, value *string, stdout io.Writer) error {
	path, err := saveKeymapOverride(c.keymapStore(), actionID, field, value)
	return c.finishKeymapApply(path, err, stdout)
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
	configPath, err := c.writeTmuxAppConfig()
	if err != nil {
		report.Prepared = keymapApplyStage{Status: keymapApplyFailed, Detail: keymapApplyDiagnostic("generated tmux config", err)}
		report.Live = keymapApplyStage{Status: keymapApplySkipped, Detail: "generated tmux config failed"}
		_ = writeKeymapApplyReport(stdout, report)
		return fmt.Errorf("update keybinding runtime config: %w", err)
	}
	report.Prepared = keymapApplyStage{Status: keymapApplyOK, Detail: "generated tmux config: " + configPath}
	if c.lookupEnv == nil || strings.TrimSpace(c.lookupEnv("TMUX")) == "" {
		report.Live = keymapApplyStage{Status: keymapApplySkipped, Detail: "Settings is not running inside tmux"}
		return writeKeymapApplyReport(stdout, report)
	}
	if c.runCommand == nil {
		report.Live = keymapApplyStage{Status: keymapApplySkipped, Detail: "tmux command runner is not configured"}
		return writeKeymapApplyReport(stdout, report)
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		if err := c.runCommand("tmux", "source-file", configPath); err != nil {
			report.Live = keymapApplyStage{Status: keymapApplyFailed, Detail: keymapApplyDiagnostic("live tmux reload", err)}
			_ = writeKeymapApplyReport(stdout, report)
			return fmt.Errorf("reload active tmux keybindings: %w", err)
		}
	}
	report.Live = keymapApplyStage{Status: keymapApplyOK}
	return writeKeymapApplyReport(stdout, report)
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

func (c *settingsCommand) runLabsSection(stdout, stderr io.Writer) error {
	for {
		options, err := c.sectionOptions(settingsSectionLabs)
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
		case action == settingsLabKeybindings:
			if err := c.runKeybindingsSection(stdout, stderr); err != nil {
				return err
			}
		case action == settingsLabsProjectHooks:
			return c.runLabsProjectHooksSection(stdout, stderr)
		case strings.HasPrefix(action, settingsActionPrefixHooks):
			if err := c.execute(action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown labs settings action: %s", action)
		}
	}
}

func (c *settingsCommand) runLabsProjectHooksSection(stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-labs-project-hooks",
			Entries:    c.labsProjectHooksEntries(),
			Title:      "Labs - Project Hooks",
			Prompt:     "Settings > Labs > Project Hooks > ",
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
		case strings.HasPrefix(action, settingsActionPrefixHooks):
			if err := c.execute(action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown project hooks lab action: %s", action)
		}
	}
}

func (c *settingsCommand) probeLabKeybinding(key probeKey, timeout time.Duration) (probeResult, error) {
	if c.probeKeybinding != nil {
		return c.probeKeybinding(key, timeout)
	}
	cmd := &setupCommand{openTTY: openControllingTTY}
	return cmd.probeControllingTTYKey(key, timeout)
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

func (c *settingsCommand) currentStatusbarDecorations() statusbarDecorationSet {
	return loadStatusbarDecorationSet(c.homeDir, c.lookupEnv)
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

func (c *settingsCommand) setStatusbarDecoration(value string) error {
	target, mode := parseStatusbarDecorationSetting(value)
	paths, err := statusbarConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return err
	}
	path := paths.StatusbarDecorationFile()
	option := statusbarDecorationTmuxOption
	label := "decoration mode"
	if target != "" {
		path = statusbarDecorationTargetFile(paths, target)
		option = statusbarDecorationTmuxOptionForTarget(target)
		label = "decoration " + string(target)
	}
	if err := config.SaveStatusbarDecorationFile(path, mode); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		if err := c.runCommand("tmux", "set-option", "-g", option, string(mode)); err != nil {
			return fmt.Errorf("set live tmux decoration mode: %w", err)
		}
		_ = c.runCommand("tmux", "display-message", label+": "+string(mode))
	}
	return nil
}

func (c *settingsCommand) setAIBadgeStyle(value string) error {
	style := config.NormalizeAIBadgeStyle(value)
	if err := saveAIBadgeStyle(c.homeDir, c.lookupEnv, style); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		if err := c.runCommand("tmux", "set-option", "-g", aiBadgeStyleTmuxOption, string(style)); err != nil {
			return fmt.Errorf("set live tmux AI badge style option: %w", err)
		}
		if err := c.runCommand("tmux", "set-option", "-g", "pane-border-format", tmuxPaneBorderFormatWithAIBadgeStyle(style)); err != nil {
			return fmt.Errorf("set live tmux pane border format: %w", err)
		}
		source, err := configRenderThemeSource(c.homeDir, c.lookupEnv, c.resolveSettingsProjectContext().Path)
		if err != nil {
			return fmt.Errorf("resolve live tmux theme: %w", err)
		}
		binaryPath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolve live tmux executable: %w", err)
		}
		windowStatusFormat, windowStatusCurrentFormat := tmuxWindowStatusFormats(binaryPath, source.effective)
		if err := c.runCommand("tmux", "set-option", "-g", "window-status-format", windowStatusFormat); err != nil {
			return fmt.Errorf("set live tmux window status format: %w", err)
		}
		if err := c.runCommand("tmux", "set-option", "-g", "window-status-current-format", windowStatusCurrentFormat); err != nil {
			return fmt.Errorf("set live tmux current window status format: %w", err)
		}
		_ = c.runCommand("tmux", "display-message", "AI badge style: "+string(style))
	}
	return nil
}

func parseStatusbarDecorationSetting(value string) (statusbarDecorationTarget, config.StatusbarDecoration) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ":")
	if len(parts) == 2 {
		target := statusbarDecorationTarget(strings.TrimSpace(parts[0]))
		switch target {
		case statusbarDecorationTargetCwd, statusbarDecorationTargetGit, statusbarDecorationTargetNotify:
			return target, config.NormalizeStatusbarDecoration(parts[1])
		}
	}
	return "", config.NormalizeStatusbarDecoration(value)
}

func (c *settingsCommand) labsEntries() []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	current, source := c.currentPickerBackend()
	hookMode, hookSource := c.currentProjectHooksMode()
	entries := make([]intpickercompat.Entry, 0, 4)
	entries = append(entries, settingsBackEntryLocale(locale))
	entries = append(entries, intpickercompat.Entry{
		Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Project Hooks", string(hookMode)+" - "+hookSource),
		Value:     settingsLabsProjectHooks,
		SearchKey: "Project Hooks trusted local hooks on off",
	})
	if source != "" {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Picker source", string(current), source),
			Value: settingsNoopValue,
		})
	}
	return entries
}

func (c *settingsCommand) labsProjectHooksEntries() []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	hookMode, hookSource := c.currentProjectHooksMode()
	entries := make([]intpickercompat.Entry, 0, 4)
	entries = append(entries, settingsBackEntryLocale(locale))
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabelInfoLocale(locale, "Project hooks", string(hookMode), hookSource),
		Value: settingsNoopValue,
	})
	for _, item := range []struct {
		mode config.ProjectHooksMode
		desc string
	}{
		{config.ProjectHooksOn, "allow trusted project-local post-create hooks"},
		{config.ProjectHooksOff, "disable project-local hooks; global hook still runs"},
	} {
		glyph := settingsGlyphInactive
		color := settingsColorDim
		if item.mode == hookMode {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
		}
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelLocale(locale, glyph, color, "Project hooks "+string(item.mode), item.desc),
			Value: settingsActionPrefixHooks + string(item.mode),
		})
	}
	return entries
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

func (c *settingsCommand) aiForSettings() *aiCommand {
	if c.ai != nil {
		return c.ai
	}
	return &aiCommand{homeDir: c.homeDir, lookupEnv: c.lookupEnv}
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

func (c *settingsCommand) currentProjectHooksMode() (config.ProjectHooksMode, string) {
	if c.lookupEnv != nil && strings.EqualFold(strings.TrimSpace(c.lookupEnv("PROJMUX_PROJECT_HOOKS")), string(config.ProjectHooksOff)) {
		return config.ProjectHooksOff, "PROJMUX_PROJECT_HOOKS env"
	}
	paths, err := pickerBackendConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return config.ProjectHooksOn, "default"
	}
	mode, err := config.LoadProjectHooksFile(paths.ProjectHooksFile())
	if err != nil {
		return config.ProjectHooksOn, "default"
	}
	if _, err := osStat(paths.ProjectHooksFile()); err == nil {
		return mode, "saved"
	}
	return mode, "default"
}

func (c *settingsCommand) setProjectHooksMode(value string) error {
	mode := config.NormalizeProjectHooksMode(value)
	paths, err := pickerBackendConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return err
	}
	if err := config.SaveProjectHooksFile(paths.ProjectHooksFile(), mode); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		_ = c.runCommand("tmux", "display-message", "project hooks: "+string(mode))
	}
	return nil
}

func (c *settingsCommand) currentPickerBackend() (config.PickerBackend, string) {
	if backend, ok := pickerBackendFromEnv(c.lookupEnv); ok {
		return config.NormalizePickerBackend(string(backend)), intpicker.BackendEnv + " env"
	}

	paths, err := pickerBackendConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return config.DefaultPickerBackend, "default"
	}
	mode, err := config.LoadPickerBackendFile(paths.PickerBackendFile())
	if err != nil {
		return config.DefaultPickerBackend, "default"
	}
	if _, err := osStat(paths.PickerBackendFile()); err == nil {
		return mode, "saved"
	}
	return mode, "default"
}

func (c *settingsCommand) setPickerBackend(value string) error {
	mode := config.NormalizePickerBackend(value)
	paths, err := pickerBackendConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return err
	}
	if err := config.SavePickerBackendFile(paths.PickerBackendFile(), mode); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		if err := c.runCommand("tmux", "set-environment", "-g", pickerBackendTmuxEnv, string(mode)); err != nil {
			return fmt.Errorf("set live tmux picker backend: %w", err)
		}
		_ = c.runCommand("tmux", "display-message", "picker backend: "+string(mode))
	}
	return nil
}

func (c *settingsCommand) aboutEntries() []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	status, statusErr := updateStatus{}, errors.New("update status is not configured")
	if c.update != nil {
		status, statusErr = c.update.status()
	}

	rows := []struct{ name, value string }{
		{"Version", "projmux " + version.String()},
		{"Source", "https://github.com/crevissepartners/projmux"},
		{"App", "sidebar, sessions, projects, AI picker, settings"},
		{"Tmux actions", "new window, rename window/pane, previous/next window"},
		{"Key setup", "try shortcuts in projmux shell before changing terminal config"},
		{"Diagnose keys", "projmux setup reports swallowed shortcuts"},
		{"Terminal remediation", "projmux init previews supported terminal key delivery mappings"},
		{"Dependencies", "projmux doctor checks tmux, git, stty, kubectl"},
		{"Rename key", "configure a plain alias or use tmux prefix rename"},
		{"Ghostty", "Alt Meta defaults normally need no projmux key block"},
		{"Windows Term.", "actions sendInput tmux/meta sequences; keybindings attach keys"},
		{"Docs", "docs/keybindings.md has copyable terminal examples"},
	}
	entries := make([]intpickercompat.Entry, 0, len(rows)+8)
	entries = append(entries, settingsBackEntryLocale(locale))
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Welcome", "revisit the shell quickstart guide"),
		Value: settingsWelcomeShow,
	})
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabelLocale(locale, settingsGlyphRemove, settingsColorRemove, "Quit projmux", "open quit actions"),
		Value: settingsQuitOpen,
	})
	if statusErr != nil {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Update", "status unavailable", statusErr.Error()),
			Value: settingsNoopValue,
		})
	} else {
		latest := status.LatestVersion
		if latest == "" {
			latest = "unknown"
		}
		entries = append(entries,
			intpickercompat.Entry{
				Label: settingsLabelLocale(locale, settingsGlyphAdd, settingsColorAdd, "Update Now", "run installer-specific update command"),
				Value: settingsUpdateApply,
			},
			intpickercompat.Entry{
				Label: settingsLabelLocale(locale, settingsGlyphAdd, settingsColorAdd, "Check Updates", "refresh cached GitHub release metadata"),
				Value: settingsUpdateCheck,
			},
			intpickercompat.Entry{
				Label: settingsLabelInfoLocale(locale, "Latest", latest, status.CacheState),
				Value: settingsNoopValue,
			},
			intpickercompat.Entry{
				Label: settingsLabelInfoLocale(locale, "Update state", status.UpdateState, ""),
				Value: settingsNoopValue,
			},
			intpickercompat.Entry{
				Label: settingsLabelInfoLocale(locale, "Installer", status.Installer.Source, status.Installer.Note),
				Value: settingsNoopValue,
			},
		)
		if status.ReleaseURL != "" {
			entries = append(entries, intpickercompat.Entry{
				Label: settingsLabelInfoLocale(locale, "Release notes", status.ReleaseURL, ""),
				Value: settingsNoopValue,
			})
		}
	}
	for _, r := range rows {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, r.name, r.value, ""),
			Value: settingsNoopValue,
		})
	}
	return entries
}

func (c *settingsCommand) execute(value string, stdout, stderr io.Writer) error {
	switch {
	case strings.HasPrefix(value, settingsActionPrefixAI):
		mode := strings.TrimPrefix(value, settingsActionPrefixAI)
		if c.ai == nil {
			return errors.New("ai settings are not configured")
		}
		return c.ai.setMode(mode)
	case strings.HasPrefix(value, settingsActionPrefixDesktopNotifyMode):
		return c.setDesktopNotifyMode(strings.TrimPrefix(value, settingsActionPrefixDesktopNotifyMode))
	case strings.HasPrefix(value, settingsActionPrefixHooks):
		return c.setProjectHooksMode(strings.TrimPrefix(value, settingsActionPrefixHooks))
	case strings.HasPrefix(value, settingsActionPrefixPicker):
		return c.setPickerBackend(strings.TrimPrefix(value, settingsActionPrefixPicker))
	case strings.HasPrefix(value, settingsActionPrefixProjdir):
		action := strings.TrimPrefix(value, settingsActionPrefixProjdir)
		if c.switcher == nil {
			return errors.New("project root settings are not configured")
		}
		return c.switcher.executeProjdirSettingsAction(action, stdout, stderr)
	case strings.HasPrefix(value, settingsActionPrefixSessionState):
		return c.executeSessionStateAction(strings.TrimPrefix(value, settingsActionPrefixSessionState), stdout, stderr)
	case strings.HasPrefix(value, settingsActionPrefixStatusbar):
		return c.setStatusbarDecoration(strings.TrimPrefix(value, settingsActionPrefixStatusbar))
	case strings.HasPrefix(value, settingsActionPrefixSwitch):
		action := strings.TrimPrefix(value, settingsActionPrefixSwitch)
		if c.switcher == nil {
			return errors.New("project picker settings are not configured")
		}
		return c.switcher.executeSettingsAction(action, stdout, stderr)
	case strings.HasPrefix(value, settingsActionPrefixUpdate):
		if c.update == nil {
			return errors.New("update settings are not configured")
		}
		action := strings.TrimPrefix(value, settingsActionPrefixUpdate)
		switch action {
		case "apply":
			return c.update.Run([]string{"apply"}, stdout, stderr)
		case "check":
			return c.update.Run([]string{"check"}, stdout, stderr)
		default:
			return fmt.Errorf("unknown update settings action: %s", action)
		}
	case strings.HasPrefix(value, settingsActionPrefixWelcome):
		switch strings.TrimPrefix(value, settingsActionPrefixWelcome) {
		case "show":
			return c.runWelcomeSettingsViewer()
		default:
			return fmt.Errorf("unknown welcome settings action: %s", value)
		}
	case strings.HasPrefix(value, settingsActionPrefixQuit):
		if c.quit == nil {
			return errors.New("quit settings are not configured")
		}
		switch strings.TrimPrefix(value, settingsActionPrefixQuit) {
		case "open":
			return c.quit.Run(nil, stdout, stderr)
		default:
			return fmt.Errorf("unknown quit settings action: %s", value)
		}
	case strings.HasPrefix(value, settingsActionPrefixWorkdir):
		action := strings.TrimPrefix(value, settingsActionPrefixWorkdir)
		if c.switcher == nil {
			return errors.New("project picker settings are not configured")
		}
		return c.switcher.executeWorkdirSettingsAction(action, stdout, stderr)
	default:
		printSettingsUsage(stderr)
		return fmt.Errorf("unknown settings action: %s", value)
	}
}

func (c *settingsCommand) runWelcomeSettingsViewer() error {
	for {
		result, err := c.runPicker(c.welcomeSettingsViewerOptions())
		if err != nil {
			return err
		}
		action := strings.TrimSpace(result.Value)
		if result.Key != "enter" || action == "" {
			return errSettingsClosed
		}
		switch action {
		case settingsBackValue:
			return nil
		case settingsNoopValue:
			continue
		default:
			return fmt.Errorf("unknown welcome viewer action: %s", action)
		}
	}
}

func (c *settingsCommand) welcomeSettingsViewerOptions() intpickercompat.Options {
	status, hasStatus := resolveWelcomeUpdateStatus(c.update)
	var body strings.Builder
	locale := appLocale(c.homeDir, c.lookupEnv)
	_ = writeShellWelcome(&body, welcomeCurrentVersion(), status, hasStatus, false, false, false, welcomeWidthFromEnv(c.lookupEnv), locale)
	entries := []intpickercompat.Entry{c.backEntry()}
	for line := range strings.SplitSeq(strings.Trim(body.String(), "\n"), "\n") {
		entries = append(entries, intpickercompat.Entry{
			Label:     line,
			Value:     settingsNoopValue,
			SearchKey: "welcome shell bootstrap update skip",
		})
	}
	return intpickercompat.Options{
		UI:            "settings-about-welcome",
		Entries:       entries,
		Title:         settingsCatalogTextLocale(locale, "About") + " - " + settingsCatalogTextLocale(locale, "Welcome"),
		Prompt:        settingsCatalogTextLocale(locale, "Settings") + " > " + settingsCatalogTextLocale(locale, "About") + " > " + settingsCatalogTextLocale(locale, "Welcome") + " > ",
		Footer:        projmuxFooter("Back row: About  |  Esc: close settings"),
		DisableSearch: true,
		Bindings:      c.settingsCloseBindings(),
	}
}

func settingsBackEntry() intpickercompat.Entry {
	return settingsBackEntryLocale(settingsLocaleFromEnv())
}

func settingsBackEntryLocale(locale i18n.Locale) intpickercompat.Entry {
	return intpickercompat.Entry{
		Label: settingsLabelLocale(locale, settingsGlyphBack, settingsColorBack, "Back", ""),
		Value: settingsBackValue,
	}
}

func (c *settingsCommand) settingsCloseBindings() []string {
	if c == nil {
		return settingsCloseBindings()
	}
	return pickerCloseBindingsForPopupToggleMode(c.homeDir, c.lookupEnv, "ai-split-settings", "esc", "ctrl-c", "ctrl-alt-s")
}

func settingsCloseBindings() []string {
	return pickerCloseBindingsForPopupToggleMode(nil, nil, "ai-split-settings", "esc", "ctrl-c", "ctrl-alt-s")
}

func printSettingsUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux settings")
}
