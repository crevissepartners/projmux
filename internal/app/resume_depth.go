package app

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

const aiResumeScanDepthSourceDefault = mutationruntime.ResumeScanDepthSourceDefault

type aiResumeScanDepthResolution = mutationruntime.ResumeScanDepthResolution

func resolveAIResumeScanDepth(homeDir func() (string, error), lookupEnv func(string) string, cwd string) aiResumeScanDepthResolution {
	return mutationruntime.ResolveResumeScanDepth(homeDir, lookupEnv, cwd)
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
