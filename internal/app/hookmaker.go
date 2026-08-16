package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// projectHookEntries renders the Project hooks container: the session
// lifecycle collection and the notification fan-out event. Both are Views, so
// entering a row never writes a hook; the command state and its mutation rows
// live one level down in the event detail.
func (c *settingsCommand) projectHookEntries(ctx settingsProjectContext) []intpickercompat.Entry {
	entries := []intpickercompat.Entry{settingsBackEntry()}
	if !ctx.hasProject() {
		// Phase 2.7: the frame title chip strip already announces the
		// active scope, so drop the redundant "Project context" row.
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelDim(settingsNavLabel(settingsNavProjectHooks), "disabled - no project context"),
			Value: settingsNoopValue,
		})
	}
	entries = append(entries, intpickercompat.Entry{
		Label:     settingsLabelInfo("Project config", filepath.Join(ctx.Path, ".projmux", "config.toml"), "[hooks.<event>]"),
		Value:     settingsNoopValue,
		SearchKey: "project config hooks source path",
	})
	entries = append(entries, intpickercompat.Entry{
		Label:     c.rowLabel(settingsGlyphOpen, settingsColorType, settingsNavLabel(settingsNavProjectHooks+".lifecycle"), c.projectHookLifecycleSummary()),
		Value:     settingsProjectAutomationLifecycle,
		SearchKey: "project hooks lifecycle pre-create post-create post-attach",
	})
	entries = append(entries, intpickercompat.Entry{
		Label:     c.rowLabel(settingsGlyphOpen, settingsColorType, settingsNavLabel(settingsNavProjectHooks+".send-noti"), c.automationEventSummary(hookScopeProject, string(hooks.EventSendNoti))),
		Value:     settingsProjectAutomationSendNoti,
		SearchKey: "project hooks send-noti notification queued fan-out",
	})
	return entries
}

func (c *settingsCommand) projectHookLifecycleSummary() string {
	active := 0
	for _, event := range settingsAutomationLifecycleEvents {
		command, _, _ := c.hookEventState(hookScopeProject, event)
		if strings.TrimSpace(command) != "" {
			active++
		}
	}
	return settingsLifecycleSummaryLocale(c.locale(), active, len(settingsAutomationLifecycleEvents))
}

func (c *settingsCommand) runProjectHooksSection(stdout, stderr io.Writer) error {
	// Best-effort migration on first entry so users on Phase 2.5 see their
	// existing single-line scripts as declarative entries immediately.
	if ctx := c.resolveSettingsProjectContext(); ctx.hasProject() {
		_, _ = hooks.MigrateProjectLegacyScripts(ctx.Path, "", stderr)
	}
	for {
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
		switch action {
		case settingsBackValue:
			return nil
		case settingsNoopValue:
			continue
		case settingsProjectAutomationLifecycle:
			if err := c.runProjectHooksLifecycleSection(stdout, stderr); err != nil {
				return err
			}
		case settingsProjectAutomationSendNoti:
			if err := c.runHookEventDetailSection(hookScopeProject, string(hooks.EventSendNoti), stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown project hook settings action: %s", action)
		}
	}
}

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
	projectPath := "(open Settings > Project > Automation > Project hooks from a project)"
	if projectCtx := c.resolveSettingsProjectContext(); projectCtx.hasProject() {
		projectPath = settingsProjectConfigPath(projectCtx)
	}
	_, err = c.runPicker(intpickercompat.Options{
		UI:     "settings-hook-readonly",
		Title:  "Project hooks - [hooks." + event + "]",
		Prompt: "Read-only hook > ",
		Footer: projmuxFooter("Use the Back row or picker close action to close"),
		Entries: []intpickercompat.Entry{
			settingsBackEntry(),
			{Label: settingsLabelInfo("Defined in", path, "source: "+scope), Value: settingsNoopValue},
			{Label: settingsLabelInfo("run", nonEmpty(run, "(unset)"), "[hooks."+event+"]"), Value: settingsNoopValue},
			{Label: settingsLabelDim("Read-only", "managed outside this project; edit the source file directly"), Value: settingsNoopValue},
			{Label: settingsLabelInfo("Project override", projectPath, "Settings > Project > Automation > Project hooks"), Value: settingsNoopValue},
		},
		ExpectKeys: []string{"enter"},
		Bindings:   settingsCloseBindings(),
	})
	if errors.Is(err, errSettingsClosed) {
		return nil
	}
	return err
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
