package app

import (
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// effective_merge.go implements the Settings popup "Effective merge view"
// page for the Project tab. It reads the global config.toml (if present)
// and the project-local .projmux/config.toml, calls hooks.MergeEffective,
// and renders one row per merged entry with a per-row source label of
// global / project / merged / default.

// runEffectiveMergeSection drives the Effective merge view picker page.
func (c *settingsCommand) runEffectiveMergeSection(stdout, stderr io.Writer) error {
	for {
		ctx := c.resolveSettingsProjectContext()
		options := c.effectiveMergeOptions(ctx)
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
		default:
			return fmt.Errorf("unknown effective merge action: %s", action)
		}
	}
}

func (c *settingsCommand) effectiveMergeOptions(ctx settingsProjectContext) intpickercompat.Options {
	return intpickercompat.Options{
		UI:         "settings-effective-merge",
		Entries:    c.effectiveMergeEntries(ctx),
		Title:      "Effective merge view - global + project config",
		Prompt:     "Settings > Project > Effective merge view > ",
		Footer:     projmuxFooter("Enter: back  |  Back row: parent  |  Esc/Alt+5/Ctrl+Alt+S: close"),
		ExpectKeys: []string{"enter"},
		Bindings:   settingsCloseBindings(),
	}
}

func (c *settingsCommand) effectiveMergeEntries(ctx settingsProjectContext) []intpickercompat.Entry {
	entries := []intpickercompat.Entry{settingsBackEntry()}
	if !ctx.hasProject() {
		return append(entries, intpickercompat.Entry{
			Label: settingsLabelDim("Effective merge view", "disabled - no project context"),
			Value: settingsNoopValue,
		})
	}

	globalCfg, globalPath, globalErr := c.loadGlobalConfigForMerge()
	projectCfg, projectPath, projectErr := c.loadProjectConfigForMerge(ctx)

	entries = append(entries,
		intpickercompat.Entry{
			Label: settingsLabelInfo("Project context", ctx.Path, ctx.Source),
			Value: settingsNoopValue,
		},
		intpickercompat.Entry{
			Label: settingsLabelInfo("Global config", globalPath, sourceFileState(globalCfg, globalErr, globalPath)),
			Value: settingsNoopValue,
		},
		intpickercompat.Entry{
			Label: settingsLabelInfo("Project config", projectPath, sourceFileState(projectCfg, projectErr, projectPath)),
			Value: settingsNoopValue,
		},
	)

	if globalErr != nil {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelDim("Global parse error", globalErr.Error()),
			Value: settingsNoopValue,
		})
	}
	if projectErr != nil {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelDim("Project parse error", projectErr.Error()),
			Value: settingsNoopValue,
		})
	}

	merged := hooks.MergeEffective(globalCfg, projectCfg)
	entries = append(entries, renderEffectiveSection(merged.Env)...)
	entries = append(entries, renderEffectiveSection(merged.Kube)...)
	entries = append(entries, renderEffectiveSection(merged.Startup)...)
	return entries
}

// renderEffectiveSection emits one section header row plus one row per
// entry. Sensitive env keys have their values redacted in display.
func renderEffectiveSection(section hooks.EffectiveSection) []intpickercompat.Entry {
	header := intpickercompat.Entry{
		Label: settingsLabelInfo("["+section.Name+"]", "", string(section.Source)),
		Value: settingsNoopValue,
	}
	entries := []intpickercompat.Entry{header}
	if len(section.Entries) == 0 {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelDim("  (no entries)", string(hooks.EffectiveSourceDefault)),
			Value: settingsNoopValue,
		})
		return entries
	}
	for _, entry := range section.Entries {
		entries = append(entries, intpickercompat.Entry{
			Label: settingsLabelInfo("  "+entry.Key, effectiveEntryDisplayValue(section.Name, entry), string(entry.Source)),
			Value: settingsNoopValue,
		})
	}
	return entries
}

// effectiveEntryDisplayValue applies redaction to env values and falls back
// to (unset) for empty scalars on the kube / startup sections.
func effectiveEntryDisplayValue(sectionName string, entry hooks.EffectiveEntry) string {
	if sectionName == "env" {
		return hooks.DisplayEnvValue(entry.Key, entry.Value)
	}
	if strings.TrimSpace(entry.Value) == "" {
		return "(unset)"
	}
	return entry.Value
}

// loadGlobalConfigForMerge resolves the global config.toml path and parses
// it. A missing file yields an empty ProjectConfig and nil error — that
// matches the policy used everywhere else: "no file" is not an error.
func (c *settingsCommand) loadGlobalConfigForMerge() (hooks.ProjectConfig, string, error) {
	path, err := c.globalConfigFilePath()
	if err != nil {
		return hooks.ProjectConfig{}, "", err
	}
	cfg, err := hooks.LoadProjectConfigFile(path)
	return cfg, path, err
}

// loadProjectConfigForMerge resolves the project config.toml path and
// parses it. A missing file yields an empty ProjectConfig and nil error.
func (c *settingsCommand) loadProjectConfigForMerge(ctx settingsProjectContext) (hooks.ProjectConfig, string, error) {
	path := settingsProjectConfigPath(ctx)
	cfg, err := hooks.LoadProjectConfigFile(path)
	return cfg, path, err
}

func (c *settingsCommand) globalConfigFilePath() (string, error) {
	paths, err := pickerBackendConfigPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return "", err
	}
	return paths.GlobalConfigFile(), nil
}

// sourceFileState returns a short annotation that explains whether the file
// at path was found, missing, or failed to parse. The settings popup
// renders this in the dim source slot of the info row.
func sourceFileState(cfg hooks.ProjectConfig, err error, path string) string {
	if err != nil {
		return "parse error"
	}
	if strings.TrimSpace(path) == "" {
		return "unresolved path"
	}
	if !configHasContent(cfg) {
		return "missing or empty"
	}
	return "loaded"
}

// configHasContent reports whether the parsed config carries any data —
// used to label the source slot in the file info rows.
func configHasContent(cfg hooks.ProjectConfig) bool {
	if len(cfg.Env) > 0 {
		return true
	}
	if cfg.Kube.Context != "" || cfg.Kube.Namespace != "" {
		return true
	}
	if strings.TrimSpace(cfg.StartupRun) != "" {
		return true
	}
	if len(cfg.Hooks) > 0 {
		return true
	}
	return false
}
