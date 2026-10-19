package app

import (
 "github.com/crevissepartners/projmux/internal/services/tmuxconfig"
	"net/url"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/theme"
)

const (
	statusbarDecorationTmuxOption       = tmuxconfig.StatusbarDecorationTmuxOption
	statusbarDecorationCwdTmuxOption    = tmuxconfig.StatusbarDecorationCwdTmuxOption
	statusbarDecorationGitTmuxOption    = tmuxconfig.StatusbarDecorationGitTmuxOption
	statusbarDecorationNotifyTmuxOption = tmuxconfig.StatusbarDecorationNotifyTmuxOption
)

type statusbarDecorationTarget tmuxconfig.StatusbarDecorationTarget

const (
	statusbarDecorationTargetCwd    statusbarDecorationTarget = statusbarDecorationTarget(tmuxconfig.StatusbarDecorationTargetCwd)
	statusbarDecorationTargetGit    statusbarDecorationTarget = statusbarDecorationTarget(tmuxconfig.StatusbarDecorationTargetGit)
	statusbarDecorationTargetNotify statusbarDecorationTarget = statusbarDecorationTarget(tmuxconfig.StatusbarDecorationTargetNotify)
)

type statusbarDecorationSet tmuxconfig.StatusbarDecorationSet

func statusbarDecorationSetFromGlobal(mode config.StatusbarDecoration) statusbarDecorationSet {
	return statusbarDecorationSet(tmuxconfig.StatusbarDecorationSetFromGlobal(mode))
}

func loadStatusbarDecoration(homeDir func() (string, error), lookupEnv func(string) string) config.StatusbarDecoration {
	return tmuxconfig.LoadStatusbarDecoration(homeDir, lookupEnv, configPaths)
}

func loadStatusbarDecorationSet(homeDir func() (string, error), lookupEnv func(string) string) statusbarDecorationSet {
	return statusbarDecorationSet(tmuxconfig.LoadStatusbarDecorationSet(homeDir, lookupEnv, configPaths))
}

func loadStatusbarDecorationForTarget(homeDir func() (string, error), lookupEnv func(string) string, target statusbarDecorationTarget, fallback config.StatusbarDecoration) config.StatusbarDecoration {
	return tmuxconfig.LoadStatusbarDecorationForTarget(homeDir, lookupEnv, tmuxconfig.StatusbarDecorationTarget(target), fallback, configPaths)
}

func statusbarDecorationTargetFile(paths config.Paths, target statusbarDecorationTarget) string {
	return tmuxconfig.StatusbarDecorationTargetFile(paths, tmuxconfig.StatusbarDecorationTarget(target))
}

func statusbarDecorationTmuxOptionForTarget(target statusbarDecorationTarget) string {
	return tmuxconfig.StatusbarDecorationTmuxOptionForTarget(tmuxconfig.StatusbarDecorationTarget(target))
}

func statusbarCwdSegmentFormat(roles theme.RenderRoles) string {
	return tmuxconfig.StatusbarCwdSegmentFormat(roles)
}

func statusbarCwdDecoratorFormat(roles theme.RenderRoles) string {
	return tmuxconfig.StatusbarCwdDecoratorFormat(roles)
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
			return "#[fg=" + statusSegmentRoles.DecorationGitLab + "] #[fg=" + tmuxGitSegmentFg + "]"
		default:
			return "#[fg=" + statusSegmentRoles.GitAhead + "] #[fg=" + tmuxGitSegmentFg + "]"
		}
	case config.StatusbarDecorationEmoji:
		switch provider {
		case gitRemoteProviderGitHub:
			return "#[fg=" + statusSegmentRoles.GitAhead + "]🐱 #[fg=" + tmuxGitSegmentFg + "]"
		case gitRemoteProviderGitLab:
			return "#[fg=" + statusSegmentRoles.DecorationGitLab + "]🦊 #[fg=" + tmuxGitSegmentFg + "]"
		default:
			return "#[fg=" + statusSegmentRoles.GitStaged + "]🌿 #[fg=" + tmuxGitSegmentFg + "]"
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
