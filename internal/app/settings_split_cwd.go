package app

import (
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// splitCWDFromRowDescription is the one sentence pair the chooser rows carry.
// The first half is the owner Project root rule; the second half is the
// CLI/UI boundary, which belongs on the row because the setting is silently
// half-true without it: a script that runs `projmux create pane` keeps starting
// in the Project root no matter what is chosen here.
const splitCWDFromRowDescription = "Pane directory is used only inside the Project root; the CLI does not follow this setting"

// splitCWDSourceLabelLocale renders one closed source name for a human. The
// stored value stays `project`/`pane`; only the row copy is localized.
func splitCWDSourceLabelLocale(locale i18n.Locale, source splitCWDSource) string {
	switch source {
	case splitCWDFromPane:
		return localizeText(locale, splitCWDFromPaneLabelKey, "Current Pane directory")
	default:
		return localizeText(locale, splitCWDFromProjectLabelKey, "Project root")
	}
}

const (
	splitCWDFromProjectLabelKey i18n.Key = "settings.text.split_cwd_from_project"
	splitCWDFromPaneLabelKey    i18n.Key = "settings.text.split_cwd_from_pane"
)

// currentSplitCWDFrom resolves the split start source for the Settings UI
// through the very resolver the UI split intents use, so the row can never
// show a value a keybinding split would not honor. The active Project context
// supplies the project tier, exactly as currentAIResumePickerLimit does.
func (c *settingsCommand) currentSplitCWDFrom() splitCWDResolution {
	root := ""
	if ctx := c.resolveSettingsProjectContext(); ctx.hasProject() {
		root = ctx.Path
	}
	return resolveUISplitCWDSource("", root, c.homeDir, c.lookupEnv)
}

// splitCWDFromSummary renders the "<value> - <source>" tail used on the AI root
// drill-in row, reusing the resolved source + tier.
func (c *settingsCommand) splitCWDFromSummary() string {
	locale := c.locale()
	current := c.currentSplitCWDFrom()
	return fmt.Sprintf("%s - %s", splitCWDSourceLabelLocale(locale, current.Source), current.Origin)
}

// aiSplitCWDFromEntries builds the chooser: an info header carrying the
// resolved value and the tier that decided it, then the two closed sources.
func (c *settingsCommand) aiSplitCWDFromEntries() []intpickercompat.Entry {
	locale := c.locale()
	current := c.currentSplitCWDFrom()
	entries := []intpickercompat.Entry{
		c.backEntry(),
		{
			Label: settingsNodeRowLabelInfoLocale(locale, settingsNavAISplitCWD,
				splitCWDSourceLabelLocale(locale, current.Source), string(current.Origin)),
			Value: settingsNoopValue,
		},
	}
	for _, source := range []splitCWDSource{splitCWDFromProject, splitCWDFromPane} {
		glyph := settingsGlyphInactive
		color := settingsColorDim
		if source == current.Source {
			glyph = settingsGlyphToggle
			color = settingsColorAdd
		}
		entries = append(entries, intpickercompat.Entry{
			Label: settingsResolvedLabelLocale(locale, glyph, color,
				splitCWDSourceLabelLocale(locale, source), splitCWDFromRowDescription),
			Value:     settingsActionPrefixAISplitCWD + string(source),
			SearchKey: "new splits start in split_cwd_from " + string(source),
		})
	}
	return entries
}

// runAISplitCWDFromSection drives Settings > AI > New splits start in. It is a
// compact chooser over the two closed sources; applying one writes the global
// config and nothing else.
func (c *settingsCommand) runAISplitCWDFromSection(stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-ai-split-cwd-from",
			Entries:    c.aiSplitCWDFromEntries(),
			Title:      "AI - New splits start in",
			Prompt:     "Settings > AI > New splits start in > ",
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
		case strings.HasPrefix(action, settingsActionPrefixAISplitCWD):
			source, ok := parseSplitCWDSource(strings.TrimPrefix(action, settingsActionPrefixAISplitCWD))
			if !ok {
				return fmt.Errorf("unknown split start source: %s", action)
			}
			if err := c.runSettingsMutation("Split start directory", stdout, stderr, func(out, _ io.Writer) error {
				return c.setSplitCWDFrom(source, out)
			}); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown split start directory action: %s", action)
		}
	}
}

// setSplitCWDFrom writes the chosen source to the global config [ai] section
// and touches no other key. The Project tier is not written from Settings: a
// Project-scoped value is a repository decision, and this row is global.
func (c *settingsCommand) setSplitCWDFrom(source splitCWDSource, stdout io.Writer) error {
	path, err := c.globalConfigPath()
	if err != nil {
		return err
	}
	if _, err := hooks.UpdateGlobalConfig(path, func(cfg *hooks.ProjectConfig) error {
		cfg.AI.SplitCWDFrom = string(source)
		return nil
	}); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "New splits start in: %s\n", source); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		_ = c.runCommand("tmux", "display-message", "New splits start in: "+string(source))
	}
	return nil
}
