package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
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
		// Keep one textual explanation instead of repeating it on Trust,
		// Hooks, Project recipe, and Effective merge view. The disabled
		// Project chip remains the scope signal; this passive row explains
		// how to make that scope actionable.
		return []intpickercompat.Entry{
			{
				Label:     settingsRootLabelDim("Project context", "open Settings from a managed project to enable project actions"),
				Value:     settingsNoopValue,
				SearchKey: "project context managed project trust hooks recipe effective merge",
			},
		}
	}

	// The project context label is conveyed by the chip strip plus the
	// popup header — keep the picker entries focused on actionable rows.
	// The Project tab is two containers: Automation owns trust plus the
	// project-local lifecycle scripts, and Snapshots owns the auto-save
	// override and the saved snapshots.
	return []intpickercompat.Entry{
		{
			Label:     settingsRootLabel(settingsGlyphOpen, settingsNavLabel(settingsNavProjectAutomation), "trust and project lifecycle scripts in "+filepath.Join(ctx.Path, ".projmux")),
			Value:     settingsSectionProjectAutomation,
			SearchKey: "automation trust project hooks lifecycle send-noti config.toml",
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
		case action == settingsProjectsSidebar:
			if err := c.runProjectSidebarSection(stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixSwitch):
			if err := c.executeWithFeedback(action, stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixProjdir):
			if err := c.executeWithFeedback(action, stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixWorkdir):
			if err := c.executeWithFeedback(action, stdout, stderr); err != nil {
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
		Title:      "Select Project to pin - Choose a filesystem directory",
		Prompt:     "Settings > Projects > Pinned Projects > Select Project to pin > ",
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
	return c.executeWithFeedback(action, stdout, stderr)
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
			Title:      "Primary discovery root - Effective and saved root",
			Prompt:     "Settings > Projects > Primary discovery root > ",
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
		if err := c.executeWithFeedback(action, stdout, stderr); err != nil {
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
		Title:        "Primary discovery root - Type one absolute path",
		Prompt:       "Type primary discovery root path > ",
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
		c.setSettingsFeedback("Primary discovery root failed", err.Error())
		return nil
	}
	if !filepath.IsAbs(expanded) {
		message := fmt.Sprintf("project root must be an absolute path: %s", typed)
		fmt.Fprintln(stderr, message)
		c.setSettingsFeedback("Primary discovery root failed", message)
		return nil
	}
	return c.runSettingsMutation("Primary discovery root", stdout, stderr, func(out, _ io.Writer) error {
		return c.switcher.saveSavedProjdir(expanded, out)
	})
}

// promptSettingsPath opens the shared typed-path form. It is a transient
// interaction rather than a Settings route: closing it returns to the View
// that opened it.
func (c *settingsCommand) promptSettingsPath(title, prompt, initial string) (string, error) {
	result, err := c.runPicker(intpickercompat.Options{
		UI:           "settings-typed-path",
		Entries:      nil,
		AcceptQuery:  true,
		InitialQuery: initial,
		Title:        title,
		Prompt:       prompt,
		Footer:       projmuxFooter("Enter: save "),
		ExpectKeys:   []string{"enter"},
		Bindings:     c.settingsCloseBindings(),
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Query), nil
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
		Title:      "Add path - Choose or type a directory to scan",
		Prompt:     "Settings > Projects > Additional discovery roots > Add path > ",
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
	return c.executeWithFeedback(action, stdout, stderr)
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
		Title:       "Add path - Absolute discovery root path",
		Prompt:      "Type discovery root path > ",
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
		c.setSettingsFeedback("Discovery root failed", err.Error())
		return nil
	}

	if !filepath.IsAbs(expanded) {
		message := fmt.Sprintf("workdir must be an absolute path: %s", typed)
		fmt.Fprintln(stderr, message)
		c.setSettingsFeedback("Discovery root failed", message)
		return nil
	}

	warning := ""
	if info, statErr := c.statFile(expanded); statErr != nil {
		warning = fmt.Sprintf("warning: cannot stat workdir (continuing): %s: %v", expanded, statErr)
		fmt.Fprintln(stderr, warning)
	} else if !info.IsDir() {
		warning = fmt.Sprintf("warning: workdir is not a directory (continuing): %s", expanded)
		fmt.Fprintln(stderr, warning)
	}

	if err := c.runSettingsMutation("Workdir", stdout, stderr, func(out, _ io.Writer) error {
		return c.switcher.addWorkdir(expanded, out)
	}); err != nil {
		return err
	}
	if warning != "" && c.feedback != nil && strings.HasSuffix(c.feedback.Summary, " complete") {
		c.feedback.Detail = warning
	}
	return nil
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
			Title:      "Additional discovery roots - Saved and inherited scan roots",
			Prompt:     "Settings > Projects > Additional discovery roots > ",
			Footer:     projmuxFooter("Enter: open/add  |  Back row: parent "),
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
		if root, ok := strings.CutPrefix(action, settingsActionPrefixWorkdirItem); ok {
			if err := c.runDiscoveryRootDetail(root, stdout, stderr); err != nil {
				return err
			}
			continue
		}
		if err := c.executeWithFeedback(action, stdout, stderr); err != nil {
			return err
		}
	}
}

// workdirListEntries renders the Additional discovery roots collection. The
// collection owns Add; each saved root owns its own Remove one level down, so
// no row both navigates and mutates.
func (c *settingsCommand) workdirListEntries() ([]intpickercompat.Entry, error) {
	locale := appLocale(c.homeDir, c.lookupEnv)
	entries := []intpickercompat.Entry{settingsBackEntryLocale(locale)}
	if c.switcher == nil {
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, settingsNavLabel(settingsNavProjectsExtraRoots), "unavailable"),
			Value: settingsNoopValue,
		}), nil
	}

	saved, err := c.switcher.loadSavedWorkdirs()
	if err != nil {
		return nil, err
	}

	effective := "(none)"
	if len(saved) > 0 {
		effective = strconv.Itoa(len(saved)) + " saved"
	}
	entries = append(entries, intpickercompat.Entry{
		Label:     settingsLabelInfoLocale(locale, "Effective roots", effective, "~/.config/projmux/workdirs"),
		Value:     settingsNoopValue,
		SearchKey: "effective discovery roots source workdirs",
	})
	for _, dir := range saved {
		entries = append(entries, intpickercompat.Entry{
			Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, dir, "saved"),
			Value:     settingsActionPrefixWorkdirItem + dir,
			SearchKey: "discovery root " + dir + " saved remove",
		})
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
	entries = append(entries, c.addCurrentDiscoveryRootEntryLocale(locale, saved))
	entries = append(entries, intpickercompat.Entry{
		Label:     settingsLabelLocale(locale, settingsGlyphAdd, settingsColorAdd, settingsNavLabel(settingsNavProjectsExtraRoots+".add-path"), "choose or type a directory to scan"),
		Value:     settingsWorkdirAdd,
		SearchKey: "add discovery root path browse type",
	})
	return entries, nil
}

// addCurrentDiscoveryRootEntryLocale offers the current Project's directory as
// a discovery root. It reuses the shipped `workdir:add:<path>` handler; the row
// is disabled with its reason when there is no context or the root is already
// saved.
func (c *settingsCommand) addCurrentDiscoveryRootEntryLocale(locale i18n.Locale, saved []string) intpickercompat.Entry {
	label := settingsNavLabel(settingsNavProjectsExtraRoots + ".add-current")
	if c.switcher == nil {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, label, "unavailable"),
			Value: settingsNoopValue,
		}
	}
	current, err := c.switcher.resolveSwitchTarget(nil, "settings discovery roots")
	if err != nil || strings.TrimSpace(current) == "" || current == switchSettingsSentinel {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, label, "no project context"),
			Value: settingsNoopValue,
		}
	}
	if containsString(saved, current) {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, label, "already saved  "+current),
			Value: settingsNoopValue,
		}
	}
	return intpickercompat.Entry{
		Label:     settingsLabelLocale(locale, settingsGlyphAdd, settingsColorAdd, label, current),
		Value:     settingsActionPrefixWorkdir + "add:" + current,
		SearchKey: "add current directory discovery root " + current,
	}
}

// runDiscoveryRootDetail is one saved discovery root's item View: its path and
// source, plus the Remove the item owns.
func (c *settingsCommand) runDiscoveryRootDetail(root string, stdout, stderr io.Writer) error {
	for {
		locale := appLocale(c.homeDir, c.lookupEnv)
		entries := []intpickercompat.Entry{
			settingsBackEntryLocale(locale),
			{
				Label:     settingsLabelInfoLocale(locale, "Root path", root, "saved - ~/.config/projmux/workdirs"),
				Value:     settingsNoopValue,
				SearchKey: "discovery root path source " + root,
			},
			{
				Label:     settingsLabelLocale(locale, settingsGlyphRemove, settingsColorRemove, settingsNavLabel(settingsNavProjectsExtraRoots+".item.remove"), "stops scanning "+root),
				Value:     settingsActionPrefixWorkdir + "remove:" + root,
				SearchKey: "remove discovery root " + root,
			},
		}
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-workdir-item",
			Entries:    entries,
			Title:      "Additional discovery roots - " + root,
			Prompt:     "Settings > Projects > Additional discovery roots > " + root + " > ",
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
		case strings.HasPrefix(action, settingsActionPrefixWorkdir):
			if err := c.executeWithFeedback(action, stdout, stderr); err != nil {
				return err
			}
			// The item this View describes no longer exists.
			return nil
		default:
			return fmt.Errorf("unknown discovery root action: %s", action)
		}
	}
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
			Title:      "Pinned Projects - Pin, rebind, and unpin",
			Prompt:     "Settings > Projects > Pinned Projects > ",
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
		if pin, ok := strings.CutPrefix(action, settingsActionPrefixPinItem); ok {
			if err := c.runPinnedProjectDetail(pin, stdout, stderr); err != nil {
				return err
			}
			continue
		}
		if err := c.executeWithFeedback(action, stdout, stderr); err != nil {
			return err
		}
	}
}

// runPinnedProjectDetail is one pinned Project's item View. It shows the
// Project's canonical identity (unique name, display name, uid) separately
// from its bound root and runtime, keeps a `MissingRoot` condition visible
// instead of hiding it, and offers same-uid remediation through the canonical
// rebind route rather than re-pinning the Project under a new identity.
func (c *settingsCommand) runPinnedProjectDetail(pin string, stdout, stderr io.Writer) error {
	for {
		entries, err := c.pinnedProjectDetailEntries(pin)
		if err != nil {
			return err
		}
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-project-pin-item",
			Entries:    entries,
			Title:      "Pinned Projects - " + pin,
			Prompt:     "Settings > Projects > Pinned Projects > " + pin + " > ",
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
		case action == settingsActionPrefixPinItem+pin+":rebind":
			if err := c.runRebindPinnedProjectRoot(pin, stdout, stderr); err != nil {
				return err
			}
		case action == settingsActionPrefixSwitch+"pin:"+pin:
			if err := c.executeWithFeedback(action, stdout, stderr); err != nil {
				return err
			}
			// The pin this View describes is gone.
			return nil
		default:
			return fmt.Errorf("unknown pinned project action: %s", action)
		}
	}
}

func (c *settingsCommand) pinnedProjectDetailEntries(pin string) ([]intpickercompat.Entry, error) {
	locale := appLocale(c.homeDir, c.lookupEnv)
	entries := []intpickercompat.Entry{settingsBackEntryLocale(locale)}
	registry := c.settingsProjectRegistry()
	project, registered := settingsProjectForRoot(registry, pin)

	if !registered {
		entries = append(entries, intpickercompat.Entry{
			Label:     settingsLabelInfoLocale(locale, "Project", pin, "pinned path, not registered as a Project resource"),
			Value:     settingsNoopValue,
			SearchKey: "pinned project path unregistered " + pin,
		})
	} else {
		display := strings.TrimSpace(project.Metadata.DisplayName)
		if display == "" {
			display = "(none)"
		}
		runtime := "offline"
		if project.Status.Session != nil {
			runtime = "session " + project.Status.Session.Name
		}
		condition := "Ready"
		missingSince := "-"
		if missing, ok := settingsProjectMissingRoot(project); ok {
			condition = coremetadata.ConditionMissingRoot
			missingSince = missing.FirstObservedAt.Format(time.RFC3339)
		}
		for _, row := range [][3]string{
			{"Display name", display, "duplicates allowed"},
			{"Unique name", project.Metadata.Name, "stable query name"},
			{"UID", project.Metadata.UID, "never changes across a rebind"},
			{"Root", project.Spec.Root, "spec.root"},
			{"Condition", condition, "registry status"},
			{"Missing since", missingSince, "first observed"},
			{"Runtime", runtime, "status.session"},
		} {
			entries = append(entries, intpickercompat.Entry{
				Label:     settingsLabelInfoLocale(locale, row[0], row[1], row[2]),
				Value:     settingsNoopValue,
				SearchKey: "project " + row[0] + " " + row[1],
			})
		}
	}

	entries = append(entries, c.rebindPinnedProjectEntry(locale, pin, project, registered))
	entries = append(entries, intpickercompat.Entry{
		Label:     settingsLabelLocale(locale, settingsGlyphRemove, settingsColorRemove, settingsNavLabel(settingsNavProjectsPins+".item.unpin"), "removes the pin; Project metadata is kept"),
		Value:     settingsActionPrefixSwitch + "pin:" + pin,
		SearchKey: "unpin project " + pin,
	})
	return entries, nil
}

func (c *settingsCommand) rebindPinnedProjectEntry(locale i18n.Locale, pin string, project coremetadata.Project, registered bool) intpickercompat.Entry {
	label := settingsNavLabel(settingsNavProjectsPins + ".item.rebind")
	if !registered {
		return intpickercompat.Entry{
			Label:     settingsLabelDimLocale(locale, label, "unavailable - this path is not a registered Project"),
			Value:     settingsNoopValue,
			SearchKey: "rebind project root unavailable " + pin,
		}
	}
	return intpickercompat.Entry{
		Label:     settingsLabelLocale(locale, settingsGlyphAdd, settingsColorAdd, label, "keeps uid "+project.Metadata.UID),
		Value:     settingsActionPrefixPinItem + pin + ":rebind",
		SearchKey: "rebind project root missing root same uid " + pin,
	}
}

// runRebindPinnedProjectRoot types a new absolute root and hands it to the
// canonical `rebind project` handler. Settings adds no second rebind
// implementation, so the uid, the name reservation and the collision refusals
// are exactly the CLI's.
func (c *settingsCommand) runRebindPinnedProjectRoot(pin string, stdout, stderr io.Writer) error {
	registry := c.settingsProjectRegistry()
	project, ok := settingsProjectForRoot(registry, pin)
	if !ok {
		return fmt.Errorf("rebind project root: %s is not a registered Project", pin)
	}
	typed, err := c.promptSettingsPath("Rebind Project root - Type one absolute Project root",
		"Settings > Projects > Pinned Projects > "+pin+" > Rebind Project root > ", pin)
	if err != nil {
		return err
	}
	if strings.TrimSpace(typed) == "" {
		return nil
	}
	root, err := c.expandTypedPath(typed, "project root")
	if err != nil {
		c.setSettingsFeedback("Rebind Project root failed", err.Error())
		return nil
	}
	return c.runSettingsMutation("Rebind Project root", stdout, stderr, func(out, errOut io.Writer) error {
		return newRebindCommand().Run([]string{"project", "uid:" + project.Metadata.UID, "--root", root}, out, errOut)
	})
}

// projectPickerEntries renders the Projects container. Settings owns Project
// discovery, pins and sidebar policy here; the runtime picker UI keeps the name
// `Project Picker`, which is why this container is not called that.
func (c *settingsCommand) projectPickerEntries() []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	entries := []intpickercompat.Entry{
		settingsBackEntryLocale(locale),
	}

	entries = append(entries, c.projectRootEntryLocale(locale))
	entries = append(entries, intpickercompat.Entry{
		Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, settingsNavLabel(settingsNavProjectsExtraRoots), "discovery roots scanned for Projects"),
		Value:     settingsWorkdirList,
		SearchKey: "additional discovery roots workdirs scan roots",
	})
	entries = append(entries, intpickercompat.Entry{
		Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, settingsNavLabel(settingsNavProjectsPins), "pinned Projects and their roots"),
		Value:     settingsProjectPins,
		SearchKey: "pinned projects pin unpin rebind root",
	})
	entries = append(entries, intpickercompat.Entry{
		Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, settingsNavLabel(settingsNavProjectsSidebar), c.projectSidebarSummary()),
		Value:     settingsProjectsSidebar,
		SearchKey: "project sidebar closed project startup snapshot topology",
	})
	return entries
}

// projectSidebarSummary reports the closed-Project startup policy. The saved
// file and its `sidebar-startup-picker` spelling are unchanged; only the
// destination and the wording move.
func (c *settingsCommand) projectSidebarSummary() string {
	startup := c.currentSidebarStartupPicker()
	if startup.Mode.Enabled() {
		return "closed Project startup: ask for Snapshot or Project topology"
	}
	return "closed Project startup: use Project topology"
}

// runProjectSidebarSection is the Projects > Project Sidebar view.
func (c *settingsCommand) runProjectSidebarSection(stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-projects-sidebar",
			Entries:    c.projectSidebarEntries(),
			Title:      "Projects - Project Sidebar",
			Prompt:     "Settings > Projects > Project Sidebar > ",
			Footer:     projmuxFooter("Enter: open  |  Back row: parent "),
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
		switch action {
		case settingsBackValue:
			return nil
		case settingsNoopValue:
			continue
		case settingsSessionStateSidebarStartupPickerDetail:
			if err := c.runSidebarStartupPickerDetail(stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown project sidebar action: %s", action)
		}
	}
}

func (c *settingsCommand) projectSidebarEntries() []intpickercompat.Entry {
	locale := appLocale(c.homeDir, c.lookupEnv)
	startup := c.currentSidebarStartupPicker()
	choice := "Use Project topology"
	if startup.Mode.Enabled() {
		choice = "Ask for Snapshot or Project topology"
	}
	return []intpickercompat.Entry{
		settingsBackEntryLocale(locale),
		{
			Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, settingsNavLabel(settingsNavProjectsSidebar+".closed-startup"), choice+" - "+startup.Source),
			Value:     settingsSessionStateSidebarStartupPickerDetail,
			SearchKey: "closed project startup snapshot topology sidebar startup picker",
		},
	}
}

func (c *settingsCommand) projectRootEntryLocale(locale i18n.Locale) intpickercompat.Entry {
	if c.switcher == nil {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Primary discovery root", "unavailable"),
			Value: settingsNoopValue,
		}
	}
	value, source, err := c.switcher.currentProjdirInfo()
	if err != nil || value == "" {
		return intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Primary discovery root", "not configured"),
			Value: settingsProjectRootManage,
		}
	}
	return intpickercompat.Entry{
		Label: settingsLabelInfoLocale(locale, "Primary discovery root", value, source),
		Value: settingsProjectRootManage,
	}
}

func (c *settingsCommand) projectRootHintEntryLocale(locale i18n.Locale) intpickercompat.Entry {
	// Keep the entire hint in one dim run so search substrings such as
	// "Set PROJMUX_PROJDIR" stay contiguous in the rendered label.
	return intpickercompat.Entry{
		Label: "  " + settingsColorDim + settingsCatalogTextLocale(locale, "The primary discovery root is the first root scanned for Projects. Additional discovery roots extend the search. Set PROJMUX_PROJDIR, @projmux_projdir, or the saved ~/.config/projmux/projdir value.") + settingsColorReset,
		Value: settingsNoopValue,
	}
}

func (c *settingsCommand) projectRootEntries() ([]intpickercompat.Entry, error) {
	locale := appLocale(c.homeDir, c.lookupEnv)
	entries := []intpickercompat.Entry{settingsBackEntryLocale(locale)}
	if c.switcher == nil {
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "Primary discovery root", "unavailable"),
			Value: settingsNoopValue,
		}), nil
	}

	info, err := c.switcher.projdirSettingsInfo()
	if err != nil {
		return nil, err
	}

	if info.EffectiveValue == "" {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Effective discovery root", "not configured", "no env, tmux option, or saved value"),
			Value: settingsNoopValue,
		})
	} else {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Effective discovery root", info.EffectiveValue, info.EffectiveSource),
			Value: settingsNoopValue,
		})
	}

	switch {
	case info.SavedValue == "":
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Saved discovery root", "not set", "~/.config/projmux/projdir"),
			Value: settingsNoopValue,
		})
	case info.EffectiveSource == projdirSourceSaved:
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Saved discovery root", info.SavedValue, "active"),
			Value: settingsNoopValue,
		})
	case info.EffectiveSource == projdirSourceUnresolved:
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Saved discovery root", info.SavedValue, "saved"),
			Value: settingsNoopValue,
		})
	default:
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfoLocale(locale, "Saved discovery root", info.SavedValue, "shadowed by "+info.EffectiveSource),
			Value: settingsNoopValue,
		})
	}

	entries = append(entries,
		intpickercompat.Entry{
			Label: settingsLabelLocale(locale, settingsGlyphAdd, settingsColorAdd, "Enter path", "save one primary discovery root directly"),
			Value: settingsProjdirSetTyped,
		},
		c.setCurrentProjectRootEntryLocale(locale),
		intpickercompat.Entry{
			Label: settingsLabelLocale(locale, settingsGlyphRemove, settingsColorRemove, "Clear saved root", "remove ~/.config/projmux/projdir"),
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

// pinnedProjectEntries renders the Pinned Projects collection. Each pin is an
// item View; unpinning and root remediation belong to the item, and only the
// two add rows belong to the collection.
func (c *settingsCommand) pinnedProjectEntries() ([]intpickercompat.Entry, error) {
	locale := appLocale(c.homeDir, c.lookupEnv)
	entries := []intpickercompat.Entry{settingsBackEntryLocale(locale)}
	if c.switcher == nil {
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "(no pinned Projects)", ""),
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
	registry := c.settingsProjectRegistry()

	if len(pins) == 0 {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelDimLocale(locale, "(no pinned Projects)", ""),
			Value: settingsNoopValue,
		})
	}
	for _, pin := range pins {
		entries = append(entries, intpickercompat.Entry{
			Label:     settingsLabelLocale(locale, settingsGlyphOpen, settingsColorType, settingsPinnedProjectName(registry, pin, homeDir, repoRoot), settingsPinnedProjectSummary(registry, pin)),
			Value:     settingsActionPrefixPinItem + pin,
			SearchKey: "pinned project " + pin + " unpin rebind root condition",
		})
	}
	entries = append(entries, c.addCurrentProjectEntryLocale(locale))
	entries = append(entries, intpickercompat.Entry{
		Label:     settingsLabelLocale(locale, settingsGlyphAdd, settingsColorAdd, settingsNavLabel(settingsNavProjectsPins+".select"), "scan filesystem roots"),
		Value:     settingsProjectAdd,
		SearchKey: "select project to pin scan filesystem roots",
	})
	return entries, nil
}

// settingsProjectRegistry reads the resource registry without creating any
// state. Settings only displays this metadata; identity allocation, uid merges
// and pruning stay out of the Settings surface entirely.
func (c *settingsCommand) settingsProjectRegistry() coremetadata.Registry {
	registry, err := loadResourceRegistry()
	if err != nil {
		return coremetadata.Registry{}
	}
	return registry
}

// settingsProjectForRoot resolves the Project bound to an exact cleaned root.
// The comparison is exact by design: a shared basename, a shared git origin or
// a shared inode must never fold two Projects onto one identity.
func settingsProjectForRoot(registry coremetadata.Registry, root string) (coremetadata.Project, bool) {
	want := filepath.Clean(strings.TrimSpace(root))
	for _, project := range registry.Projects {
		if filepath.Clean(project.Spec.Root) == want {
			return project, true
		}
	}
	return coremetadata.Project{}, false
}

func settingsPinnedProjectName(registry coremetadata.Registry, pin, homeDir, repoRoot string) string {
	if project, ok := settingsProjectForRoot(registry, pin); ok {
		if display := strings.TrimSpace(project.Metadata.DisplayName); display != "" {
			return display
		}
		return project.Metadata.Name
	}
	return intrender.PrettyPath(pin, homeDir, repoRoot)
}

func settingsPinnedProjectSummary(registry coremetadata.Registry, pin string) string {
	project, ok := settingsProjectForRoot(registry, pin)
	if !ok {
		return pin + " - not registered"
	}
	if condition, found := settingsProjectMissingRoot(project); found {
		return pin + " - MissingRoot since " + condition.FirstObservedAt.Format(time.RFC3339)
	}
	return pin
}

func settingsProjectMissingRoot(project coremetadata.Project) (coremetadata.Condition, bool) {
	for _, condition := range project.Status.Conditions {
		if condition.Type == coremetadata.ConditionMissingRoot && strings.EqualFold(condition.Status, "True") {
			return condition, true
		}
	}
	return coremetadata.Condition{}, false
}
