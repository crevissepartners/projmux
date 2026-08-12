package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

type settingsHookEvent struct {
	Name string
}

var settingsHookEvents = []settingsHookEvent{
	{Name: string(hooks.EventPreCreate)},
	{Name: string(hooks.EventPostCreate)},
	{Name: string(hooks.EventPostAttach)},
	{Name: string(hooks.EventSendNoti)},
}

// Hook maker action prefixes. Phase 2.6 dropped the script/declarative
// distinction, so each action is now just
//
//	hook-<op>:<scope>:<event>
//
// where scope is "global" or "project" and op is one of add, edit, remove.
// The [+ Add] picker always opens the declarative one-line editor.
const (
	settingsActionPrefixHookAdd    = "hook-add:"
	settingsActionPrefixHookEdit   = "hook-edit:"
	settingsActionPrefixHookRemove = "hook-remove:"
	settingsActionPrefixHookView   = "hook-view:"

	hookScopeGlobal  = "global"
	hookScopeProject = "project"
)

// settingsHookRow describes one lifecycle event's declarative entry plus any
// legacy script file that the migrator could not flatten. Empty fields render
// as a missing row with a [+ Add] button.
type settingsHookRow struct {
	Event      string
	Scope      string
	Declared   string
	ConfigPath string
	// Legacy is set when a multi-line script remains in the historical
	// location after migration. The runner does not execute it; the row
	// exists to nudge the user toward manual cleanup.
	Legacy settingsLegacyScript
}

type settingsLegacyScript struct {
	Path  string
	Lines int
	// Symlink is true when Path resolves to a symbolic link via Lstat. The
	// migrator never touches symlinks (they typically come from a dotfiles
	// repo), and the UI surfaces a dedicated message so the user knows to
	// clean it up at the source instead of expecting projmux to rename it.
	Symlink bool
}

func (c *settingsCommand) globalHookEntries() []intpickercompat.Entry {
	entries := []intpickercompat.Entry{settingsBackEntry()}

	configPath, err := c.globalConfigPath()
	if err != nil {
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelDim("Hooks", err.Error()),
			Value: settingsNoopValue,
		})
	}
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabelInfo("Global config", configPath, "[hooks.<event>]"),
		Value: settingsNoopValue,
	})

	cfg, _ := hooks.LoadGlobalConfig(configPath)
	legacy := globalLegacyScriptMap(c.homeDir, c.lookupEnv)

	for _, event := range settingsHookEvents {
		row := settingsHookRow{
			Event:      event.Name,
			Scope:      hookScopeGlobal,
			Declared:   declaredHookRun(cfg, event.Name),
			ConfigPath: configPath,
			Legacy:     legacy[event.Name],
		}
		entries = append(entries, renderHookRowEntries(row)...)
	}
	return entries
}

func (c *settingsCommand) projectHookEntries(ctx settingsProjectContext) []intpickercompat.Entry {
	entries := []intpickercompat.Entry{settingsBackEntry()}
	if !ctx.hasProject() {
		// Phase 2.7: the frame title chip strip already announces the
		// active scope, so drop the redundant "Project context" row.
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelDim("Hooks (project)", "disabled - no project context"),
			Value: settingsNoopValue,
		})
	}

	// Phase 2.7: drop the "Project context" info row — chip strip is the
	// source of truth.

	configPath := filepath.Join(ctx.Path, ".projmux", "config.toml")
	cfg, _ := loadProjectConfigForRead(configPath)
	legacy := projectLegacyScriptMap(ctx.Path)

	for _, event := range settingsHookEvents {
		row := settingsHookRow{
			Event:      event.Name,
			Scope:      hookScopeProject,
			Declared:   declaredHookRun(cfg, event.Name),
			ConfigPath: configPath,
			Legacy:     legacy[event.Name],
		}
		entries = append(entries, renderHookRowEntries(row)...)
	}
	return entries
}

func (c *settingsCommand) runProjectHooksSection(stdout, stderr io.Writer) error {
	// Best-effort migration on first entry so users on Phase 2.5 see their
	// existing single-line scripts as declarative entries immediately.
	if ctx := c.resolveSettingsProjectContext(); ctx.hasProject() {
		_, _ = hooks.MigrateProjectLegacyScripts(ctx.Path, "", stderr)
	}
	for {
		ctx := c.resolveSettingsProjectContext()
		options, err := c.sectionOptions(settingsSectionProjectHooks)
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
		case strings.HasPrefix(action, settingsActionPrefixHookAdd),
			strings.HasPrefix(action, settingsActionPrefixHookEdit),
			strings.HasPrefix(action, settingsActionPrefixHookRemove),
			strings.HasPrefix(action, settingsActionPrefixHookView):
			if err := c.runHookMakerActionWithFeedback(ctx, action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown project hook settings action: %s", action)
		}
	}
}

func (c *settingsCommand) runGlobalHooksSection(stdout, stderr io.Writer) error {
	// Same best-effort migration for the global hooks dir.
	_, _ = hooks.MigrateGlobalLegacyScripts(c.lookupEnv, c.homeDir, "", stderr)
	for {
		options, err := c.sectionOptions(settingsSectionGlobalHooks)
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
		case strings.HasPrefix(action, settingsActionPrefixHookAdd),
			strings.HasPrefix(action, settingsActionPrefixHookEdit),
			strings.HasPrefix(action, settingsActionPrefixHookRemove),
			strings.HasPrefix(action, settingsActionPrefixHookView):
			if err := c.runHookMakerActionWithFeedback(settingsProjectContext{}, action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown hook settings action: %s", action)
		}
	}
}

// renderHookRowEntries emits picker rows for a single lifecycle event using
// the Phase 2.6 declarative-only model. The row is one of:
//   - active: a single edit row whose label includes the run line
//   - missing: a single [+ Add] row that opens the inline editor
//
// When a legacy multi-line script remains in the historical location, an
// additional dim "(legacy script)" row is appended below to nudge the user
// toward manual cleanup.
func renderHookRowEntries(row settingsHookRow) []intpickercompat.Entry {
	entries := make([]intpickercompat.Entry, 0, 2)
	if strings.TrimSpace(row.Declared) != "" {
		entries = append(entries, settingsHookActiveEntry(row))
	} else if hookRowInAppEditable(row) {
		entries = append(entries, settingsHookAddRow(row))
	} else {
		entries = append(entries, settingsHookReadonlyMissingEntry(row))
	}
	if row.Legacy.Path != "" {
		entries = append(entries, settingsHookLegacyEntry(row))
	}
	return entries
}

func hookRowInAppEditable(row settingsHookRow) bool {
	return row.Scope == hookScopeProject
}

func settingsHookAddRow(row settingsHookRow) intpickercompat.Entry {
	desc := "missing - " + row.ConfigPath + " [hooks." + row.Event + "] [+ Add]"
	return intpickercompat.Entry{
		Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, row.Event, desc),
		Value: settingsActionPrefixHookAdd + row.Scope + ":" + row.Event,
	}
}

func settingsHookActiveEntry(row settingsHookRow) intpickercompat.Entry {
	desc := "active - run = " + row.Declared
	value := settingsActionPrefixHookEdit + row.Scope + ":" + row.Event
	glyph := settingsGlyphToggle
	color := settingsColorAdd
	search := row.Event + " active run " + row.Declared
	if !hookRowInAppEditable(row) {
		desc = "read-only - " + row.ConfigPath + " [hooks." + row.Event + "]"
		value = settingsActionPrefixHookView + row.Scope + ":" + row.Event
		glyph = settingsGlyphOpen
		color = settingsColorType
		search += " read-only global system " + row.ConfigPath
	}
	return intpickercompat.Entry{
		Label:     settingsLabel(glyph, color, row.Event, desc),
		Value:     value,
		SearchKey: search,
	}
}

func settingsHookReadonlyMissingEntry(row settingsHookRow) intpickercompat.Entry {
	desc := "missing - " + row.ConfigPath + " [hooks." + row.Event + "] read-only"
	return intpickercompat.Entry{
		Label:     settingsLabelDim(row.Event, desc),
		Value:     settingsNoopValue,
		SearchKey: row.Event + " missing read-only global system " + row.ConfigPath,
	}
}

// settingsHookLegacyEntry surfaces a multi-line or symlinked script left
// behind after migration. The runner does not execute it; the row is
// informational and non-interactive. Single-line regular files are migrated
// automatically and never produce this row.
func settingsHookLegacyEntry(row settingsHookRow) intpickercompat.Entry {
	var (
		title string
		desc  string
	)
	if row.Legacy.Symlink {
		title = row.Event + " (legacy script: symlink)"
		desc = fmt.Sprintf("legacy script - %s (symlink, not executed; clean up via the source dotfiles repo)", row.Legacy.Path)
	} else {
		title = row.Event + " (legacy script)"
		desc = fmt.Sprintf("legacy script - %s (%d lines, not executed)", row.Legacy.Path, row.Legacy.Lines)
	}
	return intpickercompat.Entry{
		Label: settingsLabelDim(title, desc),
		Value: settingsNoopValue,
	}
}

// projectLegacyScriptMap collects legacy scripts that remain in the historical
// `.projmux/<event>` / `.projmux/hooks/<event>` locations after migration.
// Single-line regular files are migrated to declarative entries and renamed
// to `<path>.bak`; they never show up here. Symlinks are always surfaced
// because the migrator refuses to rename them (the source-of-truth lives
// outside the projmux config dir, typically a dotfiles repo).
func projectLegacyScriptMap(projectPath string) map[string]settingsLegacyScript {
	result := map[string]settingsLegacyScript{}
	for _, event := range settingsHookEvents {
		for _, candidate := range []string{
			filepath.Join(projectPath, ".projmux", event.Name),
			filepath.Join(projectPath, ".projmux", config.HooksDirName, event.Name),
		} {
			if entry, ok := inspectLegacyScript(candidate); ok {
				result[event.Name] = entry
				break
			}
		}
	}
	return result
}

func globalLegacyScriptMap(homeDir func() (string, error), lookupEnv func(string) string) map[string]settingsLegacyScript {
	result := map[string]settingsLegacyScript{}
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		return result
	}
	for _, event := range settingsHookEvents {
		candidate := paths.HookPath(event.Name)
		if entry, ok := inspectLegacyScript(candidate); ok {
			result[event.Name] = entry
		}
	}
	return result
}

// inspectLegacyScript returns a populated settingsLegacyScript when the
// candidate path is a legacy artifact the UI should surface. Symlinks always
// qualify (regardless of target line count) so dotfiles-managed hooks get a
// dedicated cleanup nudge. Regular files only qualify when they have two or
// more non-trivial lines — single-line scripts are migrated automatically.
func inspectLegacyScript(path string) (settingsLegacyScript, bool) {
	linfo, err := os.Lstat(path)
	if err != nil {
		return settingsLegacyScript{}, false
	}
	if linfo.Mode()&os.ModeSymlink != 0 {
		return settingsLegacyScript{Path: path, Symlink: true}, true
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return settingsLegacyScript{}, false
	}
	lines := countLegacyScriptLines(path)
	if lines <= 1 {
		return settingsLegacyScript{}, false
	}
	return settingsLegacyScript{Path: path, Lines: lines}, true
}

// countLegacyScriptLines counts non-empty, non-comment lines so the UI can
// echo the same metric the migrator uses to decide whether a script qualifies
// for automatic conversion.
func countLegacyScriptLines(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	count := 0
	for raw := range strings.SplitSeq(string(data), "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		count++
	}
	return count
}

func (c *settingsCommand) globalConfigPath() (string, error) {
	return hooks.GlobalConfigPath(c.lookupEnv, c.homeDir)
}

func settingsProjectConfigState(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "missing - " + path
		}
		return "unreadable - " + path
	}
	cfg, err := hooks.ParseProjectConfig(string(content))
	if err != nil {
		return "present - " + path + " (parse error)"
	}
	summary := settingsProjectConfigSummary(cfg)
	if summary == "" {
		return "present - " + path
	}
	return fmt.Sprintf("present - %s (%s)", path, summary)
}

func settingsProjectConfigSummary(cfg hooks.ProjectConfig) string {
	parts := make([]string, 0, 4)
	if len(cfg.Hooks) > 0 {
		parts = append(parts, fmt.Sprintf("%d hook commands", len(cfg.Hooks)))
	}
	if strings.TrimSpace(cfg.StartupRun) != "" {
		parts = append(parts, "startup")
	}
	if len(cfg.Env) > 0 {
		parts = append(parts, fmt.Sprintf("%d env vars", len(cfg.Env)))
	}
	if cfg.Kube.Context != "" || cfg.Kube.Namespace != "" {
		parts = append(parts, "kube")
	}
	return strings.Join(parts, ", ")
}

func loadProjectConfigForRead(path string) (hooks.ProjectConfig, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return hooks.ProjectConfig{}, nil
		}
		return hooks.ProjectConfig{}, err
	}
	return hooks.ParseProjectConfig(string(content))
}

func declaredHookRun(cfg hooks.ProjectConfig, event string) string {
	if cfg.Hooks == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Hooks[hooks.Event(event)])
}

// runHookMakerAction dispatches the hook-add / hook-edit / hook-remove value
// produced by the hook maker page. action is one of the prefixed action keys
// declared above.
func (c *settingsCommand) runHookMakerAction(ctx settingsProjectContext, action string, stdout, stderr io.Writer) error {
	switch {
	case strings.HasPrefix(action, settingsActionPrefixHookAdd):
		body := strings.TrimPrefix(action, settingsActionPrefixHookAdd)
		scope, event, ok := parseHookActionBody(body)
		if !ok {
			return fmt.Errorf("invalid hook add action: %s", body)
		}
		return c.hookMakerEditDeclarative(ctx, scope, event, stdout, stderr)
	case strings.HasPrefix(action, settingsActionPrefixHookEdit):
		body := strings.TrimPrefix(action, settingsActionPrefixHookEdit)
		scope, event, ok := parseHookActionBody(body)
		if !ok {
			return fmt.Errorf("invalid hook edit action: %s", body)
		}
		return c.hookMakerEditDeclarative(ctx, scope, event, stdout, stderr)
	case strings.HasPrefix(action, settingsActionPrefixHookRemove):
		body := strings.TrimPrefix(action, settingsActionPrefixHookRemove)
		scope, event, ok := parseHookActionBody(body)
		if !ok {
			return fmt.Errorf("invalid hook remove action: %s", body)
		}
		return c.hookMakerRemoveDeclarative(ctx, scope, event, stdout)
	case strings.HasPrefix(action, settingsActionPrefixHookView):
		body := strings.TrimPrefix(action, settingsActionPrefixHookView)
		scope, event, ok := parseHookActionBody(body)
		if !ok {
			return fmt.Errorf("invalid hook view action: %s", body)
		}
		return c.runReadonlyHookView(ctx, scope, event, stdout, stderr)
	default:
		return fmt.Errorf("unknown hook maker action: %s", action)
	}
}

func (c *settingsCommand) runHookMakerActionWithFeedback(ctx settingsProjectContext, action string, stdout, stderr io.Writer) error {
	if strings.HasPrefix(action, settingsActionPrefixHookView) {
		return c.runHookMakerAction(ctx, action, stdout, stderr)
	}
	return c.runObservedSettingsMutation("Hook", stdout, stderr, func(out, errOut io.Writer) error {
		return c.runHookMakerAction(ctx, action, out, errOut)
	})
}

// parseHookActionBody parses "<scope>:<event>" payloads. Phase 2.6 collapsed
// the scope/event/source triple to scope/event since there is only one source
// (declarative).
func parseHookActionBody(body string) (scope, event string, ok bool) {
	parts := strings.SplitN(body, ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	scope = parts[0]
	event = parts[1]
	if scope != hookScopeGlobal && scope != hookScopeProject {
		return "", "", false
	}
	if !isSettingsSupportedHookEvent(event) {
		return "", "", false
	}
	return scope, event, true
}

func isSettingsSupportedHookEvent(event string) bool {
	for _, e := range settingsHookEvents {
		if e.Name == event {
			return true
		}
	}
	return false
}

// --- declarative branch ----------------------------------------------------

func (c *settingsCommand) hookMakerEditDeclarative(ctx settingsProjectContext, scope, event string, stdout, stderr io.Writer) error {
	switch scope {
	case hookScopeProject:
		return c.hookMakerEditProjectDeclarative(ctx, event, stdout, stderr)
	case hookScopeGlobal:
		return c.hookMakerEditGlobalDeclarative(event, stdout, stderr)
	default:
		return fmt.Errorf("unknown hook scope: %s", scope)
	}
}

func (c *settingsCommand) hookMakerRemoveDeclarative(ctx settingsProjectContext, scope, event string, stdout io.Writer) error {
	switch scope {
	case hookScopeProject:
		if !ctx.hasProject() {
			return nil
		}
		return c.saveProjectConfig(ctx, stdout, func(cfg *hooks.ProjectConfig) error {
			delete(cfg.Hooks, hooks.Event(event))
			return nil
		})
	case hookScopeGlobal:
		path, err := c.globalConfigPath()
		if err != nil {
			return err
		}
		if _, err := hooks.UpdateGlobalConfig(path, func(cfg *hooks.ProjectConfig) error {
			delete(cfg.Hooks, hooks.Event(event))
			return nil
		}); err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "wrote %s\n", path)
		return err
	default:
		return fmt.Errorf("unknown hook scope: %s", scope)
	}
}

func (c *settingsCommand) hookMakerEditProjectDeclarative(ctx settingsProjectContext, event string, stdout, stderr io.Writer) error {
	if !ctx.hasProject() {
		return errors.New("declarative hook requires a project context")
	}
	cfg, err := c.loadProjectConfigForEdit(ctx)
	if err != nil {
		return err
	}
	current := ""
	if cfg.Hooks != nil {
		current = cfg.Hooks[hooks.Event(event)]
	}
	value, ok, err := c.runProjectConfigTyped("Hook "+event+" - run",
		"Type [hooks."+event+"] run > ", current)
	if err != nil || !ok {
		return err
	}
	value = strings.TrimSpace(value)
	return c.saveProjectConfig(ctx, stdout, func(cfg *hooks.ProjectConfig) error {
		if cfg.Hooks == nil {
			cfg.Hooks = map[hooks.Event]string{}
		}
		if value == "" {
			delete(cfg.Hooks, hooks.Event(event))
		} else {
			cfg.Hooks[hooks.Event(event)] = value
		}
		return nil
	})
}

// hookMakerEditGlobalDeclarative reads and writes the global
// `${XDG_CONFIG_HOME}/projmux/config.toml` entry for event. Global declarative
// hooks bypass the trust prompt — they are considered authoritative for the
// user that runs projmux.
func (c *settingsCommand) hookMakerEditGlobalDeclarative(event string, stdout, stderr io.Writer) error {
	path, err := c.globalConfigPath()
	if err != nil {
		return err
	}
	cfg, err := hooks.LoadGlobalConfig(path)
	if err != nil {
		return err
	}
	current := ""
	if cfg.Hooks != nil {
		current = cfg.Hooks[hooks.Event(event)]
	}
	value, ok, err := c.runProjectConfigTyped("Global Hook "+event+" - run",
		"Type [hooks."+event+"] run > ", current)
	if err != nil || !ok {
		return err
	}
	value = strings.TrimSpace(value)
	if _, err := hooks.UpdateGlobalConfig(path, func(cfg *hooks.ProjectConfig) error {
		if cfg.Hooks == nil {
			cfg.Hooks = map[hooks.Event]string{}
		}
		if value == "" {
			delete(cfg.Hooks, hooks.Event(event))
		} else {
			cfg.Hooks[hooks.Event(event)] = value
		}
		return nil
	}); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "wrote %s\n", path)
	return err
}

func (c *settingsCommand) runReadonlyHookView(ctx settingsProjectContext, scope, event string, stdout, stderr io.Writer) error {
	if scope == hookScopeProject {
		return c.hookMakerEditProjectDeclarative(ctx, event, stdout, stderr)
	}
	path, err := c.globalConfigPath()
	if err != nil {
		return err
	}
	cfg, err := hooks.LoadGlobalConfig(path)
	if err != nil {
		return err
	}
	run := ""
	if cfg.Hooks != nil {
		run = strings.TrimSpace(cfg.Hooks[hooks.Event(event)])
	}
	projectPath := "(open Settings > Project > Project recipe from a project)"
	if projectCtx := c.resolveSettingsProjectContext(); projectCtx.hasProject() {
		projectPath = settingsProjectConfigPath(projectCtx)
	}
	_, err = c.runPicker(intpickercompat.Options{
		UI:     "settings-hook-readonly",
		Title:  "Project recipe - [hooks." + event + "]",
		Prompt: "Read-only hook > ",
		Footer: projmuxFooter("Use the Back row or picker close action to close"),
		Entries: []intpickercompat.Entry{
			settingsBackEntry(),
			{Label: settingsLabelInfo("Defined in", path, "source: "+scope), Value: settingsNoopValue},
			{Label: settingsLabelInfo("run", nonEmpty(run, "(unset)"), "[hooks."+event+"]"), Value: settingsNoopValue},
			{Label: settingsLabelDim("Read-only", "managed outside this project; edit the source file directly"), Value: settingsNoopValue},
			{Label: settingsLabelInfo("Project override", projectPath, "Settings > Project > Project recipe"), Value: settingsNoopValue},
		},
		ExpectKeys: []string{"enter"},
		Bindings:   settingsCloseBindings(),
	})
	if errors.Is(err, errSettingsClosed) {
		return nil
	}
	return err
}

// --- project config section ------------------------------------------------

func (c *settingsCommand) runProjectConfigSection(stdout, stderr io.Writer) error {
	for {
		ctx := c.resolveSettingsProjectContext()
		options := c.projectConfigOptions(ctx)
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
		case action == settingsActionPrefixProjectConfig+"startup":
			if err := c.runProjectConfigStartupSection(ctx, stdout, stderr); err != nil {
				return err
			}
		case action == settingsActionPrefixProjectConfig+"kube":
			if err := c.runProjectConfigKubeSection(ctx, stdout, stderr); err != nil {
				return err
			}
		case action == settingsActionPrefixProjectConfig+"env":
			if err := c.runProjectConfigEnvSection(ctx, stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixProjectConfig):
			if err := c.executeProjectConfigActionWithFeedback(ctx, action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown project config action: %s", action)
		}
	}
}

func (c *settingsCommand) runProjectConfigStartupSection(ctx settingsProjectContext, stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-project-config-startup",
			Entries:    projectConfigStartupEntries(c.currentProjectConfig(ctx)),
			Title:      "Project recipe - Startup command",
			Prompt:     "Settings > Project > Project recipe > Startup command > ",
			Footer:     projmuxFooter("Enter: edit/apply  |  Back row: parent "),
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
		case strings.HasPrefix(action, settingsActionPrefixProjectConfig+"startup:"):
			if err := c.executeProjectConfigActionWithFeedback(ctx, action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown startup project config action: %s", action)
		}
	}
}

func (c *settingsCommand) runProjectConfigKubeSection(ctx settingsProjectContext, stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-project-config-kube",
			Entries:    projectConfigKubeEntries(c.currentProjectConfig(ctx)),
			Title:      "Project recipe - Kube",
			Prompt:     "Settings > Project > Project recipe > Kube > ",
			Footer:     projmuxFooter("Enter: edit/apply  |  Back row: parent "),
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
		case strings.HasPrefix(action, settingsActionPrefixProjectConfig+"kube:"):
			if err := c.executeProjectConfigActionWithFeedback(ctx, action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown kube project config action: %s", action)
		}
	}
}

func (c *settingsCommand) runProjectConfigEnvSection(ctx settingsProjectContext, stdout, stderr io.Writer) error {
	for {
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-project-config-env",
			Entries:    projectConfigEnvEntries(c.currentProjectConfig(ctx)),
			Title:      "Project recipe - Environment",
			Prompt:     "Settings > Project > Project recipe > Environment > ",
			Footer:     projmuxFooter("Enter: edit/apply  |  Back row: parent "),
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
		case action == settingsActionPrefixProjectConfig+"env:add",
			strings.HasPrefix(action, settingsActionPrefixProjectConfig+"env:"):
			if err := c.executeProjectConfigActionWithFeedback(ctx, action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown environment project config action: %s", action)
		}
	}
}

func (c *settingsCommand) executeProjectConfigAction(ctx settingsProjectContext, action string, stdout, stderr io.Writer) error {
	switch {
	case action == settingsActionPrefixProjectConfig+"startup:set":
		return c.runProjectConfigStringField(ctx, "Startup command", "Type startup command > ", c.currentProjectConfig(ctx).StartupRun, stdout, stderr, func(cfg *hooks.ProjectConfig, value string) {
			cfg.StartupRun = strings.TrimSpace(value)
		})
	case action == settingsActionPrefixProjectConfig+"startup:clear":
		return c.saveProjectConfig(ctx, stdout, func(cfg *hooks.ProjectConfig) error {
			cfg.StartupRun = ""
			return nil
		})
	case action == settingsActionPrefixProjectConfig+"kube:context:set":
		return c.runProjectConfigStringField(ctx, "Kube context", "Type kube context > ", c.currentProjectConfig(ctx).Kube.Context, stdout, stderr, func(cfg *hooks.ProjectConfig, value string) {
			cfg.Kube.Context = strings.TrimSpace(value)
		})
	case action == settingsActionPrefixProjectConfig+"kube:context:clear":
		return c.saveProjectConfig(ctx, stdout, func(cfg *hooks.ProjectConfig) error {
			cfg.Kube.Context = ""
			return nil
		})
	case action == settingsActionPrefixProjectConfig+"kube:namespace:set":
		return c.runProjectConfigStringField(ctx, "Kube namespace", "Type kube namespace > ", c.currentProjectConfig(ctx).Kube.Namespace, stdout, stderr, func(cfg *hooks.ProjectConfig, value string) {
			cfg.Kube.Namespace = strings.TrimSpace(value)
		})
	case action == settingsActionPrefixProjectConfig+"kube:namespace:clear":
		return c.saveProjectConfig(ctx, stdout, func(cfg *hooks.ProjectConfig) error {
			cfg.Kube.Namespace = ""
			return nil
		})
	case action == settingsActionPrefixProjectConfig+"env:add":
		return c.runProjectConfigAddEnv(ctx, stdout, stderr)
	case strings.HasPrefix(action, settingsActionPrefixProjectConfig+"env:"):
		return c.runProjectConfigEnvAction(ctx, action, stdout, stderr)
	default:
		return fmt.Errorf("unknown project config action: %s", action)
	}
}

func (c *settingsCommand) executeProjectConfigActionWithFeedback(ctx settingsProjectContext, action string, stdout, stderr io.Writer) error {
	return c.runObservedSettingsMutation("Project recipe", stdout, stderr, func(out, errOut io.Writer) error {
		return c.executeProjectConfigAction(ctx, action, out, errOut)
	})
}

func (c *settingsCommand) projectConfigOptions(ctx settingsProjectContext) intpickercompat.Options {
	return intpickercompat.Options{
		UI:         "settings-project-config",
		Entries:    c.projectConfigEntries(ctx),
		Title:      "Project recipe - env, kube, startup",
		Prompt:     "Settings > Project > Project recipe > ",
		Footer:     projmuxFooter("Enter: edit/apply  |  Back row: parent "),
		ExpectKeys: []string{"enter"},
		Bindings:   settingsCloseBindings(),
	}
}

func (c *settingsCommand) projectConfigEntries(ctx settingsProjectContext) []intpickercompat.Entry {
	entries := []intpickercompat.Entry{settingsBackEntry()}
	if !ctx.hasProject() {
		return append(entries, intpickercompat.Entry{
			Label:     settingsLabelDim("Project recipe", "disabled - no project context"),
			Value:     settingsNoopValue,
			SearchKey: "Project recipe config.toml",
		})
	}
	path := settingsProjectConfigPath(ctx)
	// Phase 2.7: drop the redundant "Project context" info row — the
	// frame title chip strip already announces the active scope.
	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabelInfo("Path", path, settingsProjectConfigState(path)),
		Value: settingsNoopValue,
	})
	cfg, err := c.loadProjectConfigForEdit(ctx)
	if err != nil {
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelDim("Parse error", err.Error()),
			Value: settingsNoopValue,
		})
	}
	entries = append(entries, projectConfigRootEntries(cfg)...)
	// Hooks are now authored from the Hooks page; the Project recipe section is
	// data-only for env / kube / startup. Hook commands are still parsed and
	// preserved on save (UpdateProjectConfig round-trips them).
	return entries
}

func projectConfigRootEntries(cfg hooks.ProjectConfig) []intpickercompat.Entry {
	startup := strings.TrimSpace(cfg.StartupRun)
	if startup == "" {
		startup = "(unset)"
	}
	kube := []string{}
	if strings.TrimSpace(cfg.Kube.Context) != "" {
		kube = append(kube, "context="+cfg.Kube.Context)
	}
	if strings.TrimSpace(cfg.Kube.Namespace) != "" {
		kube = append(kube, "namespace="+cfg.Kube.Namespace)
	}
	kubeSummary := "(unset)"
	if len(kube) > 0 {
		kubeSummary = strings.Join(kube, ", ")
	}
	envSummary := "(none)"
	if len(cfg.Env) == 1 {
		envSummary = "1 var"
	} else if len(cfg.Env) > 1 {
		envSummary = fmt.Sprintf("%d vars", len(cfg.Env))
	}
	return []intpickercompat.Entry{
		{
			Label:     settingsLabel(settingsGlyphOpen, settingsColorType, "Startup command", startup),
			Value:     settingsActionPrefixProjectConfig + "startup",
			SearchKey: "startup command run set clear",
		},
		{
			Label:     settingsLabel(settingsGlyphOpen, settingsColorType, "Kube", kubeSummary),
			Value:     settingsActionPrefixProjectConfig + "kube",
			SearchKey: "kube context namespace set clear",
		},
		{
			Label:     settingsLabel(settingsGlyphOpen, settingsColorType, "Environment", envSummary),
			Value:     settingsActionPrefixProjectConfig + "env",
			SearchKey: "environment env variables add set remove",
		},
	}
}

func projectConfigStartupEntries(cfg hooks.ProjectConfig) []intpickercompat.Entry {
	current := cfg.StartupRun
	if strings.TrimSpace(current) == "" {
		current = "(unset)"
	}
	return []intpickercompat.Entry{
		{
			Label: settingsLabelInfo("Startup command", current, "[startup] run"),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabel(settingsGlyphType, settingsColorType, "Set startup command...", "write [startup] run"),
			Value: settingsActionPrefixProjectConfig + "startup:set",
		},
		{
			Label: settingsLabel(settingsGlyphRemove, settingsColorRemove, "Clear startup command", "remove [startup] run"),
			Value: settingsActionPrefixProjectConfig + "startup:clear",
		},
	}
}

func projectConfigKubeEntries(cfg hooks.ProjectConfig) []intpickercompat.Entry {
	context := cfg.Kube.Context
	if context == "" {
		context = "(unset)"
	}
	namespace := cfg.Kube.Namespace
	if namespace == "" {
		namespace = "(unset)"
	}
	return []intpickercompat.Entry{
		{
			Label: settingsLabelInfo("Kube context", context, "[kube] context"),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabel(settingsGlyphType, settingsColorType, "Set kube context...", "write [kube] context"),
			Value: settingsActionPrefixProjectConfig + "kube:context:set",
		},
		{
			Label: settingsLabel(settingsGlyphRemove, settingsColorRemove, "Clear kube context", "remove [kube] context"),
			Value: settingsActionPrefixProjectConfig + "kube:context:clear",
		},
		{
			Label: settingsLabelInfo("Kube namespace", namespace, "[kube] namespace"),
			Value: settingsNoopValue,
		},
		{
			Label: settingsLabel(settingsGlyphType, settingsColorType, "Set kube namespace...", "write [kube] namespace"),
			Value: settingsActionPrefixProjectConfig + "kube:namespace:set",
		},
		{
			Label: settingsLabel(settingsGlyphRemove, settingsColorRemove, "Clear kube namespace", "remove [kube] namespace"),
			Value: settingsActionPrefixProjectConfig + "kube:namespace:clear",
		},
	}
}

func projectConfigEnvEntries(cfg hooks.ProjectConfig) []intpickercompat.Entry {
	entries := []intpickercompat.Entry{
		{
			Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, "Add env var...", "write [env] KEY"),
			Value: settingsActionPrefixProjectConfig + "env:add",
		},
	}
	keys := make([]string, 0, len(cfg.Env))
	for key := range cfg.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("Env vars", "(none)", "[env]"),
			Value: settingsNoopValue,
		})
	}
	for _, key := range keys {
		entries = append(entries,
			intpickercompat.Entry{
				Label: settingsLabelInfo("Env "+key, cfg.Env[key], "[env]"),
				Value: settingsNoopValue,
			},
			intpickercompat.Entry{
				Label: settingsLabel(settingsGlyphType, settingsColorType, "Set "+key+"...", "edit [env] "+key),
				Value: settingsActionPrefixProjectConfig + "env:" + key + ":set",
			},
			intpickercompat.Entry{
				Label: settingsLabel(settingsGlyphRemove, settingsColorRemove, "Remove "+key, "delete [env] "+key),
				Value: settingsActionPrefixProjectConfig + "env:" + key + ":remove",
			},
		)
	}
	return entries
}

func (c *settingsCommand) runProjectConfigStringField(ctx settingsProjectContext, title, prompt, initial string, stdout, stderr io.Writer, apply func(*hooks.ProjectConfig, string)) error {
	value, ok, err := c.runProjectConfigTyped(title, prompt, initial)
	if err != nil || !ok {
		return err
	}
	return c.saveProjectConfig(ctx, stdout, func(cfg *hooks.ProjectConfig) error {
		apply(cfg, value)
		return nil
	})
}

func (c *settingsCommand) runProjectConfigAddEnv(ctx settingsProjectContext, stdout, stderr io.Writer) error {
	key, ok, err := c.runProjectConfigTyped("Env key", "Type env key > ", "")
	if err != nil || !ok {
		return err
	}
	key = strings.TrimSpace(key)
	if err := hooks.ValidateProjectEnvKey(key); err != nil {
		fmt.Fprintf(stderr, "invalid env key: %v\n", err)
		return nil
	}
	value, ok, err := c.runProjectConfigTyped("Env value - "+key, "Type env value > ", "")
	if err != nil || !ok {
		return err
	}
	return c.saveProjectConfig(ctx, stdout, func(cfg *hooks.ProjectConfig) error {
		if cfg.Env == nil {
			cfg.Env = map[string]string{}
		}
		cfg.Env[key] = value
		return nil
	})
}

func (c *settingsCommand) runProjectConfigEnvAction(ctx settingsProjectContext, action string, stdout, stderr io.Writer) error {
	body := strings.TrimPrefix(action, settingsActionPrefixProjectConfig+"env:")
	key, op, ok := strings.Cut(body, ":")
	if !ok {
		return fmt.Errorf("unknown env project config action: %s", action)
	}
	if err := hooks.ValidateProjectEnvKey(key); err != nil {
		return err
	}
	switch op {
	case "set":
		current := ""
		cfg, err := c.loadProjectConfigForEdit(ctx)
		if err != nil {
			return err
		}
		if cfg.Env != nil {
			current = cfg.Env[key]
		}
		value, ok, err := c.runProjectConfigTyped("Env value - "+key, "Type env value > ", current)
		if err != nil || !ok {
			return err
		}
		return c.saveProjectConfig(ctx, stdout, func(cfg *hooks.ProjectConfig) error {
			if cfg.Env == nil {
				cfg.Env = map[string]string{}
			}
			cfg.Env[key] = value
			return nil
		})
	case "remove":
		return c.saveProjectConfig(ctx, stdout, func(cfg *hooks.ProjectConfig) error {
			delete(cfg.Env, key)
			return nil
		})
	default:
		return fmt.Errorf("unknown env project config operation: %s", op)
	}
}

func (c *settingsCommand) runProjectConfigTyped(title, prompt, initial string) (string, bool, error) {
	result, err := c.runPicker(intpickercompat.Options{
		UI:           "settings-project-config-typed",
		Entries:      nil,
		AcceptQuery:  true,
		InitialQuery: initial,
		Title:        title,
		Prompt:       prompt,
		Footer:       projmuxFooter("Enter: save "),
		ExpectKeys:   []string{"enter"},
		Bindings:     settingsCloseBindings(),
	})
	if err != nil {
		return "", false, err
	}
	if result.Key != "enter" {
		return "", false, nil
	}
	return result.Query, true, nil
}

func (c *settingsCommand) currentProjectConfig(ctx settingsProjectContext) hooks.ProjectConfig {
	cfg, err := c.loadProjectConfigForEdit(ctx)
	if err != nil {
		return hooks.ProjectConfig{}
	}
	return cfg
}

func (c *settingsCommand) loadProjectConfigForEdit(ctx settingsProjectContext) (hooks.ProjectConfig, error) {
	path := settingsProjectConfigPath(ctx)
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return hooks.ProjectConfig{}, nil
		}
		return hooks.ProjectConfig{}, err
	}
	return hooks.ParseProjectConfig(string(content))
}

func (c *settingsCommand) saveProjectConfig(ctx settingsProjectContext, stdout io.Writer, update func(*hooks.ProjectConfig) error) error {
	if !ctx.hasProject() {
		return errors.New("project config requires a project context")
	}
	path := settingsProjectConfigPath(ctx)
	if _, err := hooks.UpdateProjectConfig(path, update); err != nil {
		return err
	}
	trustPath, err := c.projectConfigTrustStorePath()
	if err != nil {
		return err
	}
	if _, err := hooks.TrustProjectConfig(ctx.Path, trustPath); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "wrote %s\n", path); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "trusted %s\n", path)
	return err
}

func (c *settingsCommand) projectConfigTrustStorePath() (string, error) {
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return "", err
	}
	return filepath.Join(paths.StateDir, "trusted-projects.json"), nil
}

func settingsProjectConfigPath(ctx settingsProjectContext) string {
	if !ctx.hasProject() {
		return ""
	}
	return filepath.Join(ctx.Path, ".projmux", "config.toml")
}
