package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
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
	ai                 *aiCommand
	switcher           *switchCommand
	update             *updateCommand
	runner             intpickercompat.Runner
	nativePicker       intpicker.Runner
	homeDir            func() (string, error)
	lookupEnv          func(string) string
	runCommand         func(name string, args ...string) error
	probeKeybinding    func(probeKey, time.Duration) (probeResult, error)
	runInitKeybindings func(args []string, stdout, stderr io.Writer) error
	lastLabProbe       map[string]probeResult
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
	settingsBackValue:             {Name: "Back", Axis: settingsAxisBoth},
	settingsNoopValue:             {Name: "Info", Axis: settingsAxisBoth},
	settingsRootTabGlobalValue:    {Name: "Global Settings", Axis: settingsAxisBoth},
	settingsRootTabProjectValue:   {Name: "Project Settings", Axis: settingsAxisBoth},
	settingsSectionProject:        {Name: "Project Picker", Axis: settingsAxisGlobal},
	settingsSectionGlobalHooks:    {Name: "Hooks", Axis: settingsAxisGlobal},
	settingsSectionProjectHooks:   {Name: "Hooks", Axis: settingsAxisProject},
	settingsSectionProjectConfig:  {Name: "Project recipe", Axis: settingsAxisProject},
	settingsSectionProjectTrust:   {Name: "Trust", Axis: settingsAxisProject},
	settingsSectionEffectiveMerge: {Name: "Effective merge view", Axis: settingsAxisProject},
	settingsSectionAI:             {Name: "AI Settings", Axis: settingsAxisGlobal},
	settingsSectionStatusbar:      {Name: "Appearance", Axis: settingsAxisGlobal},
	settingsSectionKeybindings:    {Name: "Keybindings", Axis: settingsAxisGlobal},
	settingsSectionLabs:           {Name: "Labs", Axis: settingsAxisGlobal},
	settingsSectionAbout:          {Name: "About", Axis: settingsAxisGlobal},
	settingsProjectAdd:            {Name: "Add Project", Axis: settingsAxisGlobal},
	settingsProjectPins:           {Name: "Pinned Projects", Axis: settingsAxisGlobal},
	settingsProjectRootManage:     {Name: "Project Root", Axis: settingsAxisGlobal},
	settingsProjdirClear:          {Name: "Clear Project Root", Axis: settingsAxisGlobal},
	settingsProjdirSetCurrent:     {Name: "Use Current Project as Root", Axis: settingsAxisGlobal},
	settingsProjdirSetTyped:       {Name: "Set Project Root", Axis: settingsAxisGlobal},
	settingsWorkdirAdd:            {Name: "Add Workdir", Axis: settingsAxisGlobal},
	settingsWorkdirList:           {Name: "Workdirs", Axis: settingsAxisGlobal},
	settingsWorkdirTyped:          {Name: "Type Workdir", Axis: settingsAxisGlobal},
	settingsKeybindingsDiagnostic: {Name: "Keybinding Diagnostic", Axis: settingsAxisGlobal},
	settingsKeybindingsProbe:      {Name: "Keybinding Probe", Axis: settingsAxisGlobal},
	settingsKeybindingsInit:       {Name: "Keybinding Init", Axis: settingsAxisGlobal},
	settingsAIDefaultMode:         {Name: "Default split mode", Axis: settingsAxisGlobal},
	settingsLabsDesktopNotify:     {Name: "Desktop notifications", Axis: settingsAxisGlobal},
	settingsLabKeybindings:        {Name: "Keybindings", Axis: settingsAxisGlobal},
	settingsUpdateApply:           {Name: "Update Now", Axis: settingsAxisGlobal},
	settingsUpdateCheck:           {Name: "Check Updates", Axis: settingsAxisGlobal},
	settingsWelcomeShow:           {Name: "Welcome", Axis: settingsAxisGlobal},
}

var settingsEntryPrefixCatalog = []struct {
	prefix string
	meta   settingsEntryMeta
}{
	{settingsActionPrefixAI, settingsEntryMeta{Name: "AI Settings", Axis: settingsAxisGlobal}},
	{settingsActionPrefixDesktopNotifyMode, settingsEntryMeta{Name: "Desktop notifications", Axis: settingsAxisGlobal}},
	{settingsActionPrefixHooks, settingsEntryMeta{Name: "Project hook policy", Axis: settingsAxisGlobal}},
	{settingsActionPrefixHookAdd, settingsEntryMeta{Name: "Hook maker - add", Axis: settingsAxisBoth}},
	{settingsActionPrefixHookEdit, settingsEntryMeta{Name: "Hook maker - edit", Axis: settingsAxisBoth}},
	{settingsActionPrefixHookRemove, settingsEntryMeta{Name: "Hook maker - remove", Axis: settingsAxisBoth}},
	{settingsActionPrefixHookView, settingsEntryMeta{Name: "Hook maker - view", Axis: settingsAxisBoth}},
	{settingsActionPrefixKeymap, settingsEntryMeta{Name: "Keybindings", Axis: settingsAxisGlobal}},
	{settingsActionPrefixLabKeymap, settingsEntryMeta{Name: "Keybindings diagnostics", Axis: settingsAxisGlobal}},
	{settingsActionPrefixPicker, settingsEntryMeta{Name: "Picker backend", Axis: settingsAxisGlobal}},
	{settingsActionPrefixProjectConfig, settingsEntryMeta{Name: "Project recipe", Axis: settingsAxisProject}},
	{settingsActionPrefixWelcome, settingsEntryMeta{Name: "Welcome", Axis: settingsAxisGlobal}},
	{settingsActionPrefixTrust, settingsEntryMeta{Name: "Trust", Axis: settingsAxisProject}},
	{settingsActionPrefixProjdir, settingsEntryMeta{Name: "Project Root", Axis: settingsAxisGlobal}},
	{settingsActionPrefixStatusbar, settingsEntryMeta{Name: "Appearance", Axis: settingsAxisGlobal}},
	{settingsActionPrefixSwitch, settingsEntryMeta{Name: "Pinned Projects", Axis: settingsAxisGlobal}},
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
	settingsBackValue                     = "__settings_back__"
	settingsNoopValue                     = "__settings_noop__"
	settingsRootTabGlobalValue            = "__settings_tab_global__"
	settingsRootTabProjectValue           = "__settings_tab_project__"
	settingsSectionAI                     = "section:ai"
	settingsSectionGlobalHooks            = "section:hooks-global"
	settingsSectionProjectHooks           = "section:hooks-project"
	settingsSectionProjectConfig          = "section:project-config"
	settingsSectionProjectTrust           = "section:project-trust"
	settingsSectionEffectiveMerge         = "section:effective-merge"
	settingsSectionKeybindings            = "section:keybindings"
	settingsSectionProject                = "section:project-picker"
	settingsSectionStatusbar              = "section:statusbar"
	settingsSectionLabs                   = "section:labs"
	settingsSectionAbout                  = "section:about"
	settingsActionPrefixAI                = "ai:"
	settingsActionPrefixDesktopNotifyMode = "desktop-notify-mode:"
	settingsActionPrefixHooks             = "project-hooks:"
	settingsActionPrefixKeymap            = "keymap:"
	settingsActionPrefixLabKeymap         = "lab-keymap:"
	settingsActionPrefixPicker            = "picker-backend:"
	settingsActionPrefixProjectConfig     = "project-config:"
	settingsActionPrefixWelcome           = "welcome:"
	settingsActionPrefixTrust             = "trust:"
	settingsActionPrefixProjdir           = "projdir:"
	settingsActionPrefixStatusbar         = "statusbar-decoration:"
	settingsActionPrefixSwitch            = "switch:"
	settingsActionPrefixUpdate            = "update:"
	settingsActionPrefixWorkdir           = "workdir:"
	settingsProjectAdd                    = "project:add"
	settingsProjectPins                   = "project:pins"
	settingsProjectRootManage             = "project-root:manage"
	settingsProjdirClear                  = "projdir:clear"
	settingsProjdirSetCurrent             = "projdir:set-current"
	settingsProjdirSetTyped               = "projdir:set-typed"
	settingsUpdateApply                   = "update:apply"
	settingsUpdateCheck                   = "update:check"
	settingsWorkdirAdd                    = "workdir:add"
	settingsWorkdirList                   = "workdir:list"
	settingsWorkdirTyped                  = "workdir:typed"
	settingsKeybindingsBindings           = "keybindings:bindings"
	settingsKeybindingsDiagnostic         = "keybindings:diagnostic"
	settingsKeybindingsProbe              = "keybindings:probe"
	settingsKeybindingsInit               = "keybindings:init"
	settingsAIDefaultMode                 = "ai-default-mode"
	settingsLabsDesktopNotify             = "labs:desktop-notify"
	settingsLabKeybindings                = "labs:keybindings"
	settingsWelcomeShow                   = "welcome:show"
	settingsKeymapFieldPlain              = "plain"
	settingsKeymapFieldPrefix             = "prefix"
)

func newSettingsCommand(ai *aiCommand, switcher *switchCommand, update *updateCommand) *settingsCommand {
	return &settingsCommand{
		ai:           ai,
		switcher:     switcher,
		update:       update,
		nativePicker: intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout},
		homeDir:      os.UserHomeDir,
		lookupEnv:    os.Getenv,
		runCommand: func(name string, args ...string) error {
			return exec.Command(name, args...).Run()
		},
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
	if section == settingsSectionAI {
		return c.runAISection(stdout, stderr)
	}
	if section == settingsSectionKeybindings {
		return c.runKeybindingsSection(stdout, stderr)
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
	result, err := runPickerOptionBackend(c.lookupEnv, c.nativePicker, c.runner, options)
	if err != nil {
		if isNoSelectionExit(err) {
			return intpickercompat.Result{}, errSettingsClosed
		}
		return intpickercompat.Result{}, fmt.Errorf("run settings picker: %w", err)
	}
	return result, nil
}

func (c *settingsCommand) rootOptions(tab settingsRootTab) intpickercompat.Options {
	if tab != settingsRootTabProject {
		tab = settingsRootTabGlobal
	}
	ctx := c.resolveSettingsProjectContext()
	return intpickercompat.Options{
		UI:         "settings",
		Entries:    c.rootEntriesForTab(tab),
		Title:      "Settings",
		TitleChips: settingsRootTabChips(tab, ctx.hasProject()),
		Prompt:     settingsRootPrompt(tab),
		Header:     settingsRootContextHeader(tab, ctx),
		Footer:     projmuxFooter("Enter: open  |  Alt-Shift-Left/Alt-Shift-Right or click chip: switch tab  |  Esc/Alt+5/Ctrl+Alt+S: close"),
		ExpectKeys: []string{"enter", "ctrl-g", "ctrl-p", "alt-shift-left", "alt-shift-right"},
		Bindings:   settingsCloseBindings(),
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
	return []projmuxpicker.Chip{
		{
			Label:      "Global",
			Active:     active == settingsRootTabGlobal,
			ClickValue: settingsRootTabGlobalValue,
		},
		{
			Label:      "Project",
			Active:     active == settingsRootTabProject,
			Disabled:   !hasProject,
			ClickValue: settingsRootTabProjectValue,
		},
	}
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
	if tab == settingsRootTabProject {
		return "Settings > Project > "
	}
	return "Settings > "
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
	if tab == settingsRootTabProject {
		return c.projectTabEntries()
	}
	return c.rootEntriesForAxis(settingsAxisGlobal)
}

func (c *settingsCommand) rootEntriesForAxis(axis SettingsAxis) []intpickercompat.Entry {
	all := []intpickercompat.Entry{
		{
			Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Project Picker", "project roots, workdirs, and pins"),
			Value: settingsSectionProject,
		},
		{
			Label: settingsLabel(settingsGlyphOpen, settingsColorType, "AI Settings", "default split mode"),
			Value: settingsSectionAI,
		},
		{
			Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Hooks", "global lifecycle hook paths"),
			Value: settingsSectionGlobalHooks,
		},
		{
			Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Appearance", "status and popup decoration mode"),
			Value: settingsSectionStatusbar,
		},
		{
			Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Keybindings", "edit tmux plain and prefix chords"),
			Value: settingsSectionKeybindings,
		},
		{
			Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Labs", "experimental picker engine"),
			Value: settingsSectionLabs,
		},
		{
			Label: settingsLabel(settingsGlyphOpen, settingsColorType, "About", "version, updates, key setup"),
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
				Label: settingsLabelDim("Trust", "disabled - no project context"),
				Value: settingsNoopValue,
			},
			{
				Label: settingsLabelDim("Hooks (project)", "disabled - no project context"),
				Value: settingsNoopValue,
			},
			{
				Label:     settingsLabelDim("Project recipe", "disabled - no project context"),
				Value:     settingsNoopValue,
				SearchKey: "Project recipe config.toml",
			},
			{
				Label: settingsLabelDim("Effective merge view", "disabled - no project context"),
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
			Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Hooks (project)", filepath.Join(ctx.Path, ".projmux")),
			Value: settingsSectionProjectHooks,
		},
		{
			Label:     settingsLabel(settingsGlyphOpen, settingsColorType, "Project recipe", "declare env, kube, startup"),
			Value:     settingsSectionProjectConfig,
			SearchKey: "Project recipe config.toml project config env kube startup",
		},
		{
			Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Effective merge view", "global + project merge with source labels"),
			Value: settingsSectionEffectiveMerge,
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
			Footer:     projmuxFooter("Enter: open  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
		}, nil
	case settingsSectionGlobalHooks:
		return intpickercompat.Options{
			UI:         "settings-hooks-global",
			Entries:    c.globalHookEntries(),
			Title:      "Hooks - Global lifecycle hook paths",
			Prompt:     "Settings > Hooks > ",
			Footer:     projmuxFooter("Enter: edit/add  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
		}, nil
	case settingsSectionProjectHooks:
		ctx := c.resolveSettingsProjectContext()
		return intpickercompat.Options{
			UI:         "settings-hooks-project",
			Entries:    c.projectHookEntries(ctx),
			Title:      "Hooks - Project lifecycle hook paths",
			Prompt:     "Settings > Project > Hooks > ",
			Footer:     projmuxFooter("Enter: edit/add  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
		}, nil
	case settingsSectionProject:
		return intpickercompat.Options{
			UI:         "settings-project-picker",
			Entries:    c.projectPickerEntries(),
			Title:      "Project Picker - Project roots, workdirs, and pinned projects",
			Prompt:     "Settings > Project Picker > ",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
		}, nil
	case settingsSectionStatusbar:
		return intpickercompat.Options{
			UI:         "settings-statusbar",
			Entries:    c.statusbarEntries(),
			Title:      "Appearance - Status and popup decoration mode",
			Prompt:     "Settings > Appearance > ",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
		}, nil
	case settingsSectionKeybindings:
		return c.keybindingsOptions(settingsKeybindingsBindings), nil
	case settingsSectionLabs:
		return intpickercompat.Options{
			UI:         "settings-labs",
			Entries:    c.labsEntries(),
			Title:      "Labs - Experimental features",
			Prompt:     "Settings > Labs > ",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
		}, nil
	case settingsSectionAbout:
		return intpickercompat.Options{
			UI:         "settings-about",
			Entries:    c.aboutEntries(),
			Title:      "About - Version, updates, key setup",
			Prompt:     "Settings > About > ",
			Footer:     projmuxFooter("Enter: action  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
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
	entries = append([]intpickercompat.Entry{settingsBackEntry()}, entries...)

	result, err := c.runPicker(intpickercompat.Options{
		UI:         "settings-project-add",
		Entries:    entries,
		Title:      "Add Project - Choose a filesystem directory",
		Prompt:     "Settings > Project Picker > Add Project > ",
		Footer:     projmuxFooter("Enter: add  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
		ExpectKeys: []string{"enter"},
		Bindings:   settingsCloseBindings(),
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
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
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
		Footer:       projmuxFooter("Enter: save  |  Esc/Alt+5/Ctrl+Alt+S: close"),
		ExpectKeys:   []string{"enter"},
		Bindings:     settingsCloseBindings(),
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
	entries = append([]intpickercompat.Entry{
		settingsBackEntry(),
		settingsWorkdirTypedEntry(),
	}, entries...)

	result, err := c.runPicker(intpickercompat.Options{
		UI:         "settings-workdir-add",
		Entries:    entries,
		Title:      "Add Workdir - Choose or type a directory to scan",
		Prompt:     "Settings > Project Picker > Add Workdir > ",
		Footer:     projmuxFooter("Enter: add  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
		ExpectKeys: []string{"enter"},
		Bindings:   settingsCloseBindings(),
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
	return intpickercompat.Entry{
		Label: settingsLabel(settingsGlyphType, settingsColorType, "Type path manually...", "skip filesystem scan"),
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
		Footer:      projmuxFooter("Enter: add  |  Esc/Alt+5/Ctrl+Alt+S: close"),
		ExpectKeys:  []string{"enter"},
		Bindings:    settingsCloseBindings(),
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
			Footer:     projmuxFooter("Enter: open/add/remove  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
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
	entries := []intpickercompat.Entry{settingsBackEntry()}
	if c.switcher == nil {
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelDim("Saved workdirs", "unavailable"),
			Value: settingsNoopValue,
		}), nil
	}

	saved, err := c.switcher.loadSavedWorkdirs()
	if err != nil {
		return nil, err
	}

	if len(saved) == 0 {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("Saved workdirs", "(none)", "~/.config/projmux/workdirs"),
			Value: settingsNoopValue,
		})
	} else {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("Saved workdirs", strconv.Itoa(len(saved)), "~/.config/projmux/workdirs"),
			Value: settingsNoopValue,
		})
		for _, dir := range saved {
			entries = append(entries, intpickercompat.Entry{
				Label: settingsLabel(settingsGlyphRemove, settingsColorRemove, "Remove", dir+"  (saved)"),
				Value: settingsActionPrefixWorkdir + "remove:" + dir,
			})
		}
	}

	for _, src := range c.switcher.envWorkdirSources() {
		if strings.TrimSpace(src.Value) == "" {
			continue
		}
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo(src.Name, src.Value, "env, read-only"),
			Value: settingsNoopValue,
		})
	}
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Add Workdir...", "append a directory to the saved workdirs list"),
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
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
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
	entries := []intpickercompat.Entry{
		settingsBackEntry(),
	}

	entries = append(entries, c.projectRootEntry())
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Workdirs", "add or remove scan roots"),
		Value: settingsWorkdirList,
	})
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Pinned Projects", "add or remove pins"),
		Value: settingsProjectPins,
	})
	return entries
}

// projectRootEntry renders the resolved primary root with its source label.
// Opening it manages the saved project root; rendering never memoizes env state.
func (c *settingsCommand) projectRootEntry() intpickercompat.Entry {
	if c.switcher == nil {
		return intpickercompat.Entry{
			Label: settingsLabelDim("Project Root", "unavailable"),
			Value: settingsNoopValue,
		}
	}
	value, source, err := c.switcher.currentProjdirInfo()
	if err != nil || value == "" {
		return intpickercompat.Entry{
			Label: settingsLabelDim("Project Root", "not configured"),
			Value: settingsProjectRootManage,
		}
	}
	return intpickercompat.Entry{
		Label: settingsLabelInfo("Project Root", value, source),
		Value: settingsProjectRootManage,
	}
}

func (c *settingsCommand) projectRootHintEntry() intpickercompat.Entry {
	// Keep the entire hint in one dim run so search substrings such as
	// "Set PROJMUX_PROJDIR" stay contiguous in the rendered label.
	return intpickercompat.Entry{
		Label: "  " + settingsColorDim + "Project Root is the primary root. Workdirs are extra search roots. Set PROJMUX_PROJDIR, @projmux_projdir, or the saved ~/.config/projmux/projdir value." + settingsColorReset,
		Value: settingsNoopValue,
	}
}

func (c *settingsCommand) projectRootEntries() ([]intpickercompat.Entry, error) {
	entries := []intpickercompat.Entry{settingsBackEntry()}
	if c.switcher == nil {
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelDim("Project Root", "unavailable"),
			Value: settingsNoopValue,
		}), nil
	}

	info, err := c.switcher.projdirSettingsInfo()
	if err != nil {
		return nil, err
	}

	if info.EffectiveValue == "" {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("Effective Project Root", "not configured", "no env, tmux option, or saved value"),
			Value: settingsNoopValue,
		})
	} else {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("Effective Project Root", info.EffectiveValue, info.EffectiveSource),
			Value: settingsNoopValue,
		})
	}

	switch {
	case info.SavedValue == "":
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("Saved Project Root", "not set", "~/.config/projmux/projdir"),
			Value: settingsNoopValue,
		})
	case info.EffectiveSource == projdirSourceSaved:
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("Saved Project Root", info.SavedValue, "active"),
			Value: settingsNoopValue,
		})
	case info.EffectiveSource == projdirSourceUnresolved:
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("Saved Project Root", info.SavedValue, "saved"),
			Value: settingsNoopValue,
		})
	default:
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("Saved Project Root", info.SavedValue, "shadowed by "+info.EffectiveSource),
			Value: settingsNoopValue,
		})
	}

	entries = append(entries,
		intpickercompat.Entry{
			Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Set Project Root...", "save one primary root path directly"),
			Value: settingsProjdirSetTyped,
		},
		c.setCurrentProjectRootEntry(),
		intpickercompat.Entry{
			Label: settingsLabel(settingsGlyphRemove, settingsColorRemove, "Clear Saved Project Root", "remove ~/.config/projmux/projdir"),
			Value: settingsProjdirClear,
		},
		c.projectRootHintEntry(),
		intpickercompat.Entry{
			Label: "  " + settingsColorDim + "Env PROJMUX_PROJDIR and tmux @projmux_projdir override the saved value until unset." + settingsColorReset,
			Value: settingsNoopValue,
		},
	)
	return entries, nil
}

func (c *settingsCommand) setCurrentProjectRootEntry() intpickercompat.Entry {
	if c.switcher == nil {
		return intpickercompat.Entry{
			Label: settingsLabelDim("Use Current Project as Root", "unavailable"),
			Value: settingsNoopValue,
		}
	}

	homeDir, err := c.switcher.resolveHomeDir()
	if err != nil {
		return intpickercompat.Entry{
			Label: settingsLabelDim("Use Current Project as Root", "home unavailable"),
			Value: settingsNoopValue,
		}
	}
	repoRoot, _, _ := c.switcher.currentProjdirInfo()
	currentTarget, err := c.switcher.resolveSwitchTargetNoMemoize(nil, "settings project root")
	if err != nil || currentTarget == "" || currentTarget == switchSettingsSentinel {
		return intpickercompat.Entry{
			Label: settingsLabelDim("Use Current Project as Root", "no project context"),
			Value: settingsNoopValue,
		}
	}
	return intpickercompat.Entry{
		Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Use Current Project as Root", intrender.PrettyPath(currentTarget, homeDir, repoRoot)),
		Value: settingsProjdirSetCurrent,
	}
}

func (c *settingsCommand) addCurrentProjectEntry() intpickercompat.Entry {
	if c.switcher == nil {
		return intpickercompat.Entry{
			Label: settingsLabelDim("Add Current Project", "unavailable"),
			Value: settingsNoopValue,
		}
	}

	pins, err := c.switcher.loadPins()
	if err != nil {
		return intpickercompat.Entry{
			Label: settingsLabelDim("Add Current Project", "pins unavailable"),
			Value: settingsNoopValue,
		}
	}
	homeDir, err := c.switcher.resolveHomeDir()
	if err != nil {
		return intpickercompat.Entry{
			Label: settingsLabelDim("Add Current Project", "home unavailable"),
			Value: settingsNoopValue,
		}
	}
	repoRoot := c.switcher.switchRepoRoot(homeDir)
	currentTarget, err := c.switcher.resolveSwitchTarget(nil, "settings project picker")
	if err != nil || currentTarget == "" || currentTarget == switchSettingsSentinel {
		return intpickercompat.Entry{
			Label: settingsLabelDim("Add Current Project", "no project context"),
			Value: settingsNoopValue,
		}
	}
	if containsString(pins, currentTarget) {
		return intpickercompat.Entry{
			Label: settingsLabelDim("Add Current Project", "already pinned  "+intrender.PrettyPath(currentTarget, homeDir, repoRoot)),
			Value: settingsNoopValue,
		}
	}
	return intpickercompat.Entry{
		Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Add Current Project", intrender.PrettyPath(currentTarget, homeDir, repoRoot)),
		Value: settingsActionPrefixSwitch + "add:" + currentTarget,
	}
}

func (c *settingsCommand) pinnedProjectEntries() ([]intpickercompat.Entry, error) {
	entries := []intpickercompat.Entry{
		settingsBackEntry(),
		{
			Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Add Project...", "scan filesystem roots"),
			Value: settingsProjectAdd,
		},
		c.addCurrentProjectEntry(),
	}
	if c.switcher == nil {
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelDim("(no pinned projects)", ""),
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
			Label: settingsLabelDim("(no pinned projects)", ""),
			Value: settingsNoopValue,
		}), nil
	}

	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabel(settingsGlyphRemove, settingsColorRemove, "Clear all pins", ""),
		Value: settingsActionPrefixSwitch + "clear",
	})
	for _, pin := range pins {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabel(settingsGlyphRemove, settingsColorRemove, "Remove", intrender.PrettyPath(pin, homeDir, repoRoot)),
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
		case strings.HasPrefix(action, settingsActionPrefixAI):
			if err := c.execute(action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown AI settings action: %s", action)
		}
	}
}

func (c *settingsCommand) runAIDefaultModeSection(stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-ai-default-mode",
			Entries:    c.aiEntries(),
			Title:      "AI Settings - Default Ctrl+Shift+R/L split mode",
			Prompt:     "Settings > AI Settings > Default split mode > ",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
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
	entries := []intpickercompat.Entry{settingsBackEntry()}
	if c.ai == nil {
		return entries
	}
	current := c.ai.getMode()
	return append(entries, intpickercompat.Entry{
		Label:     settingsLabel(settingsGlyphOpen, settingsColorType, "Default split mode", current),
		Value:     settingsAIDefaultMode,
		SearchKey: "default split mode claude codex shell selective",
	})
}

func (c *settingsCommand) aiEntries() []intpickercompat.Entry {
	if c.ai == nil {
		return nil
	}

	current := c.ai.getMode()
	modes := []struct {
		mode string
		desc string
	}{
		{aiModeSelective, "show picker each time"},
		{aiModeClaude, "always run Claude split"},
		{aiModeCodex, "always run Codex split"},
		{aiModeShell, "always open plain shell split"},
	}

	entries := make([]intpickercompat.Entry, 0, len(modes)+1)
	entries = append(entries, settingsBackEntry())
	for _, item := range modes {
		glyph := settingsGlyphInactive
		color := settingsColorDim
		if item.mode == current {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
		}
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabel(glyph, color, item.mode, item.desc),
			Value: settingsActionPrefixAI + item.mode,
		})
	}
	return entries
}

func (c *settingsCommand) statusbarEntries() []intpickercompat.Entry {
	current := c.currentStatusbarDecoration()
	modes := []struct {
		mode config.StatusbarDecoration
		desc string
	}{
		{config.StatusbarDecorationOff, "no status or popup icon prefix; safest for all fonts"},
		{config.StatusbarDecorationSymbol, "Nerd Font-style status and notification icons"},
		{config.StatusbarDecorationEmoji, "emoji status and notification icons"},
	}

	entries := make([]intpickercompat.Entry, 0, len(modes)+1)
	entries = append(entries, settingsBackEntry())
	for _, item := range modes {
		glyph := settingsGlyphInactive
		color := settingsColorDim
		if item.mode == current {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
		}
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabel(glyph, color, string(item.mode), item.desc),
			Value: settingsActionPrefixStatusbar + string(item.mode),
		})
	}
	return entries
}

func (c *settingsCommand) keybindingsOptions(active string) intpickercompat.Options {
	active = normalizeKeybindingsTab(active)
	entries, err := c.keybindingsTabEntries(active)
	if err != nil {
		entries = []intpickercompat.Entry{
			settingsBackEntry(),
			{
				Label: settingsLabelDim("Keymap error", err.Error()),
				Value: settingsNoopValue,
			},
		}
	}
	return intpickercompat.Options{
		UI:         "settings-keybindings",
		Entries:    entries,
		Title:      "Keybindings",
		TitleChips: keybindingsTitleChips(active),
		Prompt:     "Settings > Keybindings > ",
		Footer:     projmuxFooter("Enter: edit/apply  |  click chip: switch view  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
		ExpectKeys: []string{"enter"},
		Bindings:   settingsCloseBindings(),
	}
}

func normalizeKeybindingsTab(active string) string {
	switch active {
	case settingsKeybindingsBindings, settingsKeybindingsDiagnostic, settingsKeybindingsProbe, settingsKeybindingsInit:
		return active
	default:
		return settingsKeybindingsBindings
	}
}

func keybindingsTitleChips(active string) []projmuxpicker.Chip {
	return []projmuxpicker.Chip{
		{Label: "Bindings", Active: active == settingsKeybindingsBindings, ClickValue: settingsKeybindingsBindings},
		{Label: "Diagnostic", Active: active == settingsKeybindingsDiagnostic, ClickValue: settingsKeybindingsDiagnostic},
		{Label: "Probe", Active: active == settingsKeybindingsProbe, ClickValue: settingsKeybindingsProbe},
		{Label: "Init", Active: active == settingsKeybindingsInit, ClickValue: settingsKeybindingsInit},
	}
}

func (c *settingsCommand) keybindingsTabEntries(active string) ([]intpickercompat.Entry, error) {
	switch normalizeKeybindingsTab(active) {
	case settingsKeybindingsBindings:
		return c.keybindingEntries()
	case settingsKeybindingsDiagnostic:
		return c.labKeybindingEntries()
	case settingsKeybindingsProbe:
		entries, err := c.labKeybindingEntries()
		if err != nil {
			return nil, err
		}
		if len(entries) > 1 {
			entries = append(entries[:1], append([]intpickercompat.Entry{{
				Label: settingsLabelInfo("Probe", "select an action, then press its key", "raw key delivery"),
				Value: settingsNoopValue,
			}}, entries[1:]...)...)
		}
		return entries, nil
	case settingsKeybindingsInit:
		terminal := detectTerminal(c.lookupEnv)
		entries := []intpickercompat.Entry{
			settingsBackEntry(),
			{Label: settingsLabelInfo("Terminal", terminal.Display(), labTerminalSupportSummary(terminal)), Value: settingsNoopValue},
			{Label: settingsLabelInfo("After fallback apply", terminal.ReloadCapability().Label, terminal.ReloadCapability().Summary), Value: settingsNoopValue},
		}
		if terminal.InitCommand() != "" {
			entries = append(entries,
				intpickercompat.Entry{
					Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Preview terminal fallback", strings.TrimSuffix(terminal.InitCommand(), " --apply")),
					Value: settingsKeybindingsInit + ":preview",
				},
				intpickercompat.Entry{
					Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Apply terminal fallback", terminal.InitCommand()),
					Value: settingsKeybindingsInit + ":apply",
				},
			)
		} else if hint := terminal.RemediationHint(); hint != "" {
			entries = append(entries, intpickercompat.Entry{
				Label: settingsLabelInfo("Manual fallback", hint, ""),
				Value: settingsNoopValue,
			})
		}
		return entries, nil
	default:
		return c.keybindingEntries()
	}
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
		if action == settingsKeybindingsBindings || action == settingsKeybindingsDiagnostic || action == settingsKeybindingsProbe || action == settingsKeybindingsInit {
			active = action
			continue
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
		if after, ok := strings.CutPrefix(action, settingsActionPrefixLabKeymap); ok {
			id := after
			if err := c.runLabKeybindingDetail(id, stdout, stderr); err != nil {
				return err
			}
			continue
		}
		if action == settingsKeybindingsInit+":preview" {
			if err := c.runLabTerminalInit(false, stdout, stderr); err != nil {
				return err
			}
			continue
		}
		if action == settingsKeybindingsInit+":apply" {
			if err := c.runLabTerminalInit(true, stdout, stderr); err != nil {
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
			Footer:     projmuxFooter("Enter: edit/apply  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
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
		field, op, ok := parseKeymapDetailAction(action, actionID)
		if !ok {
			return fmt.Errorf("unknown keybinding detail action: %s", action)
		}
		switch op {
		case "set":
			if err := c.runKeybindingTyped(actionID, field, stdout, stderr); err != nil {
				return err
			}
		case "disable":
			disabled := ""
			if err := c.saveKeymapAndApply(actionID, field, &disabled, stdout); err != nil {
				return err
			}
		case "reset":
			if err := c.saveKeymapAndApply(actionID, field, nil, stdout); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown keybinding operation: %s", op)
		}
	}
}

func parseKeymapDetailAction(value, actionID string) (string, string, bool) {
	prefix := settingsActionPrefixKeymap + actionID + ":"
	if !strings.HasPrefix(value, prefix) {
		return "", "", false
	}
	field, op, ok := strings.Cut(strings.TrimPrefix(value, prefix), ":")
	if !ok {
		return "", "", false
	}
	if field != settingsKeymapFieldPlain && field != settingsKeymapFieldPrefix {
		return "", "", false
	}
	return field, op, op == "set" || op == "disable" || op == "reset"
}

func (c *settingsCommand) runKeybindingTyped(actionID, field string, stdout, stderr io.Writer) error {
	_, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return err
	}
	action, ok := keyBindingActionByID(actions, actionID)
	if !ok {
		return fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	initial := action.PlainChord
	if field == settingsKeymapFieldPrefix {
		initial = action.PrefixChord
	}
	result, err := c.runPicker(intpickercompat.Options{
		UI:           "settings-keybinding-typed",
		Entries:      nil,
		AcceptQuery:  true,
		InitialQuery: initial,
		Title:        "Set Keybinding - Type a tmux chord string",
		Prompt:       "Type tmux chord > ",
		Footer:       projmuxFooter("Enter: save empty disables  |  Esc/Alt+5/Ctrl+Alt+S: close"),
		ExpectKeys:   []string{"enter"},
		Bindings:     settingsCloseBindings(),
	})
	if err != nil {
		return err
	}
	if result.Key != "enter" {
		return nil
	}
	typed := strings.TrimSpace(result.Query)
	if err := validateKeymapChord(typed); err != nil {
		fmt.Fprintf(stderr, "invalid keybinding chord: %v\n", err)
		return nil
	}
	return c.saveKeymapAndApply(actionID, field, &typed, stdout)
}

func (c *settingsCommand) keybindingEntries() ([]intpickercompat.Entry, error) {
	_, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return nil, err
	}
	defaults := defaultKeyBindingCatalog()
	entries := make([]intpickercompat.Entry, 0, len(actions)+2)
	entries = append(entries, settingsBackEntry())
	entries = append(entries, intpickercompat.Entry{
		Label: "  " + settingsColorDim + "Terminal fallback mappings still require rerunning projmux init and restarting the terminal where applicable." + settingsColorReset,
		Value: settingsNoopValue,
	})
	for _, action := range actions {
		defaultAction, _ := keyBindingActionByID(defaults, action.ID)
		plain := keybindingValueSummary(action.PlainChord, defaultAction.PlainChord)
		prefix := keybindingValueSummary(action.PrefixChord, defaultAction.PrefixChord)
		desc := strings.TrimSpace("plain " + plain + "  prefix " + prefix)
		entries = append(entries, intpickercompat.Entry{
			Label:     settingsLabel(settingsGlyphOpen, settingsColorType, action.Description, desc),
			Value:     settingsActionPrefixKeymap + action.ID,
			SearchKey: action.ID + " " + action.Description + " " + action.PlainChord + " " + action.PrefixChord,
		})
	}
	return entries, nil
}

func (c *settingsCommand) keybindingDetailEntries(actionID string) ([]intpickercompat.Entry, string, error) {
	_, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return nil, "", err
	}
	action, ok := keyBindingActionByID(actions, actionID)
	if !ok {
		return nil, "", fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	defaultAction, _ := keyBindingActionByID(defaultKeyBindingCatalog(), actionID)
	entries := []intpickercompat.Entry{
		settingsBackEntry(),
		{
			Label: settingsLabelInfo("Action ID", action.ID, ""),
			Value: settingsNoopValue,
		},
		{
			Label: "  " + settingsColorDim + "Terminal fallback mappings still require rerunning projmux init and restarting the terminal where applicable." + settingsColorReset,
			Value: settingsNoopValue,
		},
	}
	entries = append(entries, keybindingFieldEntries(action, defaultAction, settingsKeymapFieldPlain, "Plain chord")...)
	entries = append(entries, keybindingFieldEntries(action, defaultAction, settingsKeymapFieldPrefix, "Prefix chord")...)
	title := "Keybinding - " + action.Description
	return entries, title, nil
}

func keybindingFieldEntries(action, defaultAction keyBindingAction, field, label string) []intpickercompat.Entry {
	current := action.PlainChord
	def := defaultAction.PlainChord
	if field == settingsKeymapFieldPrefix {
		current = action.PrefixChord
		def = defaultAction.PrefixChord
	}
	value := current
	source := "default"
	if current == "" {
		value = "disabled"
	}
	if current != def {
		source = "keymap.toml"
	}
	prefix := settingsActionPrefixKeymap + action.ID + ":" + field + ":"
	return []intpickercompat.Entry{
		{
			Label: settingsLabelInfo(label, value, source),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabel(settingsGlyphType, settingsColorType, "Set "+label+"...", "type a tmux chord; empty disables"),
			Value: prefix + "set",
		},
		{
			Label: settingsLabel(settingsGlyphRemove, settingsColorRemove, "Disable "+label, "write empty string override"),
			Value: prefix + "disable",
		},
		{
			Label: settingsLabel(settingsGlyphBack, settingsColorBack, "Reset "+label, "remove override and use default"),
			Value: prefix + "reset",
		},
	}
}

func keybindingValueSummary(current, def string) string {
	if current == "" {
		return "(disabled)"
	}
	if current != def {
		return current + " (custom)"
	}
	return current
}

func (c *settingsCommand) keymapStore() keymapStore {
	return keymapStore{
		homeDir:   c.homeDir,
		lookupEnv: c.lookupEnv,
	}
}

func (c *settingsCommand) saveKeymapAndApply(actionID, field string, value *string, stdout io.Writer) error {
	path, err := saveKeymapOverride(c.keymapStore(), actionID, field, value)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "wrote %s\n", path); err != nil {
		return err
	}
	configPath, err := c.writeTmuxAppConfig()
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "wrote %s\n", configPath); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		if err := c.runCommand("tmux", "source-file", configPath); err != nil {
			return fmt.Errorf("source live tmux config: %w", err)
		}
		_, err = fmt.Fprintf(stdout, "reloaded tmux config\n")
		return err
	}
	_, err = fmt.Fprintf(stdout, "saved keymap; no live tmux reload outside TMUX\n")
	return err
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
			if err := c.runKeybindingsSectionWithActive(settingsKeybindingsDiagnostic, stdout, stderr); err != nil {
				return err
			}
		case action == settingsLabsDesktopNotify:
			if err := c.runLabsDesktopNotifySection(stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixHooks):
			if err := c.execute(action, stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixDesktopNotifyMode):
			if err := c.execute(action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown labs settings action: %s", action)
		}
	}
}

func (c *settingsCommand) runLabsDesktopNotifySection(stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-labs-desktop-notify",
			Entries:    c.labsDesktopNotifyEntries(),
			Title:      "Labs - Desktop notifications",
			Prompt:     "Settings > Labs > Desktop notifications > ",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
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

func (c *settingsCommand) runLabKeybindingsSection(stdout, stderr io.Writer) error {
	for {
		entries, err := c.labKeybindingEntries()
		if err != nil {
			entries = []intpickercompat.Entry{
				settingsBackEntry(),
				{
					Label: settingsLabelDim("Keymap error", err.Error()),
					Value: settingsNoopValue,
				},
			}
		}
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-lab-keybindings",
			Entries:    entries,
			Title:      "Keybinding Lab - Diagnose delivery",
			Prompt:     "Settings > Labs > Keybindings > ",
			Footer:     projmuxFooter("Enter: diagnose  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
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
		if id, ok := strings.CutPrefix(action, settingsActionPrefixLabKeymap); ok {
			if err := c.runLabKeybindingDetail(id, stdout, stderr); err != nil {
				return err
			}
			continue
		}
		return fmt.Errorf("unknown lab keybinding action: %s", action)
	}
}

func (c *settingsCommand) runLabKeybindingDetail(actionID string, stdout, stderr io.Writer) error {
	for {
		entries, title, err := c.labKeybindingDetailEntries(actionID)
		if err != nil {
			return err
		}
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-lab-keybinding-detail",
			Entries:    entries,
			Title:      title,
			Prompt:     "Settings > Labs > Keybindings > Action > ",
			Footer:     projmuxFooter("Enter: probe/apply  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
		})
		if err != nil {
			return err
		}
		value := strings.TrimSpace(result.Value)
		if result.Key != "enter" || value == "" {
			return errSettingsClosed
		}
		if value == settingsBackValue {
			return nil
		}
		if value == settingsNoopValue {
			continue
		}
		op, ok := parseLabKeybindingAction(value, actionID)
		if !ok {
			return fmt.Errorf("unknown lab keybinding detail action: %s", value)
		}
		switch op {
		case "probe":
			key, err := c.labProbeKey(actionID)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "press %s for %s\n", key.Label, key.Action)
			res, err := c.probeLabKeybinding(key, defaultProbeTimeout)
			if err != nil {
				return err
			}
			if c.lastLabProbe == nil {
				c.lastLabProbe = map[string]probeResult{}
			}
			c.lastLabProbe[actionID] = res
			fmt.Fprintf(stdout, "probe %s: %s\n", key.Label, renderProbeStatus(res))
		case "init-preview":
			if err := c.runLabTerminalInit(false, stdout, stderr); err != nil {
				return err
			}
		case "init-apply":
			if err := c.runLabTerminalInit(true, stdout, stderr); err != nil {
				return err
			}
		case "save-plain":
			if err := c.saveKeymapAndApply(actionID, settingsKeymapFieldPlain, nil, stdout); err != nil {
				return err
			}
		default:
			if chord, ok := strings.CutPrefix(op, "save-plain-override:"); ok {
				if err := c.saveKeymapAndApply(actionID, settingsKeymapFieldPlain, &chord, stdout); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("unknown lab keybinding operation: %s", op)
		}
	}
}

func parseLabKeybindingAction(value, actionID string) (string, bool) {
	prefix := settingsActionPrefixLabKeymap + actionID + ":"
	if !strings.HasPrefix(value, prefix) {
		return "", false
	}
	op := strings.TrimPrefix(value, prefix)
	switch op {
	case "probe", "init-preview", "init-apply", "save-plain":
		return op, true
	default:
		if strings.HasPrefix(op, "save-plain-override:") && strings.TrimPrefix(op, "save-plain-override:") != "" {
			return op, true
		}
		return "", false
	}
}

func (c *settingsCommand) labKeybindingEntries() ([]intpickercompat.Entry, error) {
	_, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return nil, err
	}
	terminal := detectTerminal(c.lookupEnv)
	keys := probeKeysFromActions(actions)
	entries := make([]intpickercompat.Entry, 0, len(keys)+3)
	entries = append(entries, settingsBackEntry())
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabelInfo("Terminal", terminal.Display(), labTerminalSupportSummary(terminal)),
		Value: settingsNoopValue,
	})
	for _, key := range keys {
		action, ok := keyBindingActionByID(actions, key.ActionID)
		if !ok {
			continue
		}
		defaultAction, _ := keyBindingActionByID(defaultKeyBindingCatalog(), action.ID)
		desc := strings.TrimSpace(action.Description + "  plain " + keybindingValueSummary(action.PlainChord, defaultAction.PlainChord))
		if key.UserKey != "" {
			desc += "  " + key.UserKey
		}
		entries = append(entries, intpickercompat.Entry{
			Label:     settingsLabel(settingsGlyphOpen, settingsColorType, key.Label, desc),
			Value:     settingsActionPrefixLabKeymap + key.ActionID,
			SearchKey: key.ActionID + " " + key.Label + " " + key.Action + " " + action.Description,
		})
	}
	return entries, nil
}

func (c *settingsCommand) labKeybindingDetailEntries(actionID string) ([]intpickercompat.Entry, string, error) {
	_, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return nil, "", err
	}
	action, ok := keyBindingActionByID(actions, actionID)
	if !ok {
		return nil, "", fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	key, err := c.labProbeKeyFromActions(actionID, actions)
	if err != nil {
		return nil, "", err
	}
	defaultAction, _ := keyBindingActionByID(defaultKeyBindingCatalog(), actionID)
	terminal := detectTerminal(c.lookupEnv)
	prefix := settingsActionPrefixLabKeymap + actionID + ":"
	entries := []intpickercompat.Entry{
		settingsBackEntry(),
		{
			Label: settingsLabelInfo("Action ID", action.ID, ""),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabelInfo("Probe key", key.Label, key.Action),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabelInfo("Terminal", terminal.Display(), labTerminalSupportSummary(terminal)),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabelInfo("After fallback apply", terminal.ReloadCapability().Label, terminal.ReloadCapability().Summary),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabelInfo("Plain chord", keybindingValueSummary(action.PlainChord, defaultAction.PlainChord), "tmux"),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabel(settingsGlyphType, settingsColorType, "Press the key", "read one raw keypress from /dev/tty"),
			Value: prefix + "probe",
		},
	}
	if terminal.InitCommand() != "" {
		entries = append(entries,
			intpickercompat.Entry{
				Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Preview terminal fallback", strings.TrimSuffix(terminal.InitCommand(), " --apply")),
				Value: prefix + "init-preview",
			},
			intpickercompat.Entry{
				Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Apply terminal fallback", terminal.InitCommand()),
				Value: prefix + "init-apply",
			},
		)
	} else if hint := terminal.RemediationHint(); hint != "" {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("Manual fallback", hint, ""),
			Value: settingsNoopValue,
		})
	}
	if res, ok := c.lastLabProbe[actionID]; ok {
		entries = append(entries, labProbeOutcomeEntries(prefix, action, defaultAction, res, terminal)...)
	}
	return entries, "Keybindings - " + action.Description, nil
}

func labProbeOutcomeEntries(prefix string, action, defaultAction keyBindingAction, res probeResult, terminal terminalInfo) []intpickercompat.Entry {
	entries := []intpickercompat.Entry{
		{
			Label: settingsLabelInfo("Probe result", string(res.Status), renderProbeStatus(res)),
			Value: settingsNoopValue,
		},
	}
	switch res.Status {
	case probeStatusPlain:
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("Plain key reached", "tmux-level binding can work immediately", res.Reason),
			Value: settingsNoopValue,
		})
		if defaultAction.PlainChord != "" && action.PlainChord != defaultAction.PlainChord {
			entries = append(entries, intpickercompat.Entry{
				Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Save plain tmux binding", "reset keymap.toml to "+defaultAction.PlainChord+" and reload app config"),
				Value: prefix + "save-plain",
			})
		}
	case probeStatusCSIu:
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("CSI-u reached", "already routed through terminal fallback", res.Key.UserKey),
			Value: settingsNoopValue,
		})
	case probeStatusUnknown:
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("Unexpected sequence", visibleEscape(string(res.Sequence)), "no keymap overwrite"),
			Value: settingsNoopValue,
		})
		if chord, ok := suggestedPlainChordForSequence(res.Sequence); ok {
			entries = append(entries, intpickercompat.Entry{
				Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Save as plain override", "write plain = "+chord+" and reload app config"),
				Value: prefix + "save-plain-override:" + chord,
			})
		}
	case probeStatusTimeout:
		desc := "terminal fallback unavailable"
		if cmd := terminal.InitCommand(); cmd != "" {
			desc = cmd
		}
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("Timeout or swallowed", "no bytes reached projmux", desc),
			Value: settingsNoopValue,
		})
	}
	return entries
}

func (c *settingsCommand) labProbeKey(actionID string) (probeKey, error) {
	_, actions, _, _, err := loadKeymapForEdit(c.keymapStore())
	if err != nil {
		return probeKey{}, err
	}
	return c.labProbeKeyFromActions(actionID, actions)
}

func (c *settingsCommand) labProbeKeyFromActions(actionID string, actions []keyBindingAction) (probeKey, error) {
	for _, key := range probeKeysFromActions(actions) {
		if key.ActionID == actionID {
			return key, nil
		}
	}
	return probeKey{}, fmt.Errorf("keybinding action %s has no probe key", actionID)
}

func (c *settingsCommand) probeLabKeybinding(key probeKey, timeout time.Duration) (probeResult, error) {
	if c.probeKeybinding != nil {
		return c.probeKeybinding(key, timeout)
	}
	cmd := &setupCommand{openTTY: openControllingTTY}
	return cmd.probeControllingTTYKey(key, timeout)
}

func (c *settingsCommand) runLabTerminalInit(apply bool, stdout, stderr io.Writer) error {
	terminal := detectTerminal(c.lookupEnv)
	if terminal.InitCommand() == "" {
		return fmt.Errorf("keybinding lab: terminal fallback is not supported for %s", terminal.Display())
	}
	args := []string{terminal.Slug, "--dry-run"}
	if apply {
		args[1] = "--apply"
	}
	if c.runInitKeybindings != nil {
		return c.runInitKeybindings(args, stdout, stderr)
	}
	cmd := newInitCommand()
	cmd.getenv = c.lookupEnv
	return cmd.Run(args, stdout, stderr)
}

func labTerminalSupportSummary(terminal terminalInfo) string {
	activation := terminal.ReloadCapability()
	if cmd := terminal.InitCommand(); cmd != "" {
		return "supported fallback: " + strings.TrimSuffix(cmd, " --apply") + "; after apply: " + activation.Label
	}
	if hint := terminal.RemediationHint(); hint != "" {
		return hint + "; after apply: " + activation.Label
	}
	return "no automatic fallback adapter; after apply: " + activation.Label
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

func (c *settingsCommand) currentStatusbarDecoration() config.StatusbarDecoration {
	return loadStatusbarDecoration(c.homeDir, c.lookupEnv)
}

// setDesktopNotifyMode writes the user-facing 3-way choice into the
// `@projmux_desktop_notify_mode` global tmux user-option. The env
// variables (`PROJMUX_DESKTOP_NOTIFY_MODE`, plus the legacy
// `PROJMUX_DESKTOP_NOTIFY`) continue to take priority at resolve time, so
// toggling here when an env is set will appear to "do nothing" — the
// Settings info row surfaces the source so users see why.
//
// The legacy `@projmux_desktop_notify` option is intentionally NOT
// rewritten here. Read-time migration keeps honoring it for users who
// never opened the Settings row; once they pick a value via this code
// path the new option pins the resolution and the legacy one is
// effectively orphaned.
//
// When projmux runs outside tmux we silently skip the live update; the
// gate at `aiDesktopNotifier.Notify` only reads the option inside tmux
// anyway, so there's nothing to persist elsewhere.
func (c *settingsCommand) setDesktopNotifyMode(value string) error {
	mode, ok := parseDesktopNotifyMode(value)
	if !ok {
		return fmt.Errorf("unknown desktop notification mode: %s", value)
	}
	if c.lookupEnv == nil || strings.TrimSpace(c.lookupEnv("TMUX")) == "" {
		// Outside tmux there is no server to persist to. The toggle is
		// inherently a tmux-scoped surface (resolve order checks env
		// first, then this option, then default) so this isn't an
		// error — just a no-op with a friendly hint.
		return nil
	}
	if c.runCommand == nil {
		return errors.New("settings runner is not configured")
	}
	if err := c.runCommand("tmux", "set-option", "-g", desktopNotifyModeTmuxOption, string(mode)); err != nil {
		return fmt.Errorf("set live tmux desktop-notify-mode option: %w", err)
	}
	_ = c.runCommand("tmux", "display-message", "desktop notifications: "+string(mode))
	return nil
}

func (c *settingsCommand) setStatusbarDecoration(value string) error {
	mode := config.NormalizeStatusbarDecoration(value)
	paths, err := statusbarConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return err
	}
	if err := config.SaveStatusbarDecorationFile(paths.StatusbarDecorationFile(), mode); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		if err := c.runCommand("tmux", "set-option", "-g", statusbarDecorationTmuxOption, string(mode)); err != nil {
			return fmt.Errorf("set live tmux decoration mode: %w", err)
		}
		_ = c.runCommand("tmux", "display-message", "decoration mode: "+string(mode))
	}
	return nil
}

func (c *settingsCommand) labsEntries() []intpickercompat.Entry {
	current, source := c.currentPickerBackend()
	hookMode, hookSource := c.currentProjectHooksMode()
	notifyMode, notifySource := settingsDesktopNotifyResolver(c.lookupEnv).resolveMode()
	entries := make([]intpickercompat.Entry, 0, 6)
	entries = append(entries, settingsBackEntry())
	entries = append(entries, intpickercompat.Entry{
		Label:     settingsLabel(settingsGlyphOpen, settingsColorType, "Desktop notifications", string(notifyMode)+" - "+string(notifySource)),
		Value:     settingsLabsDesktopNotify,
		SearchKey: "Desktop notifications none notify raise toast osfocus",
	})
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabelInfo("Project hooks", string(hookMode), hookSource),
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
			Label: settingsLabel(glyph, color, "Project hooks "+string(item.mode), item.desc),
			Value: settingsActionPrefixHooks + string(item.mode),
		})
	}
	if source != "" {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("Picker source", string(current), source),
			Value: settingsNoopValue,
		})
	}
	return entries
}

func (c *settingsCommand) labsDesktopNotifyEntries() []intpickercompat.Entry {
	notifyMode, notifySource := settingsDesktopNotifyResolver(c.lookupEnv).resolveMode()
	entries := []intpickercompat.Entry{
		settingsBackEntry(),
		{
			Label: settingsLabelInfo("Desktop notifications", string(notifyMode), string(notifySource)),
			Value: settingsNoopValue,
		},
	}
	for _, item := range []struct {
		mode desktopNotifyMode
		desc string
	}{
		{desktopNotifyModeNone, "silence OS notifications; in-app notify queue is unaffected"},
		{desktopNotifyModeNotify, "fire toast / notify-send for AI reply-ready (click -> focus via projmux://)"},
		{desktopNotifyModeRaise, "fire toast and auto-raise host terminal via osfocus chain"},
	} {
		glyph := settingsGlyphInactive
		color := settingsColorDim
		if item.mode == notifyMode {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
		}
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabel(glyph, color, string(item.mode), item.desc),
			Value: settingsActionPrefixDesktopNotifyMode + string(item.mode),
		})
	}
	return entries
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
	status, statusErr := updateStatus{}, errors.New("update status is not configured")
	if c.update != nil {
		status, statusErr = c.update.status()
	}

	rows := []struct{ name, value string }{
		{"Version", "projmux " + version.String()},
		{"Source", "https://github.com/crevissepartners/projmux"},
		{"App", "sidebar, sessions, projects, AI picker, settings"},
		{"Tmux actions", "new window, rename window/pane, previous/next window"},
		{"Key setup", "Alt-1..5 work zero-config when the terminal forwards Meta"},
		{"Diagnose keys", "projmux setup reports swallowed shortcuts"},
		{"Terminal fallback", "projmux init applies supported terminal key mappings"},
		{"Dependencies", "projmux doctor checks tmux, git, stty, kubectl"},
		{"Rename key", "Ctrl-M sends 9011u, tmux maps User10 to rename"},
		{"Ghostty", "bind alt/ctrl keys to csi:9001u..9012u"},
		{"Windows Term.", "actions sendInput tmux/meta sequences; keybindings attach keys"},
		{"Docs", "docs/keybindings.md has copyable terminal examples"},
	}
	entries := make([]intpickercompat.Entry, 0, len(rows)+8)
	entries = append(entries, settingsBackEntry())
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Welcome", "revisit the shell quickstart guide"),
		Value: settingsWelcomeShow,
	})
	if statusErr != nil {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("Update", "status unavailable", statusErr.Error()),
			Value: settingsNoopValue,
		})
	} else {
		latest := status.LatestVersion
		if latest == "" {
			latest = "unknown"
		}
		entries = append(entries,
			intpickercompat.Entry{
				Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Update Now", "run installer-specific update command"),
				Value: settingsUpdateApply,
			},
			intpickercompat.Entry{
				Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Check Updates", "refresh cached GitHub release metadata"),
				Value: settingsUpdateCheck,
			},
			intpickercompat.Entry{
				Label: settingsLabelInfo("Latest", latest, status.CacheState),
				Value: settingsNoopValue,
			},
			intpickercompat.Entry{
				Label: settingsLabelInfo("Update state", status.UpdateState, ""),
				Value: settingsNoopValue,
			},
			intpickercompat.Entry{
				Label: settingsLabelInfo("Installer", status.Installer.Source, status.Installer.Note),
				Value: settingsNoopValue,
			},
		)
		if status.ReleaseURL != "" {
			entries = append(entries, intpickercompat.Entry{
				Label: settingsLabelInfo("Release notes", status.ReleaseURL, ""),
				Value: settingsNoopValue,
			})
		}
	}
	for _, r := range rows {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo(r.name, r.value, ""),
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
			welcome := newWelcomeCommand(c.update)
			welcome.lookupEnv = c.lookupEnv
			return welcome.Run(nil, stdout, stderr)
		default:
			return fmt.Errorf("unknown welcome settings action: %s", value)
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

func settingsBackEntry() intpickercompat.Entry {
	return intpickercompat.Entry{
		Label: settingsLabel(settingsGlyphBack, settingsColorBack, "Back", ""),
		Value: settingsBackValue,
	}
}

func settingsCloseBindings() []string {
	return pickerCloseBindings("esc", "ctrl-c", "alt-5", "ctrl-alt-s")
}

func printSettingsUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux settings")
}
