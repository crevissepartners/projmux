package app

import (
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/systemstatus"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

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
		case strings.HasPrefix(action, settingsActionPrefixLiveResources):
			if err := c.execute(action, stdout, stderr); err != nil {
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

func (c *settingsCommand) labsEntries() []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	current, source := c.currentPickerBackend()
	hookMode, hookSource := c.currentProjectHooksMode()
	liveMode, _, liveSupported := c.currentLiveResourcesMode()
	entries := make([]intpickercompat.Entry, 0, 5)
	entries = append(entries, settingsBackEntryLocale(locale))
	liveGlyph := settingsGlyphInactive
	liveColor := settingsColorDim
	liveValue := settingsNoopValue
	liveDescription := "unavailable on this platform"
	if liveSupported {
		liveDescription = "off - hidden; current system view"
		liveValue = settingsActionPrefixLiveResources + string(config.LiveResourcesOn)
		if liveMode == config.LiveResourcesOn {
			liveDescription = "on - live CPU and memory; current system view"
			liveGlyph = settingsGlyphToggle
			liveColor = settingsColorAdd
			liveValue = settingsActionPrefixLiveResources + string(config.LiveResourcesOff)
		}
	}
	entries = append(entries, intpickercompat.Entry{
		Label:     settingsLabelLocale(locale, liveGlyph, liveColor, "Live system resources", liveDescription),
		Value:     liveValue,
		SearchKey: "Live system resources CPU memory statusbar macOS Linux WSL on off",
	})
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

func (c *settingsCommand) currentLiveResourcesMode() (config.LiveResourcesMode, string, bool) {
	if !systemstatus.Supported() {
		return config.LiveResourcesOff, "unsupported platform", false
	}
	paths, err := pickerBackendConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return config.LiveResourcesOff, "default", true
	}
	mode, err := config.LoadLiveResourcesFile(paths.LiveResourcesFile())
	if err != nil {
		return config.LiveResourcesOff, "default", true
	}
	if _, err := c.statFile(paths.LiveResourcesFile()); err == nil {
		return mode, "saved", true
	}
	return mode, "default", true
}

func (c *settingsCommand) setLiveResourcesMode(value string) error {
	if !systemstatus.Supported() {
		return fmt.Errorf("live system resources are unavailable on this platform")
	}
	mode := config.NormalizeLiveResourcesMode(value)
	paths, err := pickerBackendConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return err
	}
	if err := config.SaveLiveResourcesFile(paths.LiveResourcesFile(), mode); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		if err := c.runCommand("tmux", "set-option", "-g", liveResourcesTmuxOption, string(mode)); err != nil {
			return fmt.Errorf("set live tmux resource status: %w", err)
		}
	}
	return nil
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
	if _, err := c.statFile(paths.ProjectHooksFile()); err == nil {
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
	if _, err := c.statFile(paths.PickerBackendFile()); err == nil {
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
