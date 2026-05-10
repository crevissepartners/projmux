package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
	intrender "github.com/crevissepartners/projmux/internal/ui/render"
	"github.com/crevissepartners/projmux/internal/version"
)

// osStat is a package-level indirection so tests can stub filesystem checks.
var osStat = os.Stat

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
	settingsBackValue:          {Name: "Back", Axis: settingsAxisGlobal},
	settingsNoopValue:          {Name: "Info", Axis: settingsAxisGlobal},
	settingsSectionProject:     {Name: "Project Picker", Axis: settingsAxisGlobal},
	settingsSectionAI:          {Name: "AI Settings", Axis: settingsAxisGlobal},
	settingsSectionStatusbar:   {Name: "Appearance", Axis: settingsAxisGlobal},
	settingsSectionKeybindings: {Name: "Keybindings", Axis: settingsAxisGlobal},
	settingsSectionLabs:        {Name: "Labs", Axis: settingsAxisGlobal},
	settingsSectionAbout:       {Name: "About", Axis: settingsAxisGlobal},
	settingsProjectAdd:         {Name: "Add Project", Axis: settingsAxisGlobal},
	settingsProjectPins:        {Name: "Pinned Projects", Axis: settingsAxisGlobal},
	settingsProjectRootManage:  {Name: "Project Root", Axis: settingsAxisGlobal},
	settingsProjdirClear:       {Name: "Clear Project Root", Axis: settingsAxisGlobal},
	settingsProjdirSetCurrent:  {Name: "Use Current Project as Root", Axis: settingsAxisGlobal},
	settingsProjdirSetTyped:    {Name: "Set Project Root", Axis: settingsAxisGlobal},
	settingsWorkdirAdd:         {Name: "Add Workdir", Axis: settingsAxisGlobal},
	settingsWorkdirList:        {Name: "Workdirs", Axis: settingsAxisGlobal},
	settingsWorkdirTyped:       {Name: "Type Workdir", Axis: settingsAxisGlobal},
	settingsLabKeybindings:     {Name: "Diagnose Keybindings", Axis: settingsAxisGlobal},
	settingsUpdateApply:        {Name: "Update Now", Axis: settingsAxisGlobal},
	settingsUpdateCheck:        {Name: "Check Updates", Axis: settingsAxisGlobal},
}

var settingsEntryPrefixCatalog = []struct {
	prefix string
	meta   settingsEntryMeta
}{
	{settingsActionPrefixAI, settingsEntryMeta{Name: "AI Settings", Axis: settingsAxisGlobal}},
	{settingsActionPrefixHooks, settingsEntryMeta{Name: "Project hook policy", Axis: settingsAxisGlobal}},
	{settingsActionPrefixKeymap, settingsEntryMeta{Name: "Keybindings", Axis: settingsAxisGlobal}},
	{settingsActionPrefixLabKeymap, settingsEntryMeta{Name: "Keybinding diagnostics", Axis: settingsAxisGlobal}},
	{settingsActionPrefixPicker, settingsEntryMeta{Name: "Picker backend", Axis: settingsAxisGlobal}},
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
	settingsBackValue             = "__settings_back__"
	settingsNoopValue             = "__settings_noop__"
	settingsSectionAI             = "section:ai"
	settingsSectionKeybindings    = "section:keybindings"
	settingsSectionProject        = "section:project-picker"
	settingsSectionStatusbar      = "section:statusbar"
	settingsSectionLabs           = "section:labs"
	settingsSectionAbout          = "section:about"
	settingsActionPrefixAI        = "ai:"
	settingsActionPrefixHooks     = "project-hooks:"
	settingsActionPrefixKeymap    = "keymap:"
	settingsActionPrefixLabKeymap = "lab-keymap:"
	settingsActionPrefixPicker    = "picker-backend:"
	settingsActionPrefixProjdir   = "projdir:"
	settingsActionPrefixStatusbar = "statusbar-decoration:"
	settingsActionPrefixSwitch    = "switch:"
	settingsActionPrefixUpdate    = "update:"
	settingsActionPrefixWorkdir   = "workdir:"
	settingsProjectAdd            = "project:add"
	settingsProjectPins           = "project:pins"
	settingsProjectRootManage     = "project-root:manage"
	settingsProjdirClear          = "projdir:clear"
	settingsProjdirSetCurrent     = "projdir:set-current"
	settingsProjdirSetTyped       = "projdir:set-typed"
	settingsUpdateApply           = "update:apply"
	settingsUpdateCheck           = "update:check"
	settingsWorkdirAdd            = "workdir:add"
	settingsWorkdirList           = "workdir:list"
	settingsWorkdirTyped          = "workdir:typed"
	settingsLabKeybindings        = "labs:keybindings"
	settingsKeymapFieldPlain      = "plain"
	settingsKeymapFieldPrefix     = "prefix"
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

	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings",
			Entries:    c.rootEntries(),
			Title:      "Settings",
			Prompt:     "Settings > ",
			Footer:     projmuxFooter("Enter: open  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
		})
		if err != nil {
			if errors.Is(err, errSettingsClosed) {
				return nil
			}
			return err
		}
		section := strings.TrimSpace(result.Value)
		if result.Key != "enter" || section == "" {
			return nil
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

func (c *settingsCommand) rootEntries() []intpickercompat.Entry {
	return []intpickercompat.Entry{
		{
			Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Project Picker", "project roots, workdirs, and pins"),
			Value: settingsSectionProject,
		},
		{
			Label: settingsLabel(settingsGlyphOpen, settingsColorType, "AI Settings", "default split mode"),
			Value: settingsSectionAI,
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
}

func (c *settingsCommand) sectionOptions(section string) (intpickercompat.Options, error) {
	switch section {
	case settingsSectionAI:
		return intpickercompat.Options{
			UI:         "settings-ai",
			Entries:    c.aiEntries(),
			Title:      "AI Settings - Default Ctrl+Shift+R/L split mode",
			Prompt:     "Settings > AI Settings > ",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
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
		entries, err := c.keybindingEntries()
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
			Title:      "Keybindings - Edit tmux plain and prefix chords",
			Prompt:     "Settings > Keybindings > ",
			Footer:     projmuxFooter("Enter: edit  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
		}, nil
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
			Title:      "Project Root - Manage the primary root",
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
			Title:      "Workdirs - Add or remove scan roots",
			Prompt:     "Settings > Project Picker > Workdirs > ",
			Footer:     projmuxFooter("Enter: add/remove  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
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
	entries := []intpickercompat.Entry{
		settingsBackEntry(),
		{
			Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Add Workdir...", "append a directory to the saved workdirs list"),
			Value: settingsWorkdirAdd,
		},
	}
	if c.switcher == nil {
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelDim("(no saved workdirs)", ""),
			Value: settingsNoopValue,
		}), nil
	}

	saved, err := c.switcher.loadSavedWorkdirs()
	if err != nil {
		return nil, err
	}

	if len(saved) == 0 {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelDim("(no saved workdirs)", ""),
			Value: settingsNoopValue,
		})
	} else {
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
	)

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

func (c *settingsCommand) runKeybindingsSection(stdout, stderr io.Writer) error {
	for {
		options, err := c.sectionOptions(settingsSectionKeybindings)
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
			if err := c.runLabKeybindingsSection(stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixHooks):
			if err := c.execute(action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown labs settings action: %s", action)
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
	return entries, "Keybinding Lab - " + action.Description, nil
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
	entries := make([]intpickercompat.Entry, 0, 6)
	entries = append(entries, settingsBackEntry())
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Diagnose keybindings", "probe delivery and apply terminal fallbacks"),
		Value: settingsLabKeybindings,
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
