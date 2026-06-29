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
	// aiResumeScanDepthDefault is the cwd-tree depth the AI resume picker uses
	// when nothing is configured. Zero preserves the historical exact-cwd
	// behaviour (only sessions started in the current directory are listed).
	aiResumeScanDepthDefault = 0
	// aiResumeScanDepthEnv overrides the configured/default depth. It takes
	// precedence over both project and global config, matching the source
	// priority convention used by the other AI settings.
	aiResumeScanDepthEnv = "PROJMUX_AI_RESUME_SCAN_DEPTH"
)

type aiResumeScanDepthSource string

const (
	aiResumeScanDepthSourceEnv     aiResumeScanDepthSource = "env"
	aiResumeScanDepthSourceProject aiResumeScanDepthSource = "project"
	aiResumeScanDepthSourceGlobal  aiResumeScanDepthSource = "global"
	aiResumeScanDepthSourceDefault aiResumeScanDepthSource = "default"
)

type aiResumeScanDepthResolution struct {
	Depth  int
	Source aiResumeScanDepthSource
}

// normalizeResumeScanDepth maps a raw depth to the value discovery applies:
// negatives collapse to the exact-cwd default, oversized depths clamp to the
// supported maximum, and in-range values pass through unchanged.
func normalizeResumeScanDepth(depth int) int {
	if depth < 0 {
		return aiResumeScanDepthDefault
	}
	if depth > hooks.AIResumeScanDepthMax {
		return hooks.AIResumeScanDepthMax
	}
	return depth
}

// resolveAIResumeScanDepth resolves the effective resume scan depth using the
// standard source priority: env > project config > global config > built-in
// default. cwd is the directory used to discover a project-local
// .projmux/config.toml; an empty cwd skips the project tier.
func resolveAIResumeScanDepth(homeDir func() (string, error), lookupEnv func(string) string, cwd string) aiResumeScanDepthResolution {
	if lookupEnv != nil {
		if raw := strings.TrimSpace(lookupEnv(aiResumeScanDepthEnv)); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
				return aiResumeScanDepthResolution{
					Depth:  normalizeResumeScanDepth(n),
					Source: aiResumeScanDepthSourceEnv,
				}
			}
		}
	}
	if cwd = strings.TrimSpace(cwd); cwd != "" {
		path := filepath.Join(cwd, ".projmux", "config.toml")
		if cfg, err := hooks.LoadProjectConfigFile(path); err == nil && cfg.AI.ResumeScanDepth > 0 {
			return aiResumeScanDepthResolution{
				Depth:  normalizeResumeScanDepth(cfg.AI.ResumeScanDepth),
				Source: aiResumeScanDepthSourceProject,
			}
		}
	}
	if homeDir != nil {
		if path, err := hooks.GlobalConfigPath(lookupEnv, homeDir); err == nil {
			if cfg, err := hooks.LoadGlobalConfig(path); err == nil && cfg.AI.ResumeScanDepth > 0 {
				return aiResumeScanDepthResolution{
					Depth:  normalizeResumeScanDepth(cfg.AI.ResumeScanDepth),
					Source: aiResumeScanDepthSourceGlobal,
				}
			}
		}
	}
	return aiResumeScanDepthResolution{
		Depth:  aiResumeScanDepthDefault,
		Source: aiResumeScanDepthSourceDefault,
	}
}

// currentAIResumeScanDepth resolves the depth for the Settings UI, using the
// active project context (when any) so the displayed source label is accurate.
func (c *settingsCommand) currentAIResumeScanDepth() aiResumeScanDepthResolution {
	cwd := ""
	if ctx := c.resolveSettingsProjectContext(); ctx.hasProject() {
		cwd = ctx.Path
	}
	return resolveAIResumeScanDepth(c.homeDir, c.lookupEnv, cwd)
}

// parseAIResumeScanDepth validates a raw depth string from a settings preset or
// custom input, enforcing the supported [min, max] range.
func parseAIResumeScanDepth(raw string) (int, error) {
	depth, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || depth < hooks.AIResumeScanDepthMin || depth > hooks.AIResumeScanDepthMax {
		return 0, fmt.Errorf("AI resume scan depth %q must be between %d and %d", raw, hooks.AIResumeScanDepthMin, hooks.AIResumeScanDepthMax)
	}
	return depth, nil
}

// setAIResumeScanDepth writes the depth to the global config [ai] section. The
// value must already be validated to the supported range. Writing zero clears
// the key (render omits a zero depth), restoring the default exact-cwd scope.
func (c *settingsCommand) setAIResumeScanDepth(depth int, stdout io.Writer) error {
	path, err := c.globalConfigPath()
	if err != nil {
		return err
	}
	if _, err := hooks.UpdateGlobalConfig(path, func(cfg *hooks.ProjectConfig) error {
		cfg.AI.ResumeScanDepth = depth
		return nil
	}); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "AI resume scan depth: %d\n", depth); err != nil {
		return err
	}
	if c.lookupEnv != nil && strings.TrimSpace(c.lookupEnv("TMUX")) != "" && c.runCommand != nil {
		_ = c.runCommand("tmux", "display-message", "AI resume scan depth: "+strconv.Itoa(depth))
	}
	return nil
}
