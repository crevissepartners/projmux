package app

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
)

const statusbarDecorationTmuxOption = "@projmux_statusbar_decoration"

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

func statusbarCwdSegmentFormat() string {
	return "#[range=user|pwd]" + statusbarCwdDecoratorFormat() + "#[fg=colour250]#{=-28/...:pane_current_path}#[norange]"
}

func statusbarCwdDecoratorFormat() string {
	return "#{?#{==:#{@projmux_statusbar_decoration},symbol},#[fg=colour33] ,#{?#{==:#{@projmux_statusbar_decoration},emoji},#[fg=colour244]📁 ,}}"
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
