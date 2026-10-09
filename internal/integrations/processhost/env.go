package processhost

import (
	"slices"
	"strings"
)

// envPolicy names the inherited variables a provider child may keep: exact
// keys and key prefixes. Everything else the caller inherited is dropped.
type envPolicy struct {
	keys, prefixes []string
}

// commonEnv is the user session a headless provider and the commands it runs
// need: locale, paths, temporary and XDG locations, proxies and CA bundles.
// TMUX_TMPDIR locates the tmux server for projmux commands an agent runs; the
// TMUX and TMUX_PANE client markers are not kept.
var commonEnv = envPolicy{
	keys: append([]string{
		"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LANGUAGE", "TZ", "TMPDIR", "TMUX_TMPDIR", "SSH_AUTH_SOCK",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "no_proxy", "all_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
	}, projmuxSettingsEnv...),
	prefixes: []string{"LC_", "XDG_"},
}

// projmuxSettingsEnv are the user settings documented in
// docs/configuration.md that the projmux commands an agent or its provider
// hooks run still read: project roots, locale and notification delivery. The
// rest of the PROJMUX_ namespace is per-invocation context (hook variables such
// as PROJMUX_PANE and PROJMUX_SESSION, popup, notify-depth and switch handoff
// keys) or concerns the operator's own terminal, so it is never inherited.
var projmuxSettingsEnv = []string{
	"PROJMUX_PROJDIR", "PROJMUX_MANAGED_ROOTS", "TMUX_SESSIONIZER_ROOTS", "PROJMUX_LOCALE",
	"PROJMUX_NOTIFY_HOOK", "PROJMUX_NOTIFY_EXPIRE_MS", "PROJMUX_DESKTOP_NOTIFY_MODE", "PROJMUX_DESKTOP_NOTIFY",
	"PROJMUX_WSL_TOAST_ICON_DIR", "PROJMUX_TMUX_NOTIFY_DEDUPE_SECONDS",
}

// claudeEnv is Claude Code's authentication, cloud-provider and configuration
// surface. Session markers a running Claude Code gives its own children
// (CLAUDECODE, CLAUDE_CODE_SESSION_ID, CLAUDE_CODE_MESSAGING_*,
// CLAUDE_CODE_ENTRYPOINT, CLAUDE_PID and the like) are not on it.
var claudeEnv = envPolicy{
	keys: []string{
		"CLAUDE_CONFIG_DIR", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_API_KEY_HELPER_TTL_MS",
		"CLAUDE_CODE_CLIENT_CERT", "CLAUDE_CODE_CLIENT_KEY", "CLAUDE_CODE_CLIENT_KEY_PASSPHRASE",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "DISABLE_TELEMETRY", "DISABLE_ERROR_REPORTING", "DISABLE_AUTOUPDATER",
		"CLOUD_ML_REGION", "GOOGLE_APPLICATION_CREDENTIALS",
	},
	prefixes: []string{"ANTHROPIC_", "CLAUDE_CODE_USE_", "CLAUDE_CODE_SKIP_", "AWS_", "VERTEX_REGION_"},
}

// codexEnv is Codex's authentication and state surface. CODEX_THREAD_ID,
// CODEX_CI and the sandbox markers Codex gives its own children are not on it.
var codexEnv = envPolicy{
	keys:     []string{"CODEX_HOME", "CODEX_SQLITE_HOME", "CODEX_CA_CERTIFICATE", "CODEX_API_KEY", "AZURE_OPENAI_API_KEY"},
	prefixes: []string{"OPENAI_"},
}

// providerEnv keeps only the inherited variables the common and provider
// policies allow, in their original order. The result is never nil: a host
// refuses an implicit environment. Activation identity is not inherited; the
// consumer appends it to the filtered environment at launch.
func providerEnv(env []string, provider envPolicy) []string {
	kept := make([]string, 0, len(env))
	for _, value := range env {
		key, _, ok := strings.Cut(value, "=")
		if ok && (commonEnv.allows(key) || provider.allows(key)) {
			kept = append(kept, value)
		}
	}
	return kept
}

func (p envPolicy) allows(key string) bool {
	if slices.Contains(p.keys, key) {
		return true
	}
	for _, prefix := range p.prefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}
