package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	intfzf "github.com/crevissepartners/projmux/internal/ui/fzf"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intrender "github.com/crevissepartners/projmux/internal/ui/render"
	"github.com/crevissepartners/projmux/internal/version"
)

// osStat is a package-level indirection so tests can stub filesystem checks.
var osStat = os.Stat

type settingsCommand struct {
	ai           *aiCommand
	switcher     *switchCommand
	update       *updateCommand
	runner       intfzf.Runner
	nativePicker intpicker.Runner
	homeDir      func() (string, error)
	lookupEnv    func(string) string
	runCommand   func(name string, args ...string) error
}

var errSettingsClosed = errors.New("settings closed")

const (
	settingsBackValue             = "__settings_back__"
	settingsNoopValue             = "__settings_noop__"
	settingsSectionAI             = "section:ai"
	settingsSectionProject        = "section:project-picker"
	settingsSectionStatusbar      = "section:statusbar"
	settingsSectionLabs           = "section:labs"
	settingsSectionAbout          = "section:about"
	settingsActionPrefixAI        = "ai:"
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
)

func newSettingsCommand(ai *aiCommand, switcher *switchCommand, update *updateCommand) *settingsCommand {
	return &settingsCommand{
		ai:           ai,
		switcher:     switcher,
		update:       update,
		runner:       intfzf.NewRunner(),
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
	if c.runner == nil {
		return errors.New("settings runner is not configured")
	}

	for {
		result, err := c.runPicker(intfzf.Options{
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

func (c *settingsCommand) runPicker(options intfzf.Options) (intfzf.Result, error) {
	result, err := runPickerOptionBackend(c.lookupEnv, c.nativePicker, c.runner, options)
	if err != nil {
		if isNoSelectionExit(err) {
			return intfzf.Result{}, errSettingsClosed
		}
		return intfzf.Result{}, fmt.Errorf("run settings picker: %w", err)
	}
	return result, nil
}

func (c *settingsCommand) rootEntries() []intfzf.Entry {
	return []intfzf.Entry{
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
			Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Labs", "experimental picker engine"),
			Value: settingsSectionLabs,
		},
		{
			Label: settingsLabel(settingsGlyphOpen, settingsColorType, "About", "version, updates, key setup"),
			Value: settingsSectionAbout,
		},
	}
}

func (c *settingsCommand) sectionOptions(section string) (intfzf.Options, error) {
	switch section {
	case settingsSectionAI:
		return intfzf.Options{
			UI:         "settings-ai",
			Entries:    c.aiEntries(),
			Title:      "AI Settings",
			Prompt:     "Settings > AI Settings > ",
			Header:     "Default Ctrl+Shift+R/L split mode",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
		}, nil
	case settingsSectionProject:
		return intfzf.Options{
			UI:         "settings-project-picker",
			Entries:    c.projectPickerEntries(),
			Title:      "Project Picker",
			Prompt:     "Settings > Project Picker > ",
			Header:     "Project roots, workdirs, and pinned projects",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
		}, nil
	case settingsSectionStatusbar:
		return intfzf.Options{
			UI:         "settings-statusbar",
			Entries:    c.statusbarEntries(),
			Title:      "Appearance",
			Prompt:     "Settings > Appearance > ",
			Header:     "Status and popup decoration mode",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
		}, nil
	case settingsSectionLabs:
		return intfzf.Options{
			UI:         "settings-labs",
			Entries:    c.labsEntries(),
			Title:      "Labs",
			Prompt:     "Settings > Labs > ",
			Header:     "Experimental features",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
		}, nil
	case settingsSectionAbout:
		return intfzf.Options{
			UI:         "settings-about",
			Entries:    c.aboutEntries(),
			Title:      "About",
			Prompt:     "Settings > About > ",
			Header:     "Version, updates, key setup",
			Footer:     projmuxFooter("Enter: action  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
		}, nil
	default:
		return intfzf.Options{}, fmt.Errorf("unknown settings section: %s", section)
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
	entries = append([]intfzf.Entry{settingsBackEntry()}, entries...)

	result, err := c.runPicker(intfzf.Options{
		UI:         "settings-project-add",
		Entries:    entries,
		Title:      "Add Project",
		Prompt:     "Settings > Project Picker > Add Project > ",
		Header:     "Choose a filesystem directory",
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

		result, err := c.runPicker(intfzf.Options{
			UI:         "settings-project-root",
			Entries:    entries,
			Title:      "Project Root",
			Prompt:     "Settings > Project Picker > Project Root > ",
			Header:     "Manage the primary Project Root; Workdirs are separate search roots",
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
	result, err := c.runPicker(intfzf.Options{
		UI:           "settings-project-root-typed",
		Entries:      nil,
		AcceptQuery:  true,
		InitialQuery: initialQuery,
		Title:        "Set Project Root",
		Prompt:       "Type project root path > ",
		Header:       "Type one absolute primary root path. If unconfigured, the prompt starts at $HOME; Workdirs are separate search roots.",
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
	entries = append([]intfzf.Entry{
		settingsBackEntry(),
		settingsWorkdirTypedEntry(),
	}, entries...)

	result, err := c.runPicker(intfzf.Options{
		UI:         "settings-workdir-add",
		Entries:    entries,
		Title:      "Add Workdir",
		Prompt:     "Settings > Project Picker > Add Workdir > ",
		Header:     "Choose or type a directory to scan",
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
func settingsWorkdirTypedEntry() intfzf.Entry {
	return intfzf.Entry{
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

	result, err := c.runPicker(intfzf.Options{
		UI:          "settings-workdir-typed",
		Entries:     nil,
		AcceptQuery: true,
		Title:       "Type Workdir",
		Prompt:      "Type workdir path > ",
		Header:      "Type an absolute path. WSL example: /mnt/c/Users/me/code",
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

		result, err := c.runPicker(intfzf.Options{
			UI:         "settings-workdirs",
			Entries:    entries,
			Title:      "Workdirs",
			Prompt:     "Settings > Project Picker > Workdirs > ",
			Header:     "Remove saved workdirs (env list takes priority when set)",
			Footer:     projmuxFooter("Enter: remove  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
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
		if err := c.execute(action, stdout, stderr); err != nil {
			return err
		}
	}
}

func (c *settingsCommand) workdirListEntries() ([]intfzf.Entry, error) {
	entries := []intfzf.Entry{settingsBackEntry()}
	if c.switcher == nil {
		return append(entries, intfzf.Entry{
			Label: settingsLabelDim("(no saved workdirs)", ""),
			Value: settingsNoopValue,
		}), nil
	}

	saved, err := c.switcher.loadSavedWorkdirs()
	if err != nil {
		return nil, err
	}

	if len(saved) == 0 {
		entries = append(entries, intfzf.Entry{
			Label: settingsLabelDim("(no saved workdirs)", ""),
			Value: settingsNoopValue,
		})
	} else {
		for _, dir := range saved {
			entries = append(entries, intfzf.Entry{
				Label: settingsLabel(settingsGlyphRemove, settingsColorRemove, "Remove", dir+"  (saved)"),
				Value: settingsActionPrefixWorkdir + "remove:" + dir,
			})
		}
	}

	for _, src := range c.switcher.envWorkdirSources() {
		if strings.TrimSpace(src.Value) == "" {
			continue
		}
		entries = append(entries, intfzf.Entry{
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

		result, err := c.runPicker(intfzf.Options{
			UI:         "settings-project-pins",
			Entries:    entries,
			Title:      "Pinned Projects",
			Prompt:     "Settings > Project Picker > Pinned Projects > ",
			Header:     "Remove pinned projects or clear all pins",
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
		if err := c.execute(action, stdout, stderr); err != nil {
			return err
		}
	}
}

func (c *settingsCommand) projectPickerEntries() []intfzf.Entry {
	entries := []intfzf.Entry{
		settingsBackEntry(),
	}

	entries = append(entries, c.projectRootEntry())
	entries = append(entries, c.projectRootHintEntry())
	entries = append(entries, intfzf.Entry{
		Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Add Workdir...", "append a directory to the saved workdirs list"),
		Value: settingsWorkdirAdd,
	})
	entries = append(entries, intfzf.Entry{
		Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Workdirs", "remove saved workdirs (env list takes priority)"),
		Value: settingsWorkdirList,
	})
	entries = append(entries, intfzf.Entry{
		Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Pinned Projects", "remove or clear pins"),
		Value: settingsProjectPins,
	})
	entries = append(entries, intfzf.Entry{
		Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Add Project...", "scan filesystem roots"),
		Value: settingsProjectAdd,
	})
	entries = append(entries, c.addCurrentProjectEntry())
	return entries
}

// projectRootEntry renders the resolved primary root with its source label.
// Opening it manages the saved project root; rendering never memoizes env state.
func (c *settingsCommand) projectRootEntry() intfzf.Entry {
	if c.switcher == nil {
		return intfzf.Entry{
			Label: settingsLabelDim("Project Root", "unavailable"),
			Value: settingsNoopValue,
		}
	}
	value, source, err := c.switcher.currentProjdirInfo()
	if err != nil || value == "" {
		return intfzf.Entry{
			Label: settingsLabelDim("Project Root", "not configured"),
			Value: settingsProjectRootManage,
		}
	}
	return intfzf.Entry{
		Label: settingsLabelInfo("Project Root", value, source),
		Value: settingsProjectRootManage,
	}
}

func (c *settingsCommand) projectRootHintEntry() intfzf.Entry {
	// Keep the entire hint in one dim run so search substrings such as
	// "Set PROJMUX_PROJDIR" stay contiguous in the rendered label.
	return intfzf.Entry{
		Label: "  " + settingsColorDim + "Project Root is the primary root. Workdirs are extra search roots. Set PROJMUX_PROJDIR, @projmux_projdir, or the saved ~/.config/projmux/projdir value." + settingsColorReset,
		Value: settingsNoopValue,
	}
}

func (c *settingsCommand) projectRootEntries() ([]intfzf.Entry, error) {
	entries := []intfzf.Entry{settingsBackEntry()}
	if c.switcher == nil {
		return append(entries, intfzf.Entry{
			Label: settingsLabelDim("Project Root", "unavailable"),
			Value: settingsNoopValue,
		}), nil
	}

	info, err := c.switcher.projdirSettingsInfo()
	if err != nil {
		return nil, err
	}
	if info.EffectiveValue == "" {
		entries = append(entries, intfzf.Entry{
			Label: settingsLabelInfo("Effective Project Root", "not configured", "no env, tmux option, or saved value"),
			Value: settingsNoopValue,
		})
	} else {
		entries = append(entries, intfzf.Entry{
			Label: settingsLabelInfo("Effective Project Root", info.EffectiveValue, info.EffectiveSource),
			Value: settingsNoopValue,
		})
	}

	switch {
	case info.SavedValue == "":
		entries = append(entries, intfzf.Entry{
			Label: settingsLabelInfo("Saved Project Root", "not set", "~/.config/projmux/projdir"),
			Value: settingsNoopValue,
		})
	case info.EffectiveSource == projdirSourceSaved:
		entries = append(entries, intfzf.Entry{
			Label: settingsLabelInfo("Saved Project Root", info.SavedValue, "active"),
			Value: settingsNoopValue,
		})
	case info.EffectiveSource == projdirSourceUnresolved:
		entries = append(entries, intfzf.Entry{
			Label: settingsLabelInfo("Saved Project Root", info.SavedValue, "saved"),
			Value: settingsNoopValue,
		})
	default:
		entries = append(entries, intfzf.Entry{
			Label: settingsLabelInfo("Saved Project Root", info.SavedValue, "shadowed by "+info.EffectiveSource),
			Value: settingsNoopValue,
		})
	}

	entries = append(entries,
		intfzf.Entry{
			Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Set Project Root...", "save one primary root path directly"),
			Value: settingsProjdirSetTyped,
		},
		c.setCurrentProjectRootEntry(),
		intfzf.Entry{
			Label: settingsLabel(settingsGlyphRemove, settingsColorRemove, "Clear Saved Project Root", "remove ~/.config/projmux/projdir"),
			Value: settingsProjdirClear,
		},
		intfzf.Entry{
			Label: "  " + settingsColorDim + "Env PROJMUX_PROJDIR and tmux @projmux_projdir override the saved value until unset." + settingsColorReset,
			Value: settingsNoopValue,
		},
	)
	return entries, nil
}

func (c *settingsCommand) setCurrentProjectRootEntry() intfzf.Entry {
	if c.switcher == nil {
		return intfzf.Entry{
			Label: settingsLabelDim("Use Current Project as Root", "unavailable"),
			Value: settingsNoopValue,
		}
	}

	homeDir, err := c.switcher.resolveHomeDir()
	if err != nil {
		return intfzf.Entry{
			Label: settingsLabelDim("Use Current Project as Root", "home unavailable"),
			Value: settingsNoopValue,
		}
	}
	repoRoot, _, _ := c.switcher.currentProjdirInfo()
	currentTarget, err := c.switcher.resolveSwitchTargetNoMemoize(nil, "settings project root")
	if err != nil || currentTarget == "" || currentTarget == switchSettingsSentinel {
		return intfzf.Entry{
			Label: settingsLabelDim("Use Current Project as Root", "no project context"),
			Value: settingsNoopValue,
		}
	}
	return intfzf.Entry{
		Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Use Current Project as Root", intrender.PrettyPath(currentTarget, homeDir, repoRoot)),
		Value: settingsProjdirSetCurrent,
	}
}

func (c *settingsCommand) addCurrentProjectEntry() intfzf.Entry {
	if c.switcher == nil {
		return intfzf.Entry{
			Label: settingsLabelDim("Add Current Project", "unavailable"),
			Value: settingsNoopValue,
		}
	}

	pins, err := c.switcher.loadPins()
	if err != nil {
		return intfzf.Entry{
			Label: settingsLabelDim("Add Current Project", "pins unavailable"),
			Value: settingsNoopValue,
		}
	}
	homeDir, err := c.switcher.resolveHomeDir()
	if err != nil {
		return intfzf.Entry{
			Label: settingsLabelDim("Add Current Project", "home unavailable"),
			Value: settingsNoopValue,
		}
	}
	repoRoot := c.switcher.switchRepoRoot(homeDir)
	currentTarget, err := c.switcher.resolveSwitchTarget(nil, "settings project picker")
	if err != nil || currentTarget == "" || currentTarget == switchSettingsSentinel {
		return intfzf.Entry{
			Label: settingsLabelDim("Add Current Project", "no project context"),
			Value: settingsNoopValue,
		}
	}
	if containsString(pins, currentTarget) {
		return intfzf.Entry{
			Label: settingsLabelDim("Add Current Project", "already pinned  "+intrender.PrettyPath(currentTarget, homeDir, repoRoot)),
			Value: settingsNoopValue,
		}
	}
	return intfzf.Entry{
		Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Add Current Project", intrender.PrettyPath(currentTarget, homeDir, repoRoot)),
		Value: settingsActionPrefixSwitch + "add:" + currentTarget,
	}
}

func (c *settingsCommand) pinnedProjectEntries() ([]intfzf.Entry, error) {
	entries := []intfzf.Entry{settingsBackEntry()}
	if c.switcher == nil {
		return append(entries, intfzf.Entry{
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
		return append(entries, intfzf.Entry{
			Label: settingsLabelDim("(no pinned projects)", ""),
			Value: settingsNoopValue,
		}), nil
	}

	entries = append(entries, intfzf.Entry{
		Label: settingsLabel(settingsGlyphRemove, settingsColorRemove, "Clear all pins", ""),
		Value: settingsActionPrefixSwitch + "clear",
	})
	for _, pin := range pins {
		entries = append(entries, intfzf.Entry{
			Label: settingsLabel(settingsGlyphRemove, settingsColorRemove, "Remove", intrender.PrettyPath(pin, homeDir, repoRoot)),
			Value: settingsActionPrefixSwitch + "pin:" + pin,
		})
	}
	return entries, nil
}

func (c *settingsCommand) aiEntries() []intfzf.Entry {
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

	entries := make([]intfzf.Entry, 0, len(modes)+1)
	entries = append(entries, settingsBackEntry())
	for _, item := range modes {
		glyph := settingsGlyphInactive
		color := settingsColorDim
		if item.mode == current {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
		}
		entries = append(entries, intfzf.Entry{
			Label: settingsLabel(glyph, color, item.mode, item.desc),
			Value: settingsActionPrefixAI + item.mode,
		})
	}
	return entries
}

func (c *settingsCommand) statusbarEntries() []intfzf.Entry {
	current := c.currentStatusbarDecoration()
	modes := []struct {
		mode config.StatusbarDecoration
		desc string
	}{
		{config.StatusbarDecorationOff, "no status or popup icon prefix; safest for all fonts"},
		{config.StatusbarDecorationSymbol, "Nerd Font-style status and notification icons"},
		{config.StatusbarDecorationEmoji, "emoji status and notification icons"},
	}

	entries := make([]intfzf.Entry, 0, len(modes)+1)
	entries = append(entries, settingsBackEntry())
	for _, item := range modes {
		glyph := settingsGlyphInactive
		color := settingsColorDim
		if item.mode == current {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
		}
		entries = append(entries, intfzf.Entry{
			Label: settingsLabel(glyph, color, string(item.mode), item.desc),
			Value: settingsActionPrefixStatusbar + string(item.mode),
		})
	}
	return entries
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

func (c *settingsCommand) labsEntries() []intfzf.Entry {
	current, source := c.currentPickerBackend()
	modes := []struct {
		mode config.PickerBackend
		name string
		desc string
	}{
		{config.PickerBackendNative, "native", "default projmux picker engine; no fzf required"},
		{config.PickerBackendFZF, "fzf", "external fzf fallback backend"},
	}

	entries := make([]intfzf.Entry, 0, len(modes)+2)
	entries = append(entries, settingsBackEntry())
	if source != "" {
		entries = append(entries, intfzf.Entry{
			Label: settingsLabelInfo("Picker source", string(current), source),
			Value: settingsNoopValue,
		})
	}
	for _, item := range modes {
		glyph := settingsGlyphInactive
		color := settingsColorDim
		if item.mode == current {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
		}
		entries = append(entries, intfzf.Entry{
			Label: settingsLabel(glyph, color, item.name, item.desc),
			Value: settingsActionPrefixPicker + string(item.mode),
		})
	}
	return entries
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

func (c *settingsCommand) aboutEntries() []intfzf.Entry {
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
		{"Dependencies", "projmux doctor checks tmux, fzf, git, stty, kubectl"},
		{"Rename key", "Ctrl-M sends 9011u, tmux maps User10 to rename"},
		{"Ghostty", "bind alt/ctrl keys to csi:9001u..9012u"},
		{"Windows Term.", "actions sendInput tmux/meta sequences; keybindings attach keys"},
		{"Docs", "docs/keybindings.md has copyable terminal examples"},
	}
	entries := make([]intfzf.Entry, 0, len(rows)+8)
	entries = append(entries, settingsBackEntry())
	if statusErr != nil {
		entries = append(entries, intfzf.Entry{
			Label: settingsLabelInfo("Update", "status unavailable", statusErr.Error()),
			Value: settingsNoopValue,
		})
	} else {
		latest := status.LatestVersion
		if latest == "" {
			latest = "unknown"
		}
		entries = append(entries,
			intfzf.Entry{
				Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Update Now", "run installer-specific update command"),
				Value: settingsUpdateApply,
			},
			intfzf.Entry{
				Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Check Updates", "refresh cached GitHub release metadata"),
				Value: settingsUpdateCheck,
			},
			intfzf.Entry{
				Label: settingsLabelInfo("Latest", latest, status.CacheState),
				Value: settingsNoopValue,
			},
			intfzf.Entry{
				Label: settingsLabelInfo("Update state", status.UpdateState, ""),
				Value: settingsNoopValue,
			},
			intfzf.Entry{
				Label: settingsLabelInfo("Installer", status.Installer.Source, status.Installer.Note),
				Value: settingsNoopValue,
			},
		)
		if status.ReleaseURL != "" {
			entries = append(entries, intfzf.Entry{
				Label: settingsLabelInfo("Release notes", status.ReleaseURL, ""),
				Value: settingsNoopValue,
			})
		}
	}
	for _, r := range rows {
		entries = append(entries, intfzf.Entry{
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

func settingsBackEntry() intfzf.Entry {
	return intfzf.Entry{
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
