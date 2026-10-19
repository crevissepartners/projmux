package tmuxconfig
import (
"os"
"strings"
"github.com/crevissepartners/projmux/internal/config"
"github.com/crevissepartners/projmux/internal/theme"
)

func StatusbarDecorationSetFromGlobal(mode config.StatusbarDecoration) StatusbarDecorationSet {
	mode = config.NormalizeStatusbarDecoration(string(mode))
	return StatusbarDecorationSet{Cwd: mode, Git: mode, Notify: mode}
}

func LoadStatusbarDecoration(homeDir func() (string, error), lookupEnv func(string) string, resolvePaths PathsResolver) config.StatusbarDecoration {
	if homeDir == nil {
		return config.StatusbarDecorationOff
	}
	paths, err := resolvePaths(homeDir, lookupEnv)
	if err != nil {
		return config.StatusbarDecorationOff
	}
	mode, err := config.LoadStatusbarDecorationFile(paths.StatusbarDecorationFile())
	if err != nil {
		return config.StatusbarDecorationOff
	}
	return mode
}

func LoadStatusbarDecorationSet(homeDir func() (string, error), lookupEnv func(string) string, resolvePaths PathsResolver) StatusbarDecorationSet {
	global := LoadStatusbarDecoration(homeDir, lookupEnv, resolvePaths)
	return StatusbarDecorationSet{
		Cwd:    LoadStatusbarDecorationForTarget(homeDir, lookupEnv, StatusbarDecorationTargetCwd, global, resolvePaths),
		Git:    LoadStatusbarDecorationForTarget(homeDir, lookupEnv, StatusbarDecorationTargetGit, global, resolvePaths),
		Notify: LoadStatusbarDecorationForTarget(homeDir, lookupEnv, StatusbarDecorationTargetNotify, global, resolvePaths),
	}
}

func LoadStatusbarDecorationForTarget(homeDir func() (string, error), lookupEnv func(string) string, target StatusbarDecorationTarget, fallback config.StatusbarDecoration, resolvePaths PathsResolver) config.StatusbarDecoration {
	if homeDir == nil {
		return fallback
	}
	paths, err := resolvePaths(homeDir, lookupEnv)
	if err != nil {
		return fallback
	}
	path := StatusbarDecorationTargetFile(paths, target)
	if strings.TrimSpace(path) == "" {
		return fallback
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return fallback
		}
		return fallback
	}
	mode, err := config.LoadStatusbarDecorationFile(path)
	if err != nil {
		return fallback
	}
	return mode
}

func StatusbarDecorationTargetFile(paths config.Paths, target StatusbarDecorationTarget) string {
	switch target {
	case StatusbarDecorationTargetCwd:
		return paths.StatusbarDecorationCwdFile()
	case StatusbarDecorationTargetGit:
		return paths.StatusbarDecorationGitFile()
	case StatusbarDecorationTargetNotify:
		return paths.StatusbarDecorationNotifyFile()
	default:
		return ""
	}
}

func StatusbarDecorationTmuxOptionForTarget(target StatusbarDecorationTarget) string {
	switch target {
	case StatusbarDecorationTargetCwd:
		return StatusbarDecorationCwdTmuxOption
	case StatusbarDecorationTargetGit:
		return StatusbarDecorationGitTmuxOption
	case StatusbarDecorationTargetNotify:
		return StatusbarDecorationNotifyTmuxOption
	default:
		return StatusbarDecorationTmuxOption
	}
}

func StatusbarCwdSegmentFormat(roles theme.RenderRoles) string {
	return "#[range=user|pwd]" + StatusbarCwdDecoratorFormat(roles) + "#[fg=" + roles.StatusTextSecondary + "]#{=-28/...:pane_current_path}#[norange]"
}

func StatusbarCwdDecoratorFormat(roles theme.RenderRoles) string {
	return "#{?#{==:#{" + StatusbarDecorationCwdTmuxOption + "},},#{?#{==:#{" + StatusbarDecorationTmuxOption + "},symbol},#[fg=" + roles.DecorationCwd + "] ,#{?#{==:#{" + StatusbarDecorationTmuxOption + "},emoji},#[fg=" + roles.DecorationCwd + "]📁 ,}},#{?#{==:#{" + StatusbarDecorationCwdTmuxOption + "},symbol},#[fg=" + roles.DecorationCwd + "] ,#{?#{==:#{" + StatusbarDecorationCwdTmuxOption + "},emoji},#[fg=" + roles.DecorationCwd + "]📁 ,}}}"
}
const (
	StatusbarDecorationTmuxOption       = "@projmux_statusbar_decoration"
	StatusbarDecorationCwdTmuxOption    = "@projmux_statusbar_decoration_cwd"
	StatusbarDecorationGitTmuxOption    = "@projmux_statusbar_decoration_git"
	StatusbarDecorationNotifyTmuxOption = "@projmux_statusbar_decoration_notify"
)

type StatusbarDecorationTarget string

const (
	StatusbarDecorationTargetCwd    StatusbarDecorationTarget = "cwd"
	StatusbarDecorationTargetGit    StatusbarDecorationTarget = "git"
	StatusbarDecorationTargetNotify StatusbarDecorationTarget = "notify"
)

type StatusbarDecorationSet struct {
	Cwd    config.StatusbarDecoration
	Git    config.StatusbarDecoration
	Notify config.StatusbarDecoration
}

