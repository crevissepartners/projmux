package app

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
)

const (
	statusbarDecorationTmuxOption       = "@projmux_statusbar_decoration"
	statusbarDecorationCwdTmuxOption    = "@projmux_statusbar_decoration_cwd"
	statusbarDecorationGitTmuxOption    = "@projmux_statusbar_decoration_git"
	statusbarDecorationNotifyTmuxOption = "@projmux_statusbar_decoration_notify"
)

type statusbarDecorationTarget string

const (
	statusbarDecorationTargetCwd    statusbarDecorationTarget = "cwd"
	statusbarDecorationTargetGit    statusbarDecorationTarget = "git"
	statusbarDecorationTargetNotify statusbarDecorationTarget = "notify"
)

type statusbarDecorationSet struct {
	Cwd    config.StatusbarDecoration
	Git    config.StatusbarDecoration
	Notify config.StatusbarDecoration
}

func statusbarDecorationSetFromGlobal(mode config.StatusbarDecoration) statusbarDecorationSet {
	mode = config.NormalizeStatusbarDecoration(string(mode))
	return statusbarDecorationSet{Cwd: mode, Git: mode, Notify: mode}
}

func statusbarConfigPaths(homeDir func() (string, error), lookupEnv func(string) string) (config.Paths, error) {
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	home, err := homeDir()
	if err != nil {
		return config.Paths{}, fmt.Errorf("resolve home directory: %w", err)
	}
	return config.Homes{
		HomeDir:    home,
		ConfigHome: lookupEnv("XDG_CONFIG_HOME"),
		StateHome:  lookupEnv("XDG_STATE_HOME"),
	}.Paths()
}

func loadStatusbarDecoration(homeDir func() (string, error), lookupEnv func(string) string) config.StatusbarDecoration {
	if homeDir == nil {
		return config.StatusbarDecorationOff
	}
	paths, err := statusbarConfigPaths(homeDir, lookupEnv)
	if err != nil {
		return config.StatusbarDecorationOff
	}
	mode, err := config.LoadStatusbarDecorationFile(paths.StatusbarDecorationFile())
	if err != nil {
		return config.StatusbarDecorationOff
	}
	return mode
}

func loadStatusbarDecorationSet(homeDir func() (string, error), lookupEnv func(string) string) statusbarDecorationSet {
	global := loadStatusbarDecoration(homeDir, lookupEnv)
	return statusbarDecorationSet{
		Cwd:    loadStatusbarDecorationForTarget(homeDir, lookupEnv, statusbarDecorationTargetCwd, global),
		Git:    loadStatusbarDecorationForTarget(homeDir, lookupEnv, statusbarDecorationTargetGit, global),
		Notify: loadStatusbarDecorationForTarget(homeDir, lookupEnv, statusbarDecorationTargetNotify, global),
	}
}

func loadStatusbarDecorationForTarget(homeDir func() (string, error), lookupEnv func(string) string, target statusbarDecorationTarget, fallback config.StatusbarDecoration) config.StatusbarDecoration {
	if homeDir == nil {
		return fallback
	}
	paths, err := statusbarConfigPaths(homeDir, lookupEnv)
	if err != nil {
		return fallback
	}
	path := statusbarDecorationTargetFile(paths, target)
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

func statusbarDecorationTargetFile(paths config.Paths, target statusbarDecorationTarget) string {
	switch target {
	case statusbarDecorationTargetCwd:
		return filepath.Join(paths.ConfigDir, "statusbar-decoration-cwd")
	case statusbarDecorationTargetGit:
		return filepath.Join(paths.ConfigDir, "statusbar-decoration-git")
	case statusbarDecorationTargetNotify:
		return filepath.Join(paths.ConfigDir, "statusbar-decoration-notify")
	default:
		return ""
	}
}

func statusbarDecorationTmuxOptionForTarget(target statusbarDecorationTarget) string {
	switch target {
	case statusbarDecorationTargetCwd:
		return statusbarDecorationCwdTmuxOption
	case statusbarDecorationTargetGit:
		return statusbarDecorationGitTmuxOption
	case statusbarDecorationTargetNotify:
		return statusbarDecorationNotifyTmuxOption
	default:
		return statusbarDecorationTmuxOption
	}
}

func statusbarCwdSegmentFormat() string {
	return "#[range=user|pwd]" + statusbarCwdDecoratorFormat() + "#[fg=" + tmuxSecondaryFg + "]#{=-28/...:pane_current_path}#[norange]"
}

func statusbarCwdDecoratorFormat() string {
	return "#{?#{==:#{" + statusbarDecorationCwdTmuxOption + "},},#{?#{==:#{" + statusbarDecorationTmuxOption + "},symbol},#[fg=colour220] ,#{?#{==:#{" + statusbarDecorationTmuxOption + "},emoji},#[fg=colour220]📁 ,}},#{?#{==:#{" + statusbarDecorationCwdTmuxOption + "},symbol},#[fg=colour220] ,#{?#{==:#{" + statusbarDecorationCwdTmuxOption + "},emoji},#[fg=colour220]📁 ,}}}"
}

type gitRemoteProvider string

const (
	gitRemoteProviderGitHub gitRemoteProvider = "github"
	gitRemoteProviderGitLab gitRemoteProvider = "gitlab"
)

func statusbarGitDecorator(mode config.StatusbarDecoration, remoteURL string) string {
	provider := detectGitRemoteProvider(remoteURL)
	switch mode {
	case config.StatusbarDecorationSymbol:
		switch provider {
		case gitRemoteProviderGitLab:
			return "#[fg=colour208] #[fg=colour16]"
		default:
			return "#[fg=colour17] #[fg=colour16]"
		}
	case config.StatusbarDecorationEmoji:
		switch provider {
		case gitRemoteProviderGitHub:
			return "#[fg=colour17]🐱 #[fg=colour16]"
		case gitRemoteProviderGitLab:
			return "#[fg=colour208]🦊 #[fg=colour16]"
		default:
			return "#[fg=colour28]🌿 #[fg=colour16]"
		}
	default:
		return ""
	}
}

func detectGitRemoteProvider(remoteURL string) gitRemoteProvider {
	host := gitRemoteHost(remoteURL)
	switch {
	case host == "github.com", strings.HasSuffix(host, ".github.com"), strings.Contains(host, "github"):
		return gitRemoteProviderGitHub
	case host == "gitlab.com", strings.HasSuffix(host, ".gitlab.com"), strings.Contains(host, "gitlab"):
		return gitRemoteProviderGitLab
	default:
		return ""
	}
}

func gitRemoteHost(remoteURL string) string {
	raw := strings.ToLower(strings.TrimSpace(remoteURL))
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err == nil && parsed.Hostname() != "" {
			return strings.TrimPrefix(parsed.Hostname(), "www.")
		}
	}
	if at := strings.LastIndex(raw, "@"); at >= 0 {
		raw = raw[at+1:]
	}
	if end := strings.IndexAny(raw, ":/"); end >= 0 {
		raw = raw[:end]
	}
	return strings.TrimPrefix(raw, "www.")
}

func notifyHeaderDecorator(mode config.StatusbarDecoration) string {
	switch mode {
	case config.StatusbarDecorationSymbol:
		return " "
	case config.StatusbarDecorationEmoji:
		return "🔔 "
	default:
		return ""
	}
}
