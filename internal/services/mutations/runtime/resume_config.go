package runtime

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
)

const (
	// ResumeScanDepthDefault is the cwd-tree depth the AI resume picker uses
	// when nothing is configured. Zero preserves the historical exact-cwd
	// behaviour (only sessions started in the current directory are listed).
	ResumeScanDepthDefault = 0
	// ResumeScanDepthEnv overrides the configured/default depth. It takes
	// precedence over both project and global config, matching the source
	// priority convention used by the other AI settings.
	ResumeScanDepthEnv = "PROJMUX_AI_RESUME_SCAN_DEPTH"
)

type ResumeScanDepthSource string

const (
	ResumeScanDepthSourceEnv     ResumeScanDepthSource = "env"
	ResumeScanDepthSourceProject ResumeScanDepthSource = "project"
	ResumeScanDepthSourceGlobal  ResumeScanDepthSource = "global"
	ResumeScanDepthSourceDefault ResumeScanDepthSource = "default"
)

type ResumeScanDepthResolution struct {
	Depth  int
	Source ResumeScanDepthSource
}

// NormalizeResumeScanDepth maps a raw depth to the value discovery applies:
// negatives collapse to the exact-cwd default, oversized depths clamp to the
// supported maximum, and in-range values pass through unchanged.
func NormalizeResumeScanDepth(depth int) int {
	if depth < 0 {
		return ResumeScanDepthDefault
	}
	if depth > hooks.AIResumeScanDepthMax {
		return hooks.AIResumeScanDepthMax
	}
	return depth
}

// ResolveResumeScanDepth resolves the effective resume scan depth using the
// standard source priority: env > project config > global config > built-in
// default. cwd is the directory used to discover a project-local
// .projmux/config.toml; an empty cwd skips the project tier.
func ResolveResumeScanDepth(homeDir func() (string, error), lookupEnv func(string) string, cwd string) ResumeScanDepthResolution {
	if lookupEnv != nil {
		if raw := strings.TrimSpace(lookupEnv(ResumeScanDepthEnv)); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
				return ResumeScanDepthResolution{
					Depth:  NormalizeResumeScanDepth(n),
					Source: ResumeScanDepthSourceEnv,
				}
			}
		}
	}
	if cwd = strings.TrimSpace(cwd); cwd != "" {
		path := filepath.Join(cwd, ".projmux", "config.toml")
		config.NoteFrontRead(config.SettingConfigAIResume, path)
		if cfg, err := hooks.LoadProjectConfigFile(path); err == nil && cfg.AI.ResumeScanDepth > 0 {
			return ResumeScanDepthResolution{
				Depth:  NormalizeResumeScanDepth(cfg.AI.ResumeScanDepth),
				Source: ResumeScanDepthSourceProject,
			}
		}
	}
	if homeDir != nil {
		if path, err := hooks.GlobalConfigPath(lookupEnv, homeDir); err == nil {
			config.NoteFrontRead(config.SettingConfigAIResume, path)
			if cfg, err := hooks.LoadGlobalConfig(path); err == nil && cfg.AI.ResumeScanDepth > 0 {
				return ResumeScanDepthResolution{
					Depth:  NormalizeResumeScanDepth(cfg.AI.ResumeScanDepth),
					Source: ResumeScanDepthSourceGlobal,
				}
			}
		}
	}
	return ResumeScanDepthResolution{
		Depth:  ResumeScanDepthDefault,
		Source: ResumeScanDepthSourceDefault,
	}
}

const (
	// ResumePickerLimitEnv overrides the configured/default limit. It takes
	// precedence over both project and global config, matching the source
	// priority convention used by the other AI settings.
	ResumePickerLimitEnv = "PROJMUX_AI_RESUME_PICKER_LIMIT"
)

type ResumePickerLimitSource string

const (
	ResumePickerLimitSourceEnv     ResumePickerLimitSource = "env"
	ResumePickerLimitSourceProject ResumePickerLimitSource = "project"
	ResumePickerLimitSourceGlobal  ResumePickerLimitSource = "global"
	ResumePickerLimitSourceDefault ResumePickerLimitSource = "default"
)

type ResumePickerLimitResolution struct {
	Limit  int
	Source ResumePickerLimitSource
}

// ResolveResumePickerLimit resolves the effective resume-picker limit using
// the standard source priority: env > project config > global config >
// built-in default. cwd is the directory used to discover a project-local
// .projmux/config.toml; an empty cwd skips the project tier.
func ResolveResumePickerLimit(homeDir func() (string, error), lookupEnv func(string) string, cwd string) ResumePickerLimitResolution {
	if lookupEnv != nil {
		if raw := strings.TrimSpace(lookupEnv(ResumePickerLimitEnv)); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 {
				return ResumePickerLimitResolution{
					Limit:  NormalizeResumePickerLimit(n),
					Source: ResumePickerLimitSourceEnv,
				}
			}
		}
	}
	if cwd = strings.TrimSpace(cwd); cwd != "" {
		path := filepath.Join(cwd, ".projmux", "config.toml")
		config.NoteFrontRead(config.SettingConfigAIResume, path)
		if cfg, err := hooks.LoadProjectConfigFile(path); err == nil && cfg.AI.ResumePickerLimit > 0 {
			return ResumePickerLimitResolution{
				Limit:  NormalizeResumePickerLimit(cfg.AI.ResumePickerLimit),
				Source: ResumePickerLimitSourceProject,
			}
		}
	}
	if homeDir != nil {
		if path, err := hooks.GlobalConfigPath(lookupEnv, homeDir); err == nil {
			config.NoteFrontRead(config.SettingConfigAIResume, path)
			if cfg, err := hooks.LoadGlobalConfig(path); err == nil && cfg.AI.ResumePickerLimit > 0 {
				return ResumePickerLimitResolution{
					Limit:  NormalizeResumePickerLimit(cfg.AI.ResumePickerLimit),
					Source: ResumePickerLimitSourceGlobal,
				}
			}
		}
	}
	return ResumePickerLimitResolution{
		Limit:  ResumePickerLimitDefault,
		Source: ResumePickerLimitSourceDefault,
	}
}
