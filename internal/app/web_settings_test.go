package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/web"
)

type recordingTmux struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingTmux) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	return nil, errors.New("no server running on /tmp/none")
}

// webSettingsHarness points the settings the web reads and writes at an empty
// home, with a tmux runner that records what it was asked.
func webSettingsHarness(t *testing.T) (send func(method, body string) (int, map[string]any), home string, tmux *recordingTmux) {
	t.Helper()
	home = t.TempDir()
	env := func(key string) string {
		if key == "HOME" {
			return home
		}
		return ""
	}
	homeDir := func() (string, error) { return home, nil }
	tmux = &recordingTmux{}
	prevSettings, prevLaunch := webSettingsCommand, webLaunchCommand
	webSettingsCommand = func() *settingsCommand {
		return &settingsCommand{homeDir: homeDir, lookupEnv: env, tmuxRunner: tmux, reloadAppServer: true}
	}
	webLaunchCommand = func() *aiCommand {
		c := newAICommand()
		c.homeDir, c.lookupEnv = homeDir, env
		return c
	}
	t.Cleanup(func() { webSettingsCommand, webLaunchCommand = prevSettings, prevLaunch })
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	backend, _ := webFixtureBackend(t)
	handler := web.New(backend, nil).Handler()
	return func(method, body string) (int, map[string]any) {
		if method == "GET" {
			return webGet(t, handler, "/api/v1/web/settings")
		}
		return webSend(t, handler, method, "/api/v1/web/settings", body)
	}, home, tmux
}

func TestWebSettingsEnvNeverCarriesTmuxClientEvidence(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1,0")
	t.Setenv("TMUX_PANE", "%3")
	t.Setenv(runtimeMutationAnchorPaneEnv, "%3")
	for _, key := range []string{"TMUX", "TMUX_PANE", runtimeMutationAnchorPaneEnv} {
		if got := webSettingsEnv(key); got != "" {
			t.Errorf("webSettingsEnv(%s) = %q", key, got)
		}
	}
	if os.Getenv("TMUX") == "" {
		t.Fatal("the test environment lost TMUX; the check above proves nothing")
	}
}

func TestWebSettingsSaveThroughTheSettingsFunctions(t *testing.T) {
	send, home, _ := webSettingsHarness(t)

	if code, body := send("GET", ""); code != 200 || body["ai"] == nil || body["statusbar"] == nil {
		t.Fatalf("settings = %d %v", code, body)
	}
	for _, change := range []string{
		`{"key":"ai.provider.codex","value":"off"}`,
		`{"key":"ai.provider.codex","value":"off"}`,
		`{"key":"ai.defaultMode","value":"claude"}`,
		`{"key":"ai.splitCwdFrom","value":"pane"}`,
		`{"key":"locale","value":"ko-KR"}`,
	} {
		if code, body := send("PATCH", change); code != 200 {
			t.Fatalf("PATCH %s = %d %v", change, code, body)
		}
	}
	homeDir := func() (string, error) { return home, nil }
	env := func(string) string { return "" }
	for _, agent := range aiEnabledAgents(homeDir, env) {
		if agent == config.AIAgentProvider("codex") {
			t.Fatal("codex is still enabled; a repeated off must not toggle it back on")
		}
	}
	mode, err := os.ReadFile(filepath.Join(home, ".config", "projmux", "tmux-ai-split-mode"))
	if err != nil || strings.TrimSpace(string(mode)) != "claude" {
		t.Fatalf("default mode file = %q %v", mode, err)
	}
	settings := &settingsCommand{homeDir: homeDir, lookupEnv: env}
	if got := resolveUISplitCWDSource("", "", homeDir, env); got.Source != splitCWDFromPane {
		t.Fatalf("split cwd = %+v", got)
	}
	if locale, _, err := settings.currentGlobalLocaleSetting(); err != nil || locale != "ko-KR" {
		t.Fatalf("locale = %q %v", locale, err)
	}

	for _, bad := range []string{
		`{"key":"ai.defaultMode","value":"vim"}`,
		`{"key":"ai.provider.codex","value":"maybe"}`,
		`{"key":"statusbar.usage.codex.monthly","value":"on"}`,
		`{"key":"theme.preset","value":"forest"}`,
	} {
		if code, body := send("PATCH", bad); code != 400 {
			t.Errorf("PATCH %s = %d %v, want 400", bad, code, body)
		}
	}
}

// A status bar change regenerates the tmux config and reloads the app server.
// The reload is routed through the app's own logical socket; a TMUX the
// process happens to carry is never the route.
func TestWebStatusbarSettingReloadsTheAppServerNotAnInheritedOne(t *testing.T) {
	send, home, tmux := webSettingsHarness(t)
	t.Setenv("TMUX", "/tmp/tmux-1000/stale,1,0")

	code, body := send("PATCH", `{"key":"statusbar.clock","value":"off"}`)
	if _, err := os.Stat(filepath.Join(home, ".config", "projmux", "tmux.conf")); err != nil {
		t.Fatalf("the tmux config was not regenerated: %v (%d %v)", err, code, body)
	}
	paths, err := configPaths(func() (string, error) { return home, nil }, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	state, err := config.LoadStatusbarVisibilityFile(paths.StatusbarClockVisibilityFile())
	if err != nil || state.Effective != config.StatusbarVisibilityOff {
		t.Fatalf("clock visibility = %+v %v", state, err)
	}
	if len(tmux.calls) == 0 {
		t.Fatal("the app server was not asked to reload")
	}
	for _, call := range tmux.calls {
		if strings.Contains(call, "stale") || !strings.Contains(call, "-L "+defaultAppSocket) {
			t.Errorf("reload call %q is not routed through -L %s", call, defaultAppSocket)
		}
	}
	// No app server answered, so the save is reported as not reloaded.
	if code != 409 || errorCode(body) != web.CodeRefused {
		t.Fatalf("PATCH = %d %v, want a refused reload", code, body)
	}
}
