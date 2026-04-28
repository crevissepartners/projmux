package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	intfzf "github.com/es5h/projmux/internal/ui/fzf"
	intrender "github.com/es5h/projmux/internal/ui/render"
	"github.com/es5h/projmux/internal/version"
)

// osStat is a package-level indirection so tests can stub filesystem checks.
var osStat = os.Stat

type settingsCommand struct {
	ai       *aiCommand
	switcher *switchCommand
	runner   intfzf.Runner
}

var errSettingsClosed = errors.New("settings closed")

const (
	settingsBackValue           = "__settings_back__"
	settingsNoopValue           = "__settings_noop__"
	settingsSectionAI           = "section:ai"
	settingsSectionProject      = "section:project-picker"
	settingsSectionAbout        = "section:about"
	settingsActionPrefixAI      = "ai:"
	settingsActionPrefixSwitch  = "switch:"
	settingsActionPrefixWorkdir = "workdir:"
	settingsProjectAdd          = "project:add"
	settingsProjectPins         = "project:pins"
	settingsWorkdirAdd          = "workdir:add"
	settingsWorkdirList         = "workdir:list"
	settingsWorkdirTyped        = "workdir:typed"
)

func newSettingsCommand(ai *aiCommand, switcher *switchCommand) *settingsCommand {
	return &settingsCommand{
		ai:       ai,
		switcher: switcher,
		runner:   intfzf.NewRunner(),
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
			Prompt:     "Settings > ",
			Header:     "Choose settings area",
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
	result, err := c.runner.Run(options)
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
			Label: "\x1b[35mAI Settings\x1b[0m      \x1b[90mdefault split mode\x1b[0m",
			Value: settingsSectionAI,
		},
		{
			Label: "\x1b[36mProject Picker\x1b[0m   \x1b[90mpinned projects and sidebar entries\x1b[0m",
			Value: settingsSectionProject,
		},
		{
			Label: "\x1b[34mAbout\x1b[0m            \x1b[90mversion, source, common keys\x1b[0m",
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
			Prompt:     "Settings > AI Settings > ",
			Header:     "Set Ctrl+Shift+R/L default mode",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
		}, nil
	case settingsSectionProject:
		return intfzf.Options{
			UI:         "settings-project-picker",
			Entries:    c.projectPickerEntries(),
			Prompt:     "Settings > Project Picker > ",
			Header:     "Add projects to the picker and manage pinned projects",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
		}, nil
	case settingsSectionAbout:
		return intfzf.Options{
			UI:         "settings-about",
			Entries:    settingsAboutEntries(),
			Prompt:     "Settings > About > ",
			Header:     "projmux app information",
			Footer:     projmuxFooter("Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
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
		Prompt:     "Settings > Project Picker > Add Project > ",
		Header:     "Choose a filesystem directory to add to the project picker",
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
		Prompt:     "Settings > Project Picker > Add Workdir > ",
		Header:     "Add a workdir to scan",
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
		Label: "\x1b[32m✏  Type path manually...\x1b[0m   \x1b[90m(skip filesystem scan)\x1b[0m",
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
	typed = strings.TrimSpace(typed)
	if typed == "" {
		return "", errors.New("workdir path is empty")
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
			Label: "\x1b[90m(no saved workdirs)\x1b[0m",
			Value: settingsNoopValue,
		}), nil
	}

	saved, err := c.switcher.loadSavedWorkdirs()
	if err != nil {
		return nil, err
	}

	if len(saved) == 0 {
		entries = append(entries, intfzf.Entry{
			Label: "\x1b[90m(no saved workdirs)\x1b[0m",
			Value: settingsNoopValue,
		})
	} else {
		for _, dir := range saved {
			entries = append(entries, intfzf.Entry{
				Label: "x Remove  " + dir + "  \x1b[90m(saved)\x1b[0m",
				Value: settingsActionPrefixWorkdir + "remove:" + dir,
			})
		}
	}

	for _, src := range c.switcher.envWorkdirSources() {
		if strings.TrimSpace(src.Value) == "" {
			continue
		}
		entries = append(entries, intfzf.Entry{
			Label: "\x1b[90m" + src.Name + " = " + src.Value + "  (env, read-only)\x1b[0m",
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
		Label: "\x1b[32m+ Add Project...\x1b[0m     \x1b[90mscan filesystem roots\x1b[0m",
		Value: settingsProjectAdd,
	})
	entries = append(entries, c.addCurrentProjectEntry())
	entries = append(entries, intfzf.Entry{
		Label: "\x1b[36mPinned Projects\x1b[0m     \x1b[90mremove or clear pins\x1b[0m",
		Value: settingsProjectPins,
	})
	entries = append(entries, intfzf.Entry{
		Label: "\x1b[32m+ Add Workdir...\x1b[0m     \x1b[90mappend a directory to the saved workdirs list\x1b[0m",
		Value: settingsWorkdirAdd,
	})
	entries = append(entries, intfzf.Entry{
		Label: "\x1b[36mWorkdirs\x1b[0m            \x1b[90mremove saved workdirs (env list takes priority)\x1b[0m",
		Value: settingsWorkdirList,
	})
	return entries
}

// projectRootEntry renders the resolved PROJDIR path with its source label.
// The row is read-only (settingsNoopValue) and never triggers memoization.
func (c *settingsCommand) projectRootEntry() intfzf.Entry {
	if c.switcher == nil {
		return intfzf.Entry{
			Label: "\x1b[90mProject Root       unavailable\x1b[0m",
			Value: settingsNoopValue,
		}
	}
	value, source, err := c.switcher.currentProjdirInfo()
	if err != nil || value == "" {
		return intfzf.Entry{
			Label: "\x1b[90mProject Root       unavailable\x1b[0m",
			Value: settingsNoopValue,
		}
	}
	return intfzf.Entry{
		Label: "\x1b[36mProject Root\x1b[0m       " + value + "  \x1b[90m(" + source + ")\x1b[0m",
		Value: settingsNoopValue,
	}
}

func (c *settingsCommand) projectRootHintEntry() intfzf.Entry {
	return intfzf.Entry{
		Label: "\x1b[90mOverride via PROJDIR env, set -g @projmux_projdir, or ~/.config/projmux/projdir\x1b[0m",
		Value: settingsNoopValue,
	}
}

func (c *settingsCommand) addCurrentProjectEntry() intfzf.Entry {
	if c.switcher == nil {
		return intfzf.Entry{
			Label: "\x1b[90m+ Add Current Project  unavailable\x1b[0m",
			Value: settingsNoopValue,
		}
	}

	pins, err := c.switcher.loadPins()
	if err != nil {
		return intfzf.Entry{
			Label: "\x1b[90m+ Add Current Project  pins unavailable\x1b[0m",
			Value: settingsNoopValue,
		}
	}
	homeDir, err := c.switcher.resolveHomeDir()
	if err != nil {
		return intfzf.Entry{
			Label: "\x1b[90m+ Add Current Project  home unavailable\x1b[0m",
			Value: settingsNoopValue,
		}
	}
	repoRoot := c.switcher.switchRepoRoot(homeDir)
	currentTarget, err := c.switcher.resolveSwitchTarget(nil, "settings project picker")
	if err != nil || currentTarget == "" || currentTarget == switchSettingsSentinel {
		return intfzf.Entry{
			Label: "\x1b[90m+ Add Current Project  no project context\x1b[0m",
			Value: settingsNoopValue,
		}
	}
	if containsString(pins, currentTarget) {
		return intfzf.Entry{
			Label: "\x1b[90m+ Add Current Project  already pinned  " + intrender.PrettyPath(currentTarget, homeDir, repoRoot) + "\x1b[0m",
			Value: settingsNoopValue,
		}
	}
	return intfzf.Entry{
		Label: "+ Add Current Project  " + intrender.PrettyPath(currentTarget, homeDir, repoRoot),
		Value: settingsActionPrefixSwitch + "add:" + currentTarget,
	}
}

func (c *settingsCommand) pinnedProjectEntries() ([]intfzf.Entry, error) {
	entries := []intfzf.Entry{settingsBackEntry()}
	if c.switcher == nil {
		return append(entries, intfzf.Entry{
			Label: "\x1b[90m(no pinned projects)\x1b[0m",
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
			Label: "\x1b[90m(no pinned projects)\x1b[0m",
			Value: settingsNoopValue,
		}), nil
	}

	entries = append(entries, intfzf.Entry{
		Label: "x Clear all pins",
		Value: settingsActionPrefixSwitch + "clear",
	})
	for _, pin := range pins {
		entries = append(entries, intfzf.Entry{
			Label: "x Remove  " + intrender.PrettyPath(pin, homeDir, repoRoot),
			Value: settingsActionPrefixSwitch + "pin:" + pin,
		})
	}
	return entries, nil
}

func (c *settingsCommand) aiEntries() []intfzf.Entry {
	if c.ai == nil {
		return nil
	}

	rows := c.ai.settingsRows()
	entries := make([]intfzf.Entry, 0, len(rows)+1)
	entries = append(entries, settingsBackEntry())
	for _, row := range rows {
		entries = append(entries, intfzf.Entry{
			Label: row.Label,
			Value: settingsActionPrefixAI + row.Value,
		})
	}
	return entries
}

func settingsAboutEntries() []intfzf.Entry {
	return []intfzf.Entry{
		settingsBackEntry(),
		{
			Label: "Version        projmux " + version.String(),
			Value: settingsNoopValue,
		},
		{
			Label: "Source         https://github.com/es5h/projmux",
			Value: settingsNoopValue,
		},
		{
			Label: "Update         go install github.com/es5h/projmux/cmd/projmux@latest",
			Value: settingsNoopValue,
		},
		{
			Label: "App            sidebar, sessions, projects, AI picker, settings",
			Value: settingsNoopValue,
		},
		{
			Label: "Tmux actions   new window, rename window/pane, previous/next window",
			Value: settingsNoopValue,
		},
		{
			Label: "Key model      terminal sends CSI-u keys; tmux runs projmux actions",
			Value: settingsNoopValue,
		},
		{
			Label: "Rename key     Ctrl-M sends 9011u, tmux maps User10 to rename",
			Value: settingsNoopValue,
		},
		{
			Label: "Ghostty        bind alt/ctrl keys to csi:9001u..9012u",
			Value: settingsNoopValue,
		},
		{
			Label: "Windows Term.  actions sendInput tmux/meta sequences; keybindings attach keys",
			Value: settingsNoopValue,
		},
		{
			Label: "Docs           docs/keybindings.md has copyable terminal examples",
			Value: settingsNoopValue,
		},
	}
}

func (c *settingsCommand) execute(value string, stdout, stderr io.Writer) error {
	switch {
	case strings.HasPrefix(value, settingsActionPrefixAI):
		mode := strings.TrimPrefix(value, settingsActionPrefixAI)
		if c.ai == nil {
			return errors.New("ai settings are not configured")
		}
		return c.ai.setMode(mode)
	case strings.HasPrefix(value, settingsActionPrefixSwitch):
		action := strings.TrimPrefix(value, settingsActionPrefixSwitch)
		if c.switcher == nil {
			return errors.New("project picker settings are not configured")
		}
		return c.switcher.executeSettingsAction(action, stdout, stderr)
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
		Label: "\x1b[90m< Back\x1b[0m",
		Value: settingsBackValue,
	}
}

func settingsCloseBindings() []string {
	return []string{
		"esc:abort",
		"ctrl-c:abort",
		"alt-5:abort",
		"ctrl-alt-s:abort",
	}
}

func printSettingsUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux settings")
}
