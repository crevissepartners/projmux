package app

import (
	"errors"
	"fmt"
	"io"
	"strings"

	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
	"github.com/crevissepartners/projmux/internal/version"
)

// aboutEntries renders the About container: build identity as state, the
// update collection as its own View, Welcome, and the confirmed quit. Quit is
// worded as `Quit Projmux` so the target of the action — the app-owned runtime
// and its socket — is explicit rather than implied.
func (c *settingsCommand) aboutEntries() []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	entries := make([]intpickercompat.Entry, 0, 6)
	entries = append(entries, settingsBackEntryLocale(locale))
	entries = append(entries, intpickercompat.Entry{
		Label:     settingsLabelInfoLocale(locale, "Version", "projmux "+version.String(), "https://github.com/crevissepartners/projmux"),
		Value:     settingsNoopValue,
		SearchKey: "version source build projmux",
	})
	entries = append(entries, intpickercompat.Entry{
		Label:     settingsNodeRowLabelLocale(locale, settingsNavAbout+".updates", settingsGlyphOpen, settingsColorType, c.aboutUpdatesSummary()),
		Value:     settingsAboutUpdates,
		SearchKey: "updates check update now latest installer release notes",
	})
	entries = append(entries, intpickercompat.Entry{
		Label: settingsNodeRowLabelLocale(locale, settingsNavAbout+".welcome", settingsGlyphOpen, settingsColorType, "revisit the shell quickstart guide"),
		Value: settingsWelcomeShow,
	})
	entries = append(entries, intpickercompat.Entry{
		Label:     settingsNodeRowLabelLocale(locale, settingsNavAbout+".quit", settingsGlyphRemove, settingsColorRemove, "stops the app-owned runtime and its socket"),
		Value:     settingsQuitOpen,
		SearchKey: "quit projmux runtime socket exit",
	})
	return entries
}

func (c *settingsCommand) aboutUpdatesSummary() string {
	if c.update == nil {
		return "status unavailable"
	}
	status, err := c.update.status()
	if err != nil {
		return "status unavailable"
	}
	latest := status.LatestVersion
	if latest == "" {
		latest = "unknown"
	}
	return strings.NewReplacer(
		"{current}", version.String(),
		"{latest}", latest,
	).Replace(localizeText(c.locale(), "settings.text.about_updates_summary", "current {current}, latest {latest}"))
}

// aboutUpdateEntries renders the Updates View: the read-only update state
// first, then the two observable actions.
func (c *settingsCommand) aboutUpdateEntries() []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	status, statusErr := updateStatus{}, errors.New("update status is not configured")
	if c.update != nil {
		status, statusErr = c.update.status()
	}
	entries := []intpickercompat.Entry{settingsBackEntryLocale(locale)}
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabelInfoLocale(locale, "Current", "projmux "+version.String(), ""),
		Value: settingsNoopValue,
	})
	if statusErr != nil {
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Update", "status unavailable", statusErr.Error()),
			Value: settingsNoopValue,
		})
	}
	latest := status.LatestVersion
	if latest == "" {
		latest = "unknown"
	}
	entries = append(entries,
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
	entries = append(entries,
		intpickercompat.Entry{
			Label: settingsNodeRowLabelLocale(locale, settingsNavAbout+".updates.check", settingsGlyphAdd, settingsColorAdd, "refresh cached GitHub release metadata"),
			Value: settingsUpdateCheck,
		},
		intpickercompat.Entry{
			Label: settingsNodeRowLabelLocale(locale, settingsNavAbout+".updates.apply", settingsGlyphAdd, settingsColorAdd, "run installer-specific update command"),
			Value: settingsUpdateApply,
		},
	)
	return entries
}

// runAboutSection drives the About container.
func (c *settingsCommand) runAboutSection(stdout, stderr io.Writer) error {
	for {
		options, err := c.sectionOptions(settingsSectionAbout)
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
		switch action {
		case settingsBackValue:
			return nil
		case settingsNoopValue:
			continue
		case settingsAboutUpdates:
			if err := c.runAboutUpdatesSection(stdout, stderr); err != nil {
				return err
			}
		default:
			if err := c.executeWithFeedback(action, stdout, stderr); err != nil {
				return err
			}
		}
	}
}

// runAboutUpdatesSection drives the Updates View.
func (c *settingsCommand) runAboutUpdatesSection(stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-about-updates",
			Entries:    c.aboutUpdateEntries(),
			Title:      "About - Updates",
			Prompt:     "Settings > About > Updates > ",
			Footer:     projmuxFooter("Enter: action  |  Back row: parent "),
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
		case strings.HasPrefix(action, settingsActionPrefixUpdate):
			if err := c.executeWithFeedback(action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown About updates action: %s", action)
		}
	}
}
