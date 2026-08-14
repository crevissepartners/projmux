package app

import (
	"context"
	"os"
	"testing"
)

func TestDesktopNotifyResolverModeCascade(t *testing.T) {
	cases := []struct {
		name       string
		envMode    string
		envLegacy  string
		configMode desktopNotifyMode
		configSet  bool
		optMode    string
		optLegacy  string
		wantMode   desktopNotifyMode
		wantSource desktopNotifySource
	}{
		{
			name:     "default linux",
			wantMode: desktopNotifyModeNotify, wantSource: desktopNotifySourceDefault,
		},
		{
			name: "new tmux option off beats default", optMode: "off",
			wantMode: desktopNotifyModeNone, wantSource: desktopNotifySourceSetting,
		},
		{
			name: "new tmux option none beats default", optMode: "none",
			wantMode: desktopNotifyModeNone, wantSource: desktopNotifySourceSetting,
		},
		{
			name: "saved config off beats new tmux option", configMode: desktopNotifyModeNone, configSet: true, optMode: "notify",
			wantMode: desktopNotifyModeNone, wantSource: desktopNotifySourceSetting,
		},
		{
			name: "new env beats saved config", envMode: "notify", configMode: desktopNotifyModeNone, configSet: true,
			wantMode: desktopNotifyModeNotify, wantSource: desktopNotifySourceEnv,
		},
		{
			name: "new tmux option notify pinned", optMode: "notify",
			wantMode: desktopNotifyModeNotify, wantSource: desktopNotifySourceSetting,
		},
		{
			name: "legacy tmux 0 fallback maps to none", optLegacy: "0",
			wantMode: desktopNotifyModeNone, wantSource: desktopNotifySourceSettingLegacy,
		},
		{
			name: "legacy tmux 1 fallback maps to notify", optLegacy: "1",
			wantMode: desktopNotifyModeNotify, wantSource: desktopNotifySourceSettingLegacy,
		},
		{
			name: "new tmux overrides legacy tmux", optMode: "notify", optLegacy: "0",
			wantMode: desktopNotifyModeNotify, wantSource: desktopNotifySourceSetting,
		},
		{
			name: "new env beats new tmux", envMode: "none", optMode: "notify",
			wantMode: desktopNotifyModeNone, wantSource: desktopNotifySourceEnv,
		},
		{
			name: "legacy env beats new tmux when new env unset", envLegacy: "off", optMode: "notify",
			wantMode: desktopNotifyModeNone, wantSource: desktopNotifySourceEnvLegacy,
		},
		{
			name: "new env beats legacy env", envMode: "notify", envLegacy: "off",
			wantMode: desktopNotifyModeNotify, wantSource: desktopNotifySourceEnv,
		},
		{
			name: "unknown new env falls through to legacy env", envMode: "garbage", envLegacy: "on",
			wantMode: desktopNotifyModeNotify, wantSource: desktopNotifySourceEnvLegacy,
		},
		{
			name: "unknown new env, unknown legacy env, falls through to tmux", envMode: "garbage", envLegacy: "garbage", optMode: "notify",
			wantMode: desktopNotifyModeNotify, wantSource: desktopNotifySourceSetting,
		},
		{
			name: "env mode case insensitive", envMode: "NOTIFY",
			wantMode: desktopNotifyModeNotify, wantSource: desktopNotifySourceEnv,
		},
		{
			name: "env mode off alias maps to none", envMode: "OFF",
			wantMode: desktopNotifyModeNone, wantSource: desktopNotifySourceEnv,
		},
		// Retired `raise` family: resolved as `notify` in the source that
		// carries it, never leaking down to a lower-precedence rung.
		{
			name: "legacy raise env resolves to notify in env", envMode: "raise",
			wantMode: desktopNotifyModeNotify, wantSource: desktopNotifySourceEnv,
		},
		{
			name: "legacy raise env does not fall through to legacy env off", envMode: "auto-raise", envLegacy: "off",
			wantMode: desktopNotifyModeNotify, wantSource: desktopNotifySourceEnv,
		},
		{
			name: "legacy raise env does not fall through to tmux off", envMode: "AUTORAISE", optMode: "off",
			wantMode: desktopNotifyModeNotify, wantSource: desktopNotifySourceEnv,
		},
		{
			name: "legacy raise saved config resolves to notify in setting", configMode: desktopNotifyModeNotify, configSet: true, optMode: "off",
			wantMode: desktopNotifyModeNotify, wantSource: desktopNotifySourceSetting,
		},
		{
			name: "legacy raise tmux option resolves to notify in setting", optMode: "raise", optLegacy: "0",
			wantMode: desktopNotifyModeNotify, wantSource: desktopNotifySourceSetting,
		},
		{
			name: "legacy autoraise tmux option resolves to notify in setting", optMode: "autoraise",
			wantMode: desktopNotifyModeNotify, wantSource: desktopNotifySourceSetting,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolver := desktopNotifyResolver{
				lookupEnv: func(name string) string {
					switch name {
					case desktopNotifyModeEnv:
						return tc.envMode
					case desktopNotifyEnv:
						return tc.envLegacy
					}
					return ""
				},
				readConfigMode: func() (desktopNotifyMode, bool) {
					return tc.configMode, tc.configSet
				},
				readTmuxOption: func(name string) string {
					switch name {
					case desktopNotifyModeTmuxOption:
						return tc.optMode
					case desktopNotifyTmuxOption:
						return tc.optLegacy
					}
					return ""
				},
			}
			gotMode, gotSource := resolver.resolveMode()
			if gotMode != tc.wantMode || gotSource != tc.wantSource {
				t.Fatalf("resolveMode() = (%q, %q), want (%q, %q)", gotMode, gotSource, tc.wantMode, tc.wantSource)
			}
		})
	}
}

// TestDesktopNotifyResolverDefaultIsNotifyOnEveryPlatform pins that the
// unset-everything default no longer depends on any host signal. WSL +
// Windows Terminal used to default to the retired `raise` mode; it must now
// resolve to `notify` exactly like every other platform.
func TestDesktopNotifyResolverDefaultIsNotifyOnEveryPlatform(t *testing.T) {
	for _, env := range []map[string]string{
		{},
		{"WSL_DISTRO_NAME": "Ubuntu-24.04"},
		{"WT_SESSION": "abc-123"},
		{"WSL_DISTRO_NAME": "Ubuntu-24.04", "WT_SESSION": "abc-123"},
	} {
		lookup := func(name string) string { return env[name] }
		resolver := settingsDesktopNotifyResolver(func() (string, error) { return t.TempDir(), nil }, lookup)
		gotMode, gotSource := resolver.resolveMode()
		if gotMode != desktopNotifyModeNotify || gotSource != desktopNotifySourceDefault {
			t.Fatalf("resolveMode() with env %#v = (%q, %q), want (notify, default)", env, gotMode, gotSource)
		}
	}
}

func TestNotificationExpireMSDefaultOverrideInvalid(t *testing.T) {
	home := t.TempDir()
	cases := []struct {
		name string
		env  string
		want int
	}{
		{name: "default", want: defaultAINotifyExpireMS},
		{name: "override", env: "2500", want: 2500},
		{name: "zero invalid", env: "0", want: defaultAINotifyExpireMS},
		{name: "negative invalid", env: "-1", want: defaultAINotifyExpireMS},
		{name: "text invalid", env: "soon", want: defaultAINotifyExpireMS},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := testAICommand(home)
			cmd.lookupEnv = func(name string) string {
				switch name {
				case "HOME":
					return home
				case aiNotifyExpireMSEnv:
					return tc.env
				default:
					return ""
				}
			}
			if got := cmd.notificationExpireMS(); got != tc.want {
				t.Fatalf("notificationExpireMS() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestParseDesktopNotifyModeAccepts(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want desktopNotifyMode
	}{
		{"none", desktopNotifyModeNone},
		{"NONE", desktopNotifyModeNone},
		{"off", desktopNotifyModeNone},
		{"disabled", desktopNotifyModeNone},
		{"notify", desktopNotifyModeNotify},
		{"toast", desktopNotifyModeNotify},
		// Retired `raise` family: accepted, read as notify.
		{"raise", desktopNotifyModeNotify},
		{"AUTO-RAISE", desktopNotifyModeNotify},
		{"autoraise", desktopNotifyModeNotify},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := parseDesktopNotifyMode(tc.in)
			if !ok || got != tc.want {
				t.Fatalf("parseDesktopNotifyMode(%q) = (%q, %v), want (%q, true)", tc.in, got, ok, tc.want)
			}
		})
	}
}

func TestParseDesktopNotifyModeRejects(t *testing.T) {
	for _, tc := range []string{"", "garbage", "1", "0", "true", "yes"} {
		t.Run(tc, func(t *testing.T) {
			if _, ok := parseDesktopNotifyMode(tc); ok {
				t.Fatalf("parseDesktopNotifyMode(%q) should not match", tc)
			}
		})
	}
}

// TestDesktopNotifyGateSilencesDispatchWhenNone confirms the boolean
// shim (still wired into aiDesktopNotifier.Notify in this commit) maps
// mode=none to false and silences the notify-send dispatch.
func TestDesktopNotifyGateSilencesDispatchWhenNone(t *testing.T) {
	home := t.TempDir()
	work := home + "/repo"
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case desktopNotifyModeEnv:
			return "none"
		}
		return ""
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && len(args) == 2 && args[0] == "-v" && args[1] == "notify-send" {
			return []byte("/usr/bin/notify-send\n"), nil
		}
		return nil, os.ErrNotExist
	}

	notifier := aiDesktopNotifier{command: cmd}
	if err := notifier.Notify(aiNotification{
		Summary: "Hello",
		Body:    "World",
		Urgency: "normal",
		AppName: desktopAppID,
		Icon:    "dialog-information",
	}); err != nil {
		t.Fatalf("Notify with mode=none returned error: %v", err)
	}

	for _, command := range cmdRecorder(cmd).commands {
		if command.name == "notify-send" {
			t.Fatalf("commands = %#v, did not expect notify-send dispatch when mode=none", cmdRecorder(cmd).commands)
		}
	}
}

// TestDesktopNotifyGateAllowsDispatchWhenNotify confirms the boolean
// shim treats mode=notify as on so notify-send fires.
func TestDesktopNotifyGateAllowsDispatchWhenNotify(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case desktopNotifyModeEnv:
			return "notify"
		}
		return ""
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && len(args) == 2 && args[0] == "-v" && args[1] == "notify-send" {
			return []byte("/usr/bin/notify-send\n"), nil
		}
		return nil, os.ErrNotExist
	}

	notifier := aiDesktopNotifier{command: cmd}
	if err := notifier.Notify(aiNotification{
		Summary: "Hello",
		Body:    "World",
		Urgency: "normal",
		AppName: desktopAppID,
		Icon:    "dialog-information",
	}); err != nil {
		t.Fatalf("Notify with mode=notify returned error: %v", err)
	}

	var sawNotifySend bool
	for _, command := range cmdRecorder(cmd).commands {
		if command.name == "notify-send" {
			sawNotifySend = true
			break
		}
	}
	if !sawNotifySend {
		t.Fatalf("commands = %#v, want notify-send dispatch when mode=notify", cmdRecorder(cmd).commands)
	}
}

// TestDesktopNotifyModeLegacyFallback exercises the migration read path:
// a user with the legacy `PROJMUX_DESKTOP_NOTIFY=off` and no new-mode env
// keeps the silenced behavior (mapped to none).
func TestDesktopNotifyModeLegacyFallback(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case desktopNotifyEnv:
			return "off"
		}
		return ""
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && len(args) == 2 && args[0] == "-v" && args[1] == "notify-send" {
			return []byte("/usr/bin/notify-send\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if got, src := cmd.desktopNotifyModeResolution(); got != desktopNotifyModeNone || src != desktopNotifySourceEnvLegacy {
		t.Fatalf("legacy off env mapping = (%q, %q), want (none, env (legacy))", got, src)
	}

	notifier := aiDesktopNotifier{command: cmd}
	if err := notifier.Notify(aiNotification{
		Summary: "Hello",
		Body:    "World",
		Urgency: "normal",
		AppName: desktopAppID,
		Icon:    "dialog-information",
	}); err != nil {
		t.Fatalf("Notify legacy off returned error: %v", err)
	}
	for _, command := range cmdRecorder(cmd).commands {
		if command.name == "notify-send" {
			t.Fatalf("commands = %#v, did not expect notify-send dispatch when legacy off env is set", cmdRecorder(cmd).commands)
		}
	}
}
