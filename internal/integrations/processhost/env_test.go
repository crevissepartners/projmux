package processhost

import (
	"slices"
	"strings"
	"testing"
)

// A web server or an agent shell carries another session's provider markers,
// tmux client and activation identity. A headless provider keeps only the
// allowlisted user session and its own provider configuration.
var inheritedProviderMarkers = []string{
	"CLAUDECODE=1",
	"CLAUDE_CODE_SESSION_ID=other-session",
	"CLAUDE_CODE_MESSAGING_SOCKET=/run/other.sock",
	"CLAUDE_CODE_MESSAGING_TOKEN=other-token",
	"CLAUDE_CODE_ENTRYPOINT=cli",
	"CLAUDE_CODE_CHILD_SESSION=1",
	"CLAUDE_PID=1",
	"CODEX_THREAD_ID=other-thread",
	"CODEX_CI=1",
	"CODEX_SANDBOX=seatbelt",
	"GOMAXPROCS=2",
	"GOFLAGS=-mod=mod",
	"NO_COLOR=1",
	"TMUX=/tmp/tmux-1000/default,1,0",
	"TMUX_PANE=%1",
	"__PROJMUX_RUNTIME_ANCHOR_PANE=%1",
	"PMX_INTERNAL_ACTIVATION_PANE_UID=pane-other",
	"PMX_INTERNAL_CLAUDE_PROCESS_BINDING={}",
	"PMX_INTERNAL_CODEX_PROCESS_HOST=/run/other",
	"PMX_PLANTED_MARKER=1",
	"MALFORMED",
}

var allowedSessionEnv = []string{
	"PATH=/usr/bin",
	"HOME=/home/user",
	"USER=user",
	"LOGNAME=user",
	"SHELL=/bin/zsh",
	"LANG=ko_KR.UTF-8",
	"LC_ALL=C.UTF-8",
	"TZ=Asia/Seoul",
	"TMPDIR=/tmp/user",
	"TMUX_TMPDIR=/run/user/tmux",
	"XDG_CONFIG_HOME=/home/user/.config",
	"XDG_RUNTIME_DIR=/run/user/1000",
	"HTTPS_PROXY=http://proxy",
	"no_proxy=localhost",
	"SSL_CERT_FILE=/etc/ssl/cert.pem",
	"PROJMUX_PROJDIR=/home/user/repos",
}

func TestProviderCommandsKeepOnlyTheAllowlistedEnvironment(t *testing.T) {
	claudeOnly := []string{"ANTHROPIC_API_KEY=a", "ANTHROPIC_BASE_URL=http://stub", "CLAUDE_CONFIG_DIR=/c", "CLAUDE_CODE_USE_BEDROCK=1", "CLAUDE_CODE_OAUTH_TOKEN=o", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "AWS_REGION=us-east-1"}
	codexOnly := []string{"CODEX_HOME=/x", "CODEX_SQLITE_HOME=/s", "CODEX_CA_CERTIFICATE=/ca", "OPENAI_API_KEY=k", "OPENAI_BASE_URL=http://stub"}
	var env []string
	for i := range max(len(allowedSessionEnv), len(inheritedProviderMarkers)) {
		if i < len(inheritedProviderMarkers) {
			env = append(env, inheritedProviderMarkers[i])
		}
		if i < len(allowedSessionEnv) {
			env = append(env, allowedSessionEnv[i])
		}
	}
	env = append(append(env, claudeOnly...), codexOnly...)

	claude, err := ClaudeCommand("claude", "/work", env, nil)
	if err != nil {
		t.Fatal(err)
	}
	codex, err := CodexCommand("codex", "/work", env, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		got, want  []string
		notAllowed []string
	}{
		{"claude", claude.Env, append(slices.Clone(allowedSessionEnv), claudeOnly...), codexOnly},
		{"codex", codex.Env, append(slices.Clone(allowedSessionEnv), codexOnly...), claudeOnly},
	} {
		if !slices.Equal(tc.got, tc.want) {
			t.Errorf("%s env keys = %v, want %v", tc.name, keys(tc.got), keys(tc.want))
		}
		for _, value := range append(slices.Clone(inheritedProviderMarkers), tc.notAllowed...) {
			if slices.Contains(tc.got, value) {
				t.Errorf("%s kept %s", tc.name, strings.SplitN(value, "=", 2)[0])
			}
		}
	}
}

func TestProviderCommandsNeverReturnAnImplicitEnvironment(t *testing.T) {
	for _, env := range [][]string{nil, {}, {"CLAUDECODE=1"}} {
		claude, err := ClaudeCommand("claude", "/work", env, nil)
		if err != nil || claude.Env == nil || len(claude.Env) != 0 {
			t.Fatalf("ClaudeCommand(%v) env = %#v, %v", env, claude.Env, err)
		}
		codex, err := CodexCommand("codex", "/work", env, nil)
		if err != nil || codex.Env == nil || len(codex.Env) != 0 {
			t.Fatalf("CodexCommand(%v) env = %#v, %v", env, codex.Env, err)
		}
	}
}

func TestProviderCommandsDoNotAliasTheCallerEnvironment(t *testing.T) {
	env := []string{"HOME=/home/user"}
	cmd, err := CodexCommand("codex", "/work", env, nil)
	if err != nil {
		t.Fatal(err)
	}
	env[0] = "HOME=/mutated"
	if cmd.Env[0] != "HOME=/home/user" {
		t.Fatal("CodexCommand aliased the caller's environment")
	}
}

func keys(env []string) []string {
	out := make([]string, 0, len(env))
	for _, value := range env {
		key, _, _ := strings.Cut(value, "=")
		out = append(out, key)
	}
	return out
}
