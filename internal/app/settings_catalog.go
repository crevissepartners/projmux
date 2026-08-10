package app

import (
	"fmt"
	"strings"

	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

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
	settingsAIResumePicker:             {Name: "Resume picker", Axis: settingsAxisGlobal},
	settingsAINotifyDiagnostics:        {Name: "AI notify diagnostics", Axis: settingsAxisGlobal},
	settingsNotificationsDesktop:       {Name: "Desktop notification settings", Axis: settingsAxisGlobal},
	settingsNotificationsAIDedupe:      {Name: "AI notification dedupe", Axis: settingsAxisGlobal},
	settingsNotificationsDelivery:      {Name: "Delivery sources", Axis: settingsAxisGlobal},
	settingsNotificationsHookActions:   {Name: "Hook quiet policy", Axis: settingsAxisGlobal},
	settingsNotificationsQueue:         {Name: "In-app queue", Axis: settingsAxisGlobal},
	settingsNotificationsHookOverride:  {Name: "Notification hook override", Axis: settingsAxisGlobal},
	settingsAppearanceLanguage:         {Name: "Language / Locale", Axis: settingsAxisGlobal},
	settingsNativeKeysToggle:           {Name: "Native macOS keybindings", Axis: settingsAxisGlobal},
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
	{settingsActionPrefixAIResumeLimit, settingsEntryMeta{Name: "Resume picker", Axis: settingsAxisGlobal}},
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
	settingsActionPrefixAIResumeLimit      = "ai-resume-limit:"
	settingsActionPrefixAIResumeDepth      = "ai-resume-depth:"
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
	settingsAIResumePicker                 = "ai-resume-picker"
	settingsAIResumePickerLimit            = "ai-resume-picker-limit"
	settingsAIResumePickerDepth            = "ai-resume-picker-depth"
	settingsAINotifyDiagnostics            = "ai-notify-diagnostics"
	settingsNotificationsDesktop           = "notifications:desktop"
	settingsNotificationsAIDedupe          = "notifications:ai-dedupe"
	settingsNotificationsDelivery          = "notifications:delivery"
	settingsNotificationsHookActions       = "notifications:hook-actions"
	settingsNotificationsQueue             = "notifications:queue"
	settingsNotificationsHookOverride      = "notifications:hook-override"
	settingsAppearanceLanguage             = "appearance:language"
	settingsNativeKeysToggle               = "native-keys:toggle"
	settingsLabsProjectHooks               = "labs:project-hooks"
	settingsLabKeybindings                 = "labs:keybindings"
	settingsSessionStateDelete             = "sessionstate:delete"
	settingsWelcomeShow                    = "welcome:show"
	settingsKeymapFieldPlain               = "plain"
	settingsKeymapFieldKeys                = "keys"
	settingsKeymapFieldPrefix              = "prefix"
)

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
			Title:      "Appearance - AI badge and icon decoration",
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
