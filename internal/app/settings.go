package app

import (
	"errors"
	"fmt"
	"io"
	"strings"

	intfzf "github.com/es5h/projmux/internal/ui/fzf"
)

type settingsCommand struct {
	ai       *aiCommand
	switcher *switchCommand
	runner   intfzf.Runner
}

var errSettingsClosed = errors.New("settings closed")

const (
	settingsBackValue          = "__settings_back__"
	settingsSectionAI          = "section:ai"
	settingsSectionProject     = "section:project-picker"
	settingsActionPrefixAI     = "ai:"
	settingsActionPrefixSwitch = "switch:"
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
		entries, err := c.projectPickerEntries()
		if err != nil {
			return intfzf.Options{}, err
		}
		return intfzf.Options{
			UI:         "settings-project-picker",
			Entries:    entries,
			Prompt:     "Settings > Project Picker > ",
			Header:     "Manage project picker pins",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
		}, nil
	default:
		return intfzf.Options{}, fmt.Errorf("unknown settings section: %s", section)
	}
}

func (c *settingsCommand) projectPickerEntries() ([]intfzf.Entry, error) {
	entries := []intfzf.Entry{settingsBackEntry()}

	if c.switcher != nil {
		switchEntries, err := c.switcher.settingsEntries()
		if err != nil {
			return nil, err
		}
		for _, entry := range switchEntries {
			entries = append(entries, intfzf.Entry{
				Label: entry.Label,
				Value: settingsActionPrefixSwitch + entry.Value,
			})
		}
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
