package app

import (
	"context"
	"os"
	"reflect"
	"testing"
)

func TestDesktopNotifyResolverPriority(t *testing.T) {
	cases := []struct {
		name       string
		env        string
		option     string
		wantOn     bool
		wantSource desktopNotifySource
	}{
		{name: "default when nothing set", env: "", option: "", wantOn: true, wantSource: desktopNotifySourceDefault},
		{name: "tmux option off", env: "", option: "0", wantOn: false, wantSource: desktopNotifySourceSetting},
		{name: "tmux option on", env: "", option: "1", wantOn: true, wantSource: desktopNotifySourceSetting},
		{name: "env off wins over tmux on", env: "off", option: "1", wantOn: false, wantSource: desktopNotifySourceEnv},
		{name: "env on wins over tmux off", env: "on", option: "0", wantOn: true, wantSource: desktopNotifySourceEnv},
		{name: "env unknown falls through to tmux", env: "garbage", option: "0", wantOn: false, wantSource: desktopNotifySourceSetting},
		{name: "env empty falls through to default", env: "", option: "", wantOn: true, wantSource: desktopNotifySourceDefault},
		{name: "env OFF case-insensitive", env: "OFF", option: "1", wantOn: false, wantSource: desktopNotifySourceEnv},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolver := desktopNotifyResolver{
				lookupEnv: func(name string) string {
					if name == desktopNotifyEnv {
						return tc.env
					}
					return ""
				},
				readTmuxOption: func(name string) string {
					if name == desktopNotifyTmuxOption {
						return tc.option
					}
					return ""
				},
			}
			gotOn, gotSource := resolver.resolve()
			if gotOn != tc.wantOn || gotSource != tc.wantSource {
				t.Fatalf("resolve() = (%v, %q), want (%v, %q)", gotOn, gotSource, tc.wantOn, tc.wantSource)
			}
		})
	}
}

func TestDesktopNotifyGateSilencesDispatchWhenOff(t *testing.T) {
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
		case "PROJMUX_DESKTOP_NOTIFY":
			return "off"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		// notify-send is "available" but the gate should silence the
		// dispatch anyway. We assert below that it's never called.
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
		AppName: "projmux.TmuxCodex",
		Icon:    "dialog-information",
	}); err != nil {
		t.Fatalf("Notify with desktop notifications off returned error: %v", err)
	}

	for _, command := range cmdRecorder(cmd).commands {
		if command.name == "notify-send" {
			t.Fatalf("commands = %#v, did not expect notify-send dispatch when gate is off", cmdRecorder(cmd).commands)
		}
	}
}

func TestDesktopNotifyGateAllowsDispatchWhenOn(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "PROJMUX_DESKTOP_NOTIFY":
			return "on"
		default:
			return ""
		}
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
		AppName: "projmux.TmuxCodex",
		Icon:    "dialog-information",
	}); err != nil {
		t.Fatalf("Notify with desktop notifications on returned error: %v", err)
	}

	var sawNotifySend bool
	for _, command := range cmdRecorder(cmd).commands {
		if command.name == "notify-send" {
			sawNotifySend = true
			wantPrefix := []string{
				"--app-name=projmux.TmuxCodex",
			}
			if !reflect.DeepEqual(command.args[:len(wantPrefix)], wantPrefix) {
				t.Fatalf("notify-send args = %#v, want first arg %q", command.args, wantPrefix[0])
			}
		}
	}
	if !sawNotifySend {
		t.Fatalf("commands = %#v, want notify-send dispatch when gate is on", cmdRecorder(cmd).commands)
	}
}
