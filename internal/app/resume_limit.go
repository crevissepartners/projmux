package app

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

const (
	// aiResumePickerLimitDefault is the number of recent sessions the AI resume
	// picker shows when nothing is configured. This preserves the historical
	// hardcoded behaviour (Phase 0 and earlier used a literal 30).
	aiResumePickerLimitDefault = mutationruntime.ResumePickerLimitDefault
	aiResumePickerLimitEnv     = mutationruntime.ResumePickerLimitEnv
)

type aiResumePickerLimitSource = mutationruntime.ResumePickerLimitSource

const (
	aiResumePickerLimitSourceEnv     = mutationruntime.ResumePickerLimitSourceEnv
	aiResumePickerLimitSourceProject = mutationruntime.ResumePickerLimitSourceProject
	aiResumePickerLimitSourceGlobal  = mutationruntime.ResumePickerLimitSourceGlobal
	aiResumePickerLimitSourceDefault = mutationruntime.ResumePickerLimitSourceDefault
)

type aiResumePickerLimitResolution = mutationruntime.ResumePickerLimitResolution

// normalizeResumePickerLimit maps a raw limit to the value the picker actually
// applies: non-positive falls back to the default, oversized clamps to the
// supported maximum, and in-range values pass through unchanged.
func normalizeResumePickerLimit(limit int) int {
	return mutationruntime.NormalizeResumePickerLimit(limit)
}

func resolveAIResumePickerLimit(homeDir func() (string, error), lookupEnv func(string) string, cwd string) aiResumePickerLimitResolution {
	return mutationruntime.ResolveResumePickerLimit(homeDir, lookupEnv, cwd)
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
