package app

import (
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/integrations/hooks"
)

const (
	// aiResumePickerLimitDefault is the number of recent sessions the AI resume
	// picker shows when nothing is configured. This preserves the historical
	// hardcoded behaviour (Phase 0 and earlier used a literal 30).
	aiResumePickerLimitDefault = 30
	// aiResumePickerLimitEnv overrides the configured/default limit. It takes
	// precedence over both project and global config, matching the source
	// priority convention used by the other AI settings.
	aiResumePickerLimitEnv = "PROJMUX_AI_RESUME_PICKER_LIMIT"
)

type aiResumePickerLimitSource string

const (
	aiResumePickerLimitSourceEnv     aiResumePickerLimitSource = "env"
	aiResumePickerLimitSourceProject aiResumePickerLimitSource = "project"
	aiResumePickerLimitSourceGlobal  aiResumePickerLimitSource = "global"
	aiResumePickerLimitSourceDefault aiResumePickerLimitSource = "default"
)

type aiResumePickerLimitResolution struct {
	Limit  int
	Source aiResumePickerLimitSource
}

// normalizeResumePickerLimit maps a raw limit to the value the picker actually
// applies: non-positive falls back to the default, oversized clamps to the
// supported maximum, and in-range values pass through unchanged.
func normalizeResumePickerLimit(limit int) int {
	if limit <= 0 {
		return aiResumePickerLimitDefault
	}
	if limit > hooks.AIResumePickerLimitMax {
		return hooks.AIResumePickerLimitMax
	}
	return limit
}

// resolveAIResumePickerLimit resolves the effective resume-picker limit using
// the standard source priority: env > project config > global config >
// built-in default. cwd is the directory used to discover a project-local
// .projmux/config.toml; an empty cwd skips the project tier.
func resolveAIResumePickerLimit(homeDir func() (string, error), lookupEnv func(string) string, cwd string) aiResumePickerLimitResolution {
	if lookupEnv != nil {
		if raw := strings.TrimSpace(lookupEnv(aiResumePickerLimitEnv)); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 {
				return aiResumePickerLimitResolution{
					Limit:  normalizeResumePickerLimit(n),
					Source: aiResumePickerLimitSourceEnv,
				}
			}
		}
	}
	if cwd = strings.TrimSpace(cwd); cwd != "" {
		path := filepath.Join(cwd, ".projmux", "config.toml")
		if cfg, err := hooks.LoadProjectConfigFile(path); err == nil && cfg.AI.ResumePickerLimit > 0 {
			return aiResumePickerLimitResolution{
				Limit:  normalizeResumePickerLimit(cfg.AI.ResumePickerLimit),
				Source: aiResumePickerLimitSourceProject,
			}
		}
	}
	if homeDir != nil {
		if path, err := hooks.GlobalConfigPath(lookupEnv, homeDir); err == nil {
			if cfg, err := hooks.LoadGlobalConfig(path); err == nil && cfg.AI.ResumePickerLimit > 0 {
				return aiResumePickerLimitResolution{
					Limit:  normalizeResumePickerLimit(cfg.AI.ResumePickerLimit),
					Source: aiResumePickerLimitSourceGlobal,
				}
			}
		}
	}
	return aiResumePickerLimitResolution{
		Limit:  aiResumePickerLimitDefault,
		Source: aiResumePickerLimitSourceDefault,
	}
}

// currentAIResumePickerLimit resolves the limit for the Settings UI, using the
// active project context (when any) so the displayed source label is accurate.
func (c *settingsCommand) currentAIResumePickerLimit() aiResumePickerLimitResolution {
	cwd := ""
	if ctx := c.resolveSettingsProjectContext(); ctx.hasProject() {
		cwd = ctx.Path
	}
	return resolveAIResumePickerLimit(c.homeDir, c.lookupEnv, cwd)
}

// parseAIResumePickerLimit validates a raw limit string from a settings preset
// or custom input, enforcing the supported [min, max] range.
func parseAIResumePickerLimit(raw string) (int, error) {
	limit, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || limit < hooks.AIResumePickerLimitMin || limit > hooks.AIResumePickerLimitMax {
		return 0, fmt.Errorf("AI resume picker limit %q must be between %d and %d", raw, hooks.AIResumePickerLimitMin, hooks.AIResumePickerLimitMax)
	}
	return limit, nil
}

// setAIResumePickerLimit writes the limit to the global config [ai] section.
// The value must already be validated to the supported range.
func (c *settingsCommand) setAIResumePickerLimit(limit int, stdout io.Writer) error {
	path, err := c.globalConfigPath()
	if err != nil {
		return err
	}
	if _, err := hooks.UpdateGlobalConfig(path, func(cfg *hooks.ProjectConfig) error {
		cfg.AI.ResumePickerLimit = limit
		return nil
	}); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "AI resume picker limit: %d\n", limit); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		_ = c.runCommand("tmux", "display-message", "AI resume picker limit: "+strconv.Itoa(limit))
	}
	return nil
}
