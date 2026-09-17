package app

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// The Projects sidebar's closed-Project startup preference.
//
// The saved value lives in `sidebar-startup-picker` under the config
// directory, unchanged from the release that introduced it. When it is on, a
// closed Project offers Continue project and Recreate Project; when it is off,
// the sidebar goes straight to Continue project.

// settingsSidebarStartupPickerDetail opens the chooser. The two set actions
// are `sidebar-startup:on` and `sidebar-startup:off`.
const settingsSidebarStartupPickerDetail = settingsActionPrefixSidebarStartup + "view"

type sidebarStartupPickerEffective struct {
	Mode   config.SidebarStartupPicker
	Source string
}

func (c *settingsCommand) runSidebarStartupPickerDetail(stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-sidebar-startup-picker",
			Entries:    c.sidebarStartupPickerEntries(c.currentSidebarStartupPicker()),
			Title:      "Projects - Closed Project startup",
			Prompt:     "Settings > Projects > Project Sidebar > Closed Project startup > ",
			Footer:     projmuxFooter("Enter: apply  |  Back row: parent "),
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
		case strings.HasPrefix(action, settingsActionPrefixSidebarStartup):
			if err := c.executeWithFeedback(action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown closed Project startup action: %s", action)
		}
	}
}

// sidebarStartupChoiceLabel renders the saved on/off toggle as the product
// choice it actually is: whether a closed Project offers Recreate Project next
// to Continue project.
func sidebarStartupChoiceLabel(mode config.SidebarStartupPicker) string {
	if mode.Enabled() {
		return "Continue project / Recreate Project"
	}
	return "Continue project"
}

func sidebarStartupSourceLabel(toggle sidebarStartupPickerEffective) string {
	if toggle.Source == "saved" {
		return string(toggle.Mode) + " - saved"
	}
	return toggle.Source
}

func (c *settingsCommand) sidebarStartupPickerEntries(sidebarStartup sidebarStartupPickerEffective) []intpickercompat.Entry {
	locale := c.locale()
	choice := settingsCatalogTextLocale(locale, sidebarStartupChoiceLabel(sidebarStartup.Mode))
	source := settingsCatalogTextLocale(locale, sidebarStartupSourceLabel(sidebarStartup))
	entries := []intpickercompat.Entry{
		c.backEntry(),
		{
			Label: settingsNodeRowLabelInfoLocale(locale, settingsNavProjectsSidebar+".closed-startup", choice, source),
			Value: settingsNoopValue,
		},
	}
	for _, item := range []struct {
		mode config.SidebarStartupPicker
		desc string
	}{
		{config.SidebarStartupPickerOn, "show Continue project and Recreate Project for a closed Project"},
		{config.SidebarStartupPickerOff, projectTopologyStartupDescription},
	} {
		glyph := settingsGlyphInactive
		color := settingsColorDim
		if item.mode == sidebarStartup.Mode {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
		}
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelLocale(locale, glyph, color, sidebarStartupChoiceLabel(item.mode),
				settingsCatalogTextLocale(locale, item.desc)+" - "+source),
			Value:     settingsActionPrefixSidebarStartup + string(item.mode),
			SearchKey: "closed project startup continue open fresh sidebar startup picker on off",
		})
	}
	return entries
}

func (c *settingsCommand) executeSidebarStartupAction(action string) error {
	switch action {
	case string(config.SidebarStartupPickerOn), string(config.SidebarStartupPickerOff):
		return c.setSidebarStartupPicker(config.SidebarStartupPicker(action))
	default:
		return fmt.Errorf("unknown closed Project startup action: %s", action)
	}
}

// sidebarStartupPickerState is the read-only authority for the closed-Project
// startup preference. A missing file is effectively on, while an explicit
// saved on/off value retains its existing meaning. Resolution never persists
// the fallback: only the Settings mutation path writes the preference file.
func sidebarStartupPickerState(homeDir func() (string, error), lookupEnv func(string) string) sidebarStartupPickerEffective {
	fallback := sidebarStartupPickerEffective{Mode: config.SidebarStartupPickerOn, Source: "default"}
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return fallback
	}
	path := paths.SidebarStartupPickerFile()
	mode, err := config.LoadSidebarStartupPickerFile(path)
	if err != nil {
		return fallback
	}
	if _, err := os.Stat(path); err == nil {
		return sidebarStartupPickerEffective{Mode: mode, Source: "saved"}
	}
	return sidebarStartupPickerEffective{Mode: mode, Source: "default"}
}

func sidebarStartupPickerEnabled(homeDir func() (string, error), lookupEnv func(string) string) bool {
	return sidebarStartupPickerState(homeDir, lookupEnv).Mode.Enabled()
}

func (c *settingsCommand) currentSidebarStartupPicker() sidebarStartupPickerEffective {
	return sidebarStartupPickerState(c.homeDir, c.lookupEnv)
}

func (c *settingsCommand) setSidebarStartupPicker(value config.SidebarStartupPicker) error {
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return err
	}
	mode := config.NormalizeSidebarStartupPicker(string(value))
	if err := config.SaveSidebarStartupPickerFile(paths.SidebarStartupPickerFile(), mode); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		_ = c.runCommand("tmux", "display-message", "sidebar startup picker: "+string(mode))
	}
	return nil
}
