package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	{Name: string(hooks.EventPaneStartup)},
	{Name: string(hooks.EventPostAttach)},
}

// Hook maker action prefixes. Each action follows the shape
//
//	hook-<op>:<source>:<scope>:<event>
//
// where source is "script" or "declarative", scope is "global" or "project",
// and op is one of add, edit, remove. add does not have a leading source — the
// branch picker that follows the add selection chooses script vs declarative.
const (
	settingsActionPrefixHookAdd    = "hook-add:"
	settingsActionPrefixHookEdit   = "hook-edit:"
	settingsActionPrefixHookRemove = "hook-remove:"

	hookSourceScript      = "script"
	hookSourceDeclarative = "declarative"

	hookScopeGlobal  = "global"
	hookScopeProject = "project"
)

type settingsHookPathState struct {
	status string
	path   string
	note   string
}

// settingsHookRow describes the two hook sources (script file and declarative
// config.toml entry) for a single lifecycle event. Empty fields are valid —
// only populated sources are rendered as live rows.
type settingsHookRow struct {
	Event      string
	Scope      string
	Script     settingsHookPathState
	HasScript  bool
	Declared   string
	ConfigPath string
}

func (c *settingsCommand) globalHookEntries() []intpickercompat.Entry {
	entries := []intpickercompat.Entry{settingsBackEntry()}

	paths, err := pickerBackendConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelDim("Hooks", err.Error()),
			Value: settingsNoopValue,
		})
	}

	for _, event := range settingsHookEvents {
		row := settingsHookRow{
			Event:     event.Name,
			Scope:     hookScopeGlobal,
			Script:    settingsHookPathStateFor(paths.HookPath(event.Name)),
			HasScript: true,
		}
		entries = append(entries, renderHookRowEntries(row)...)
	}
	return entries
}

func (c *settingsCommand) projectHookEntries(ctx settingsProjectContext) []intpickercompat.Entry {
	entries := []intpickercompat.Entry{settingsBackEntry()}
	if !ctx.hasProject() {
		return append(entries,
			intpickercompat.Entry{
				Label: settingsLabelDim("Project context", "no project - open Settings from a project pane or set PROJMUX_CWD"),
				Value: settingsNoopValue,
			},
			intpickercompat.Entry{
				Label: settingsLabelDim("Hooks (project)", "disabled - no project context"),
				Value: settingsNoopValue,
			},
		)
	}

	entries = append(entries, intpickercompat.Entry{
		Label: settingsLabelInfo("Project context", ctx.Path, ctx.Source),
		Value: settingsNoopValue,
	})

	configPath := filepath.Join(ctx.Path, ".projmux", "config.toml")
	cfg, _ := loadProjectConfigForRead(configPath)

	for _, event := range settingsHookEvents {
		row := settingsHookRow{
			Event:      event.Name,
			Scope:      hookScopeProject,
			Script:     settingsProjectHookPathState(ctx.Path, event.Name),
			HasScript:  true,
			Declared:   declaredHookRun(cfg, event.Name),
			ConfigPath: configPath,
		}
		entries = append(entries, renderHookRowEntries(row)...)
	}
	return append(entries, settingsProjectConfigEntry(ctx.Path))
}

func (c *settingsCommand) runProjectHooksSection(stdout, stderr io.Writer) error {
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
		case action == settingsSectionProjectConfig:
			if err := c.runProjectConfigSection(stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixHookAdd),
			strings.HasPrefix(action, settingsActionPrefixHookEdit),
			strings.HasPrefix(action, settingsActionPrefixHookRemove):
			if err := c.runHookMakerAction(ctx, action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown project hook settings action: %s", action)
		}
	}
}

func (c *settingsCommand) runGlobalHooksSection(stdout, stderr io.Writer) error {
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
			strings.HasPrefix(action, settingsActionPrefixHookRemove):
			if err := c.runHookMakerAction(settingsProjectContext{}, action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown hook settings action: %s", action)
		}
	}
}

func settingsProjectHookPathState(projectPath, event string) settingsHookPathState {
	candidates := []string{
		filepath.Join(projectPath, ".projmux", event),
		filepath.Join(projectPath, ".projmux", config.HooksDirName, event),
	}
	var inactive settingsHookPathState
	for _, path := range candidates {
		state := settingsHookPathStateFor(path)
		if state.status == "active" {
			return state
		}
		if state.status == "inactive" && inactive.path == "" {
			inactive = state
		}
	}
	if inactive.path != "" {
		return inactive
	}
	return settingsHookPathState{
		status: "missing",
		// Display both candidate paths so the user can see where projmux will
		// look. The canonical creation path is .projmux/hooks/<event>; the
		// legacy .projmux/<event> location is still resolved by the runner.
		path: strings.Join(candidates, " or "),
	}
}

func settingsHookPathStateFor(path string) settingsHookPathState {
	state := settingsHookPathState{status: "missing", path: path}
	info, err := osStat(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			state.status = "unreadable"
			state.note = err.Error()
		}
		return state
	}
	if info.IsDir() {
		state.status = "inactive"
		state.note = "directory"
		return state
	}
	if info.Mode().Perm()&0o100 == 0 {
		state.status = "inactive"
		state.note = "not executable"
		return state
	}
	state.status = "active"
	return state
}

// renderHookRowEntries emits picker rows for a single lifecycle event,
// covering script + declarative sources. Active sources always get their own
// edit row. When at least one source is already active, the missing
// complement gets a source-specific [+ Add] row (no branch picker — the user
// already chose). When BOTH sources are missing, a single [+ Add] row is
// emitted that opens a declarative-vs-script branch picker.
func renderHookRowEntries(row settingsHookRow) []intpickercompat.Entry {
	entries := make([]intpickercompat.Entry, 0, 4)

	scriptActive := row.HasScript && (row.Script.status == "active" || row.Script.status == "inactive")
	declarativeActive := row.Scope == hookScopeProject && strings.TrimSpace(row.Declared) != ""

	// Global scope: only script is supported; collapse to a single row.
	if row.Scope != hookScopeProject {
		if scriptActive {
			entries = append(entries, settingsHookScriptEntry(row))
		} else {
			entries = append(entries, settingsHookAddRow(row))
		}
		return entries
	}

	// Project scope.
	switch {
	case scriptActive && declarativeActive:
		entries = append(entries, settingsHookScriptEntry(row), settingsHookDeclarativeEntry(row))
	case scriptActive && !declarativeActive:
		// Script is set; offer to add the missing declarative complement.
		entries = append(entries,
			settingsHookScriptEntry(row),
			settingsHookAddDeclarativeRow(row),
		)
	case !scriptActive && declarativeActive:
		// Declarative is set; offer to add a script complement.
		entries = append(entries,
			settingsHookDeclarativeEntry(row),
			settingsHookAddScriptRow(row),
		)
	default:
		// Both missing — one combined [+ Add] row that triggers branch picker.
		entries = append(entries, settingsHookAddRow(row))
	}
	return entries
}

func settingsHookAddRow(row settingsHookRow) intpickercompat.Entry {
	desc := "missing - " + row.Script.path + " [+ Add] declarative or script"
	return intpickercompat.Entry{
		Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, row.Event, desc),
		Value: settingsActionPrefixHookAdd + row.Scope + ":" + row.Event,
	}
}

func settingsHookAddScriptRow(row settingsHookRow) intpickercompat.Entry {
	desc := "[+ Add] script - " + row.Script.path
	return intpickercompat.Entry{
		Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, row.Event, desc),
		Value: settingsActionPrefixHookAdd + row.Scope + ":" + row.Event + ":" + hookSourceScript,
	}
}

func settingsHookAddDeclarativeRow(row settingsHookRow) intpickercompat.Entry {
	desc := "[+ Add] declarative - " + row.ConfigPath + " [hooks." + row.Event + "]"
	return intpickercompat.Entry{
		Label: settingsLabel(settingsGlyphAdd, settingsColorAdd, row.Event, desc),
		Value: settingsActionPrefixHookAdd + row.Scope + ":" + row.Event + ":" + hookSourceDeclarative,
	}
}

func settingsHookScriptEntry(row settingsHookRow) intpickercompat.Entry {
	glyph := settingsGlyphInactive
	color := settingsColorDim
	if row.Script.status == "active" {
		glyph = settingsGlyphToggle
		color = settingsColorAdd
	}
	desc := row.Script.status + " - " + row.Script.path + " (script)"
	if row.Script.note != "" {
		desc += " (" + row.Script.note + ")"
	}
	return intpickercompat.Entry{
		Label: settingsLabel(glyph, color, row.Event, desc),
		Value: settingsActionPrefixHookEdit + hookSourceScript + ":" + row.Scope + ":" + row.Event,
	}
}

func settingsHookDeclarativeEntry(row settingsHookRow) intpickercompat.Entry {
	desc := "active - run = " + row.Declared + " (declarative)"
	return intpickercompat.Entry{
		Label: settingsLabel(settingsGlyphToggle, settingsColorAdd, row.Event, desc),
		Value: settingsActionPrefixHookEdit + hookSourceDeclarative + ":" + row.Scope + ":" + row.Event,
	}
}

// settingsHookEntry is preserved for tests that still rely on the simpler
// single-source rendering. The hook maker page itself uses
// renderHookRowEntries which emits the same label shape per source.
func settingsHookEntry(event string, state settingsHookPathState) intpickercompat.Entry {
	row := settingsHookRow{Event: event, Scope: hookScopeGlobal, Script: state, HasScript: true}
	return settingsHookScriptEntry(row)
}

func settingsProjectConfigEntry(projectPath string) intpickercompat.Entry {
	path := filepath.Join(projectPath, ".projmux", "config.toml")
	state := settingsProjectConfigState(path)
	return intpickercompat.Entry{
		Label: settingsLabel(settingsGlyphOpen, settingsColorType, "config.toml", state),
		Value: settingsSectionProjectConfig,
	}
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
		return c.runHookMakerAdd(ctx, body, stdout, stderr)
	case strings.HasPrefix(action, settingsActionPrefixHookEdit):
		body := strings.TrimPrefix(action, settingsActionPrefixHookEdit)
		return c.runHookMakerEdit(ctx, body, stdout, stderr)
	case strings.HasPrefix(action, settingsActionPrefixHookRemove):
		body := strings.TrimPrefix(action, settingsActionPrefixHookRemove)
		return c.runHookMakerRemove(ctx, body, stdout, stderr)
	default:
		return fmt.Errorf("unknown hook maker action: %s", action)
	}
}

func parseHookActionBody(body string) (scope, event, source string, ok bool) {
	parts := strings.SplitN(body, ":", 3)
	if len(parts) < 2 {
		return "", "", "", false
	}
	scope = parts[0]
	event = parts[1]
	if len(parts) == 3 {
		source = parts[2]
	}
	if scope != hookScopeGlobal && scope != hookScopeProject {
		return "", "", "", false
	}
	if hooks.Event(event) == "" || !isSettingsSupportedHookEvent(event) {
		return "", "", "", false
	}
	return scope, event, source, true
}

func isSettingsSupportedHookEvent(event string) bool {
	for _, e := range settingsHookEvents {
		if e.Name == event {
			return true
		}
	}
	return false
}

func parseEditActionBody(body string) (source, scope, event string, ok bool) {
	parts := strings.SplitN(body, ":", 3)
	if len(parts) != 3 {
		return "", "", "", false
	}
	source = parts[0]
	scope = parts[1]
	event = parts[2]
	if source != hookSourceScript && source != hookSourceDeclarative {
		return "", "", "", false
	}
	if scope != hookScopeGlobal && scope != hookScopeProject {
		return "", "", "", false
	}
	if !isSettingsSupportedHookEvent(event) {
		return "", "", "", false
	}
	return source, scope, event, true
}

func (c *settingsCommand) runHookMakerAdd(ctx settingsProjectContext, body string, stdout, stderr io.Writer) error {
	scope, event, source, ok := parseHookActionBody(body)
	if !ok {
		return fmt.Errorf("invalid hook add action: %s", body)
	}
	if source == "" {
		// Branch picker: declarative vs script. Adds a small help row that
		// explains the trade-off.
		entries := []intpickercompat.Entry{
			settingsBackEntry(),
			{
				Label: settingsLabelDim("How to choose", "one-line command -> declarative; complex script -> $EDITOR"),
				Value: settingsNoopValue,
			},
			{
				Label: settingsLabel(settingsGlyphType, settingsColorType, "Declarative (one-liner)", "write run = \"...\" to .projmux/config.toml"),
				Value: settingsActionPrefixHookAdd + scope + ":" + event + ":" + hookSourceDeclarative,
			},
			{
				Label: settingsLabel(settingsGlyphOpen, settingsColorType, "Script ($EDITOR)", "create .projmux/hooks/<event> and open $EDITOR"),
				Value: settingsActionPrefixHookAdd + scope + ":" + event + ":" + hookSourceScript,
			},
		}
		// Project scope only supports declarative. Surface that constraint.
		if scope == hookScopeGlobal {
			entries[2] = intpickercompat.Entry{
				Label: settingsLabelDim("Declarative", "config.toml is project-only"),
				Value: settingsNoopValue,
			}
		}
		result, err := c.runPicker(intpickercompat.Options{
			UI:         "settings-hook-maker-add",
			Entries:    entries,
			Title:      "Hook " + event + " - add",
			Prompt:     "Settings > Hooks > " + event + " > Add > ",
			Footer:     projmuxFooter("Enter: choose  |  Back: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
			ExpectKeys: []string{"enter"},
			Bindings:   settingsCloseBindings(),
		})
		if err != nil {
			return err
		}
		next := strings.TrimSpace(result.Value)
		if result.Key != "enter" || next == "" || next == settingsNoopValue {
			return nil
		}
		if next == settingsBackValue {
			return nil
		}
		// Recurse with the resolved source.
		return c.runHookMakerAction(ctx, next, stdout, stderr)
	}

	switch source {
	case hookSourceScript:
		return c.hookMakerAddScript(ctx, scope, event, stdout, stderr)
	case hookSourceDeclarative:
		if scope == hookScopeGlobal {
			fmt.Fprintln(stderr, "declarative hooks are only supported for project scope")
			return nil
		}
		return c.hookMakerEditDeclarative(ctx, event, stdout, stderr)
	default:
		return fmt.Errorf("unknown hook source: %s", source)
	}
}

func (c *settingsCommand) runHookMakerEdit(ctx settingsProjectContext, body string, stdout, stderr io.Writer) error {
	source, scope, event, ok := parseEditActionBody(body)
	if !ok {
		return fmt.Errorf("invalid hook edit action: %s", body)
	}
	switch source {
	case hookSourceScript:
		return c.hookMakerEditScript(ctx, scope, event, stdout, stderr)
	case hookSourceDeclarative:
		if scope == hookScopeGlobal {
			fmt.Fprintln(stderr, "declarative hooks are only supported for project scope")
			return nil
		}
		return c.hookMakerEditDeclarative(ctx, event, stdout, stderr)
	default:
		return fmt.Errorf("unknown hook source: %s", source)
	}
}

func (c *settingsCommand) runHookMakerRemove(ctx settingsProjectContext, body string, stdout, stderr io.Writer) error {
	source, scope, event, ok := parseEditActionBody(body)
	if !ok {
		return fmt.Errorf("invalid hook remove action: %s", body)
	}
	switch source {
	case hookSourceScript:
		return c.hookMakerRemoveScript(ctx, scope, event, stdout)
	case hookSourceDeclarative:
		if scope == hookScopeGlobal {
			return nil
		}
		return c.hookMakerRemoveDeclarative(ctx, event, stdout)
	default:
		return fmt.Errorf("unknown hook source: %s", source)
	}
}

// --- script branch ---------------------------------------------------------

func (c *settingsCommand) hookMakerScriptPath(ctx settingsProjectContext, scope, event string) (string, string, error) {
	switch scope {
	case hookScopeGlobal:
		paths, err := pickerBackendConfigPaths(c.homeDir, c.lookupEnv)
		if err != nil {
			return "", "", err
		}
		return paths.HookPath(event), "", nil
	case hookScopeProject:
		if !ctx.hasProject() {
			return "", "", errors.New("project hook requires a project context")
		}
		state := settingsProjectHookPathState(ctx.Path, event)
		path := state.path
		// state.path may be a " or "-joined list when both candidates are
		// missing; pick the canonical hooks/ location for new files.
		if state.status == "missing" {
			path = filepath.Join(ctx.Path, ".projmux", config.HooksDirName, event)
		}
		rel, err := filepath.Rel(ctx.Path, path)
		if err != nil {
			return "", "", err
		}
		return path, filepath.ToSlash(rel), nil
	default:
		return "", "", fmt.Errorf("unknown hook scope: %s", scope)
	}
}

func (c *settingsCommand) hookMakerAddScript(ctx settingsProjectContext, scope, event string, stdout, stderr io.Writer) error {
	path, rel, err := c.hookMakerScriptPath(ctx, scope, event)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		// Already exists; treat add as edit.
		return c.hookMakerOpenScript(ctx, scope, event, path, rel, stdout, stderr)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body := "#!/bin/sh\n# projmux " + event + " hook for " + scope + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "created %s\n", path); err != nil {
		return err
	}
	return c.hookMakerOpenScript(ctx, scope, event, path, rel, stdout, stderr)
}

func (c *settingsCommand) hookMakerEditScript(ctx settingsProjectContext, scope, event string, stdout, stderr io.Writer) error {
	path, rel, err := c.hookMakerScriptPath(ctx, scope, event)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(stderr, "hook script %s does not exist; creating now\n", path)
			return c.hookMakerAddScript(ctx, scope, event, stdout, stderr)
		}
		return err
	}
	return c.hookMakerOpenScript(ctx, scope, event, path, rel, stdout, stderr)
}

func (c *settingsCommand) hookMakerOpenScript(ctx settingsProjectContext, scope, event, path, rel string, stdout, stderr io.Writer) error {
	editor := c.resolveEditor()
	if err := c.openEditor(editor, path); err != nil {
		fmt.Fprintf(stderr, "editor %s exited with error: %v\n", editor, err)
		// Continue — the file may still be edited and trusted.
	}
	if err := ensureExecutableScript(path); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "edited %s\n", path); err != nil {
		return err
	}
	if scope == hookScopeProject && ctx.hasProject() {
		return c.recordProjectScriptTrust(ctx, rel, stdout, stderr)
	}
	return nil
}

func (c *settingsCommand) hookMakerRemoveScript(ctx settingsProjectContext, scope, event string, stdout io.Writer) error {
	path, _, err := c.hookMakerScriptPath(ctx, scope, event)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	_, err = fmt.Fprintf(stdout, "removed %s\n", path)
	return err
}

func ensureExecutableScript(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("hook %q is a directory", path)
	}
	mode := info.Mode().Perm()
	if mode&0o111 != 0 {
		return nil
	}
	return os.Chmod(path, mode|0o755)
}

func (c *settingsCommand) recordProjectScriptTrust(ctx settingsProjectContext, rel string, stdout, stderr io.Writer) error {
	if rel == "" {
		return nil
	}
	trustPath, err := c.projectConfigTrustStorePath()
	if err != nil {
		return err
	}
	if _, err := hooks.TrustProjectFile(ctx.Path, rel, trustPath); err != nil {
		fmt.Fprintf(stderr, "trust %s: %v\n", rel, err)
		return nil
	}
	_, err = fmt.Fprintf(stdout, "trusted %s\n", filepath.Join(ctx.Path, rel))
	return err
}

// --- declarative branch ----------------------------------------------------

func (c *settingsCommand) hookMakerEditDeclarative(ctx settingsProjectContext, event string, stdout, stderr io.Writer) error {
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

func (c *settingsCommand) hookMakerRemoveDeclarative(ctx settingsProjectContext, event string, stdout io.Writer) error {
	if !ctx.hasProject() {
		return nil
	}
	return c.saveProjectConfig(ctx, stdout, func(cfg *hooks.ProjectConfig) error {
		delete(cfg.Hooks, hooks.Event(event))
		return nil
	})
}

// --- editor ---------------------------------------------------------------

func (c *settingsCommand) resolveEditor() string {
	env := c.lookupEnv
	if env == nil {
		env = os.Getenv
	}
	for _, key := range []string{"VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(env(key)); v != "" {
			return v
		}
	}
	return "vi"
}

func (c *settingsCommand) openEditor(editor, path string) error {
	editor = strings.TrimSpace(editor)
	if editor == "" {
		editor = "vi"
	}
	if c.runCommand != nil {
		// Split editor on whitespace so VISUAL="code -w" works.
		fields := strings.Fields(editor)
		name := fields[0]
		args := append(append([]string{}, fields[1:]...), path)
		return c.runCommand(name, args...)
	}
	fields := strings.Fields(editor)
	name := fields[0]
	args := append(append([]string{}, fields[1:]...), path)
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
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
		case action == settingsActionPrefixProjectConfig+"startup:set":
			if err := c.runProjectConfigStringField(ctx, "Startup command", "Type startup command > ", c.currentProjectConfig(ctx).StartupRun, stdout, stderr, func(cfg *hooks.ProjectConfig, value string) {
				cfg.StartupRun = strings.TrimSpace(value)
			}); err != nil {
				return err
			}
		case action == settingsActionPrefixProjectConfig+"startup:clear":
			if err := c.saveProjectConfig(ctx, stdout, func(cfg *hooks.ProjectConfig) error {
				cfg.StartupRun = ""
				return nil
			}); err != nil {
				return err
			}
		case action == settingsActionPrefixProjectConfig+"kube:context:set":
			if err := c.runProjectConfigStringField(ctx, "Kube context", "Type kube context > ", c.currentProjectConfig(ctx).Kube.Context, stdout, stderr, func(cfg *hooks.ProjectConfig, value string) {
				cfg.Kube.Context = strings.TrimSpace(value)
			}); err != nil {
				return err
			}
		case action == settingsActionPrefixProjectConfig+"kube:context:clear":
			if err := c.saveProjectConfig(ctx, stdout, func(cfg *hooks.ProjectConfig) error {
				cfg.Kube.Context = ""
				return nil
			}); err != nil {
				return err
			}
		case action == settingsActionPrefixProjectConfig+"kube:namespace:set":
			if err := c.runProjectConfigStringField(ctx, "Kube namespace", "Type kube namespace > ", c.currentProjectConfig(ctx).Kube.Namespace, stdout, stderr, func(cfg *hooks.ProjectConfig, value string) {
				cfg.Kube.Namespace = strings.TrimSpace(value)
			}); err != nil {
				return err
			}
		case action == settingsActionPrefixProjectConfig+"kube:namespace:clear":
			if err := c.saveProjectConfig(ctx, stdout, func(cfg *hooks.ProjectConfig) error {
				cfg.Kube.Namespace = ""
				return nil
			}); err != nil {
				return err
			}
		case action == settingsActionPrefixProjectConfig+"env:add":
			if err := c.runProjectConfigAddEnv(ctx, stdout, stderr); err != nil {
				return err
			}
		case strings.HasPrefix(action, settingsActionPrefixProjectConfig+"env:"):
			if err := c.runProjectConfigEnvAction(ctx, action, stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown project config action: %s", action)
		}
	}
}

func (c *settingsCommand) projectConfigOptions(ctx settingsProjectContext) intpickercompat.Options {
	return intpickercompat.Options{
		UI:         "settings-project-config",
		Entries:    c.projectConfigEntries(ctx),
		Title:      "Project Config - env, kube, startup",
		Prompt:     "Settings > Project > config.toml > ",
		Footer:     projmuxFooter("Enter: edit/apply  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
		ExpectKeys: []string{"enter"},
		Bindings:   settingsCloseBindings(),
	}
}

func (c *settingsCommand) projectConfigEntries(ctx settingsProjectContext) []intpickercompat.Entry {
	entries := []intpickercompat.Entry{settingsBackEntry()}
	if !ctx.hasProject() {
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelDim("config.toml", "disabled - no project context"),
			Value: settingsNoopValue,
		})
	}
	path := settingsProjectConfigPath(ctx)
	entries = append(entries,
		intpickercompat.Entry{
			Label: settingsLabelInfo("Project context", ctx.Path, ctx.Source),
			Value: settingsNoopValue,
		},
		intpickercompat.Entry{
			Label: settingsLabelInfo("Path", path, settingsProjectConfigState(path)),
			Value: settingsNoopValue,
		},
	)
	cfg, err := c.loadProjectConfigForEdit(ctx)
	if err != nil {
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelDim("Parse error", err.Error()),
			Value: settingsNoopValue,
		})
	}
	entries = append(entries, projectConfigStartupEntries(cfg)...)
	entries = append(entries, projectConfigKubeEntries(cfg)...)
	entries = append(entries, projectConfigEnvEntries(cfg)...)
	// Hooks are now authored from the Hooks page; the config.toml section is
	// data-only for env / kube / startup. Hook commands are still parsed and
	// preserved on save (UpdateProjectConfig round-trips them).
	return entries
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
		Footer:       projmuxFooter("Enter: save  |  Esc/Alt+5/Ctrl+Alt+S: close"),
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
	paths, err := pickerBackendConfigPaths(c.homeDir, c.lookupEnv)
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
