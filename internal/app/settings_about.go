package app

import (
	"errors"

	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
	"github.com/crevissepartners/projmux/internal/version"
)

func (c *settingsCommand) aboutEntries() []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	status, statusErr := updateStatus{}, errors.New("update status is not configured")
	if c.update != nil {
		status, statusErr = c.update.status()
	}

	rows := []struct{ name, value string }{
		{"Version", "projmux " + version.String()},
		{"Source", "https://github.com/crevissepartners/projmux"},
	}
	entries := make([]intpickercompat.Entry, 0, len(rows)+8)
	entries = append(entries, settingsBackEntryLocale(locale))
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Welcome", "revisit the shell quickstart guide"),
		Value: settingsWelcomeShow,
	})
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabelLocale(locale, settingsGlyphRemove, settingsColorRemove, "Quit projmux", "open quit actions"),
		Value: settingsQuitOpen,
	})
	if statusErr != nil {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Update", "status unavailable", statusErr.Error()),
			Value: settingsNoopValue,
		})
	} else {
		latest := status.LatestVersion
		if latest == "" {
			latest = "unknown"
		}
		entries = append(entries,
			intpickercompat.Entry{
				Label: settingsLabelLocale(locale, settingsGlyphAdd, settingsColorAdd, "Update Now", "run installer-specific update command"),
				Value: settingsUpdateApply,
			},
			intpickercompat.Entry{
				Label: settingsLabelLocale(locale, settingsGlyphAdd, settingsColorAdd, "Check Updates", "refresh cached GitHub release metadata"),
				Value: settingsUpdateCheck,
			},
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
	}
	for _, r := range rows {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, r.name, r.value, ""),
			Value: settingsNoopValue,
		})
	}
	return entries
}
