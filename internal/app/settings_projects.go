package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/i18n"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
	intrender "github.com/crevissepartners/projmux/internal/ui/render"
)

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
				Label: settingsRootLabelDim("Trust", "disabled - no project context"),
				Value: settingsNoopValue,
			},
			{
				Label: settingsRootLabelDim("Hooks (project)", "disabled - no project context"),
				Value: settingsNoopValue,
			},
			{
				Label:     settingsRootLabelDim("Project recipe", "disabled - no project context"),
				Value:     settingsNoopValue,
				SearchKey: "Project recipe config.toml",
			},
			{
				Label: settingsRootLabelDim("Effective merge view", "disabled - no project context"),
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
			Label: settingsRootLabel(settingsGlyphOpen, "Hooks (project)", filepath.Join(ctx.Path, ".projmux")),
			Value: settingsSectionProjectHooks,
		},
		{
			Label:     settingsRootLabel(settingsGlyphOpen, "Project recipe", "declare env, kube, startup"),
			Value:     settingsSectionProjectConfig,
			SearchKey: "Project recipe config.toml project config env kube startup",
		},
		{
			Label: settingsRootLabel(settingsGlyphOpen, "Effective merge view", "global + project merge with source labels"),
			Value: settingsSectionEffectiveMerge,
		},
		{
			Label: c.projectSessionStateSettingsRootLabel(ctx),
			Value: settingsSectionProjectSessionState,
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
	_, err := os.Stat(path)
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
	locale := appLocale(c.homeDir, c.lookupEnv)
	entries = append([]intpickercompat.Entry{settingsBackEntryLocale(locale)}, entries...)

	result, err := c.runPicker(intpickercompat.Options{
		UI:         "settings-project-add",
		Entries:    entries,
		Title:      "Add Project - Choose a filesystem directory",
		Prompt:     "Settings > Project Picker > Add Project > ",
		Footer:     projmuxFooter("Enter: add  |  Back row: parent "),
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
		Footer:       projmuxFooter("Enter: save "),
		ExpectKeys:   []string{"enter"},
		Bindings:     c.settingsCloseBindings(),
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
	locale := appLocale(c.homeDir, c.lookupEnv)
	entries = append([]intpickercompat.Entry{
		settingsBackEntryLocale(locale),
		settingsWorkdirTypedEntryLocale(locale),
	}, entries...)

	result, err := c.runPicker(intpickercompat.Options{
		UI:         "settings-workdir-add",
		Entries:    entries,
		Title:      "Add Workdir - Choose or type a directory to scan",
		Prompt:     "Settings > Project Picker > Add Workdir > ",
		Footer:     projmuxFooter("Enter: add  |  Back row: parent "),
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
	return settingsWorkdirTypedEntryLocale(settingsLocale())
}

func settingsWorkdirTypedEntryLocale(locale i18n.Locale) intpickercompat.Entry {
	return intpickercompat.Entry{
		Label: settingsLabelLocale(locale, settingsGlyphType, settingsColorType, "Type path manually...", "skip filesystem scan"),
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
		Footer:      projmuxFooter("Enter: add "),
		ExpectKeys:  []string{"enter"},
		Bindings:    c.settingsCloseBindings(),
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

	if info, statErr := c.statFile(expanded); statErr != nil {
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
			Footer:     projmuxFooter("Enter: open/add/remove  |  Back row: parent "),
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
	locale := appLocale(c.homeDir, c.lookupEnv)
	entries := []intpickercompat.Entry{settingsBackEntryLocale(locale)}
	if c.switcher == nil {
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Saved workdirs", "unavailable"),
			Value: settingsNoopValue,
		}), nil
	}

	saved, err := c.switcher.loadSavedWorkdirs()
	if err != nil {
		return nil, err
	}

	if len(saved) == 0 {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Saved workdirs", "(none)", "~/.config/projmux/workdirs"),
			Value: settingsNoopValue,
		})
	} else {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Saved workdirs", strconv.Itoa(len(saved)), "~/.config/projmux/workdirs"),
			Value: settingsNoopValue,
		})
		for _, dir := range saved {
			entries = append(entries, intpickercompat.Entry{
				Label: settingsLabelLocale(locale, settingsGlyphRemove, settingsColorRemove, "Remove", dir+"  (saved)"),
				Value: settingsActionPrefixWorkdir + "remove:" + dir,
			})
		}
	}

	for _, src := range c.switcher.envWorkdirSources() {
		if strings.TrimSpace(src.Value) == "" {
			continue
		}
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, src.Name, src.Value, "env, read-only"),
			Value: settingsNoopValue,
		})
	}
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabelLocale(locale, settingsGlyphAdd, settingsColorAdd, "Add Workdir...", "append a directory to the saved workdirs list"),
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
	locale := appLocale(c.homeDir, c.lookupEnv)
	entries := []intpickercompat.Entry{
		settingsBackEntryLocale(locale),
	}

	entries = append(entries, c.projectRootEntryLocale(locale))
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Workdirs", "add or remove scan roots"),
		Value: settingsWorkdirList,
	})
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, "Pinned Projects", "add or remove pins"),
		Value: settingsProjectPins,
	})
	return entries
}

// projectRootEntry renders the resolved primary root with its source label.
// Opening it manages the saved project root; rendering never memoizes env state.
func (c *settingsCommand) projectRootEntry() intpickercompat.Entry {
	return c.projectRootEntryLocale(appLocale(c.homeDir, c.lookupEnv))
}

func (c *settingsCommand) projectRootEntryLocale(locale i18n.Locale) intpickercompat.Entry {
	if c.switcher == nil {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Project Root", "unavailable"),
			Value: settingsNoopValue,
		}
	}
	value, source, err := c.switcher.currentProjdirInfo()
	if err != nil || value == "" {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Project Root", "not configured"),
			Value: settingsProjectRootManage,
		}
	}
	return intpickercompat.Entry{
		Label: settingsLabelInfoLocale(locale, "Project Root", value, source),
		Value: settingsProjectRootManage,
	}
}

func (c *settingsCommand) projectRootHintEntry() intpickercompat.Entry {
	return c.projectRootHintEntryLocale(settingsLocale())
}

func (c *settingsCommand) projectRootHintEntryLocale(locale i18n.Locale) intpickercompat.Entry {
	// Keep the entire hint in one dim run so search substrings such as
	// "Set PROJMUX_PROJDIR" stay contiguous in the rendered label.
	return intpickercompat.Entry{
		Label: "  " + settingsColorDim + settingsCatalogTextLocale(locale, "Project Root is the primary root. Workdirs are extra search roots. Set PROJMUX_PROJDIR, @projmux_projdir, or the saved ~/.config/projmux/projdir value.") + settingsColorReset,
		Value: settingsNoopValue,
	}
}

func (c *settingsCommand) projectRootEntries() ([]intpickercompat.Entry, error) {
	locale := appLocale(c.homeDir, c.lookupEnv)
	entries := []intpickercompat.Entry{settingsBackEntryLocale(locale)}
	if c.switcher == nil {
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Project Root", "unavailable"),
			Value: settingsNoopValue,
		}), nil
	}

	info, err := c.switcher.projdirSettingsInfo()
	if err != nil {
		return nil, err
	}

	if info.EffectiveValue == "" {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Effective Project Root", "not configured", "no env, tmux option, or saved value"),
			Value: settingsNoopValue,
		})
	} else {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Effective Project Root", info.EffectiveValue, info.EffectiveSource),
			Value: settingsNoopValue,
		})
	}

	switch {
	case info.SavedValue == "":
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Saved Project Root", "not set", "~/.config/projmux/projdir"),
			Value: settingsNoopValue,
		})
	case info.EffectiveSource == projdirSourceSaved:
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Saved Project Root", info.SavedValue, "active"),
			Value: settingsNoopValue,
		})
	case info.EffectiveSource == projdirSourceUnresolved:
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Saved Project Root", info.SavedValue, "saved"),
			Value: settingsNoopValue,
		})
	default:
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Saved Project Root", info.SavedValue, "shadowed by "+info.EffectiveSource),
			Value: settingsNoopValue,
		})
	}

	entries = append(entries,
		intpickercompat.Entry{
			Label: settingsLabelLocale(locale, settingsGlyphAdd, settingsColorAdd, "Set Project Root...", "save one primary root path directly"),
			Value: settingsProjdirSetTyped,
		},
		c.setCurrentProjectRootEntryLocale(locale),
		intpickercompat.Entry{
			Label: settingsLabelLocale(locale, settingsGlyphRemove, settingsColorRemove, "Clear Saved Project Root", "remove ~/.config/projmux/projdir"),
			Value: settingsProjdirClear,
		},
		c.projectRootHintEntryLocale(locale),
		intpickercompat.Entry{
			Label: "  " + settingsColorDim + settingsCatalogTextLocale(locale, "Env PROJMUX_PROJDIR and tmux @projmux_projdir override the saved value until unset.") + settingsColorReset,
			Value: settingsNoopValue,
		},
	)
	return entries, nil
}

func (c *settingsCommand) setCurrentProjectRootEntry() intpickercompat.Entry {
	return c.setCurrentProjectRootEntryLocale(appLocale(c.homeDir, c.lookupEnv))
}

func (c *settingsCommand) setCurrentProjectRootEntryLocale(locale i18n.Locale) intpickercompat.Entry {
	if c.switcher == nil {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Use Current Project as Root", "unavailable"),
			Value: settingsNoopValue,
		}
	}

	homeDir, err := c.switcher.resolveHomeDir()
	if err != nil {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Use Current Project as Root", "home unavailable"),
			Value: settingsNoopValue,
		}
	}
	repoRoot, _, _ := c.switcher.currentProjdirInfo()
	currentTarget, err := c.switcher.resolveSwitchTargetNoMemoize(nil, "settings project root")
	if err != nil || currentTarget == "" || currentTarget == switchSettingsSentinel {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Use Current Project as Root", "no project context"),
			Value: settingsNoopValue,
		}
	}
	return intpickercompat.Entry{
		Label: settingsLabelLocale(locale, settingsGlyphAdd, settingsColorAdd, "Use Current Project as Root", intrender.PrettyPath(currentTarget, homeDir, repoRoot)),
		Value: settingsProjdirSetCurrent,
	}
}

func (c *settingsCommand) addCurrentProjectEntry() intpickercompat.Entry {
	return c.addCurrentProjectEntryLocale(appLocale(c.homeDir, c.lookupEnv))
}

func (c *settingsCommand) addCurrentProjectEntryLocale(locale i18n.Locale) intpickercompat.Entry {
	if c.switcher == nil {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Add Current Project", "unavailable"),
			Value: settingsNoopValue,
		}
	}

	pins, err := c.switcher.loadPins()
	if err != nil {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Add Current Project", "pins unavailable"),
			Value: settingsNoopValue,
		}
	}
	homeDir, err := c.switcher.resolveHomeDir()
	if err != nil {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Add Current Project", "home unavailable"),
			Value: settingsNoopValue,
		}
	}
	repoRoot := c.switcher.switchRepoRoot(homeDir)
	currentTarget, err := c.switcher.resolveSwitchTarget(nil, "settings project picker")
	if err != nil || currentTarget == "" || currentTarget == switchSettingsSentinel {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Add Current Project", "no project context"),
			Value: settingsNoopValue,
		}
	}
	if containsString(pins, currentTarget) {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Add Current Project", "already pinned  "+intrender.PrettyPath(currentTarget, homeDir, repoRoot)),
			Value: settingsNoopValue,
		}
	}
	return intpickercompat.Entry{
		Label: settingsLabelLocale(locale, settingsGlyphAdd, settingsColorAdd, "Add Current Project", intrender.PrettyPath(currentTarget, homeDir, repoRoot)),
		Value: settingsActionPrefixSwitch + "add:" + currentTarget,
	}
}

func (c *settingsCommand) pinnedProjectEntries() ([]intpickercompat.Entry, error) {
	locale := appLocale(c.homeDir, c.lookupEnv)
	entries := []intpickercompat.Entry{
		settingsBackEntryLocale(locale),
		{
			Label: settingsLabelLocale(locale, settingsGlyphAdd, settingsColorAdd, "Add Project...", "scan filesystem roots"),
			Value: settingsProjectAdd,
		},
		c.addCurrentProjectEntryLocale(locale),
	}
	if c.switcher == nil {
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "(no pinned projects)", ""),
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
			Label: settingsLabelDimLocale(locale, "(no pinned projects)", ""),
			Value: settingsNoopValue,
		}), nil
	}

	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabelLocale(locale, settingsGlyphRemove, settingsColorRemove, "Clear all pins", ""),
		Value: settingsActionPrefixSwitch + "clear",
	})
	for _, pin := range pins {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelLocale(locale, settingsGlyphRemove, settingsColorRemove, "Remove", intrender.PrettyPath(pin, homeDir, repoRoot)),
			Value: settingsActionPrefixSwitch + "pin:" + pin,
		})
	}
	return entries, nil
}
