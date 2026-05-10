package app

import (
	"errors"
	"fmt"
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
		Label: settingsLabel(settingsGlyphInfo, settingsColorInfo, "config.toml", state),
		Value: settingsNoopValue,
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
