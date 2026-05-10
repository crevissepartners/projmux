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
	{Name: string(hooks.EventPaneStartup)},
	{Name: string(hooks.EventPostAttach)},
}

type settingsHookPathState struct {
	status string
	path   string
	note   string
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
		path := paths.HookPath(event.Name)
		entries = append(entries, settingsHookEntry(event.Name, settingsHookPathStateFor(path)))
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
	for _, event := range settingsHookEvents {
		entries = append(entries, settingsHookEntry(event.Name, settingsProjectHookPathState(ctx.Path, event.Name)))
	}
	return append(entries, settingsProjectConfigEntry(ctx.Path))
}

func (c *settingsCommand) runProjectHooksSection(stdout, stderr io.Writer) error {
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
		case settingsSectionProjectConfig:
			if err := c.runProjectConfigSection(stdout, stderr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown project hook settings action: %s", action)
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
		path:   strings.Join(candidates, " or "),
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

func settingsHookEntry(event string, state settingsHookPathState) intpickercompat.Entry {
	glyph := settingsGlyphInactive
	color := settingsColorDim
	if state.status == "active" {
		glyph = settingsGlyphToggle
		color = settingsColorAdd
	}
	desc := state.status + " - " + state.path
	if state.note != "" {
		desc += " (" + state.note + ")"
	}
	return intpickercompat.Entry{
		Label: settingsLabel(glyph, color, event, desc),
		Value: settingsNoopValue,
	}
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
	if len(cfg.Hooks) > 0 {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("Hook commands", fmt.Sprintf("%d preserved", len(cfg.Hooks)), "script editor out of scope"),
			Value: settingsNoopValue,
		})
	}
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
