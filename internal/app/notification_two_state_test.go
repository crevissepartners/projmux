package app

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

func notifySendIsAvailable(_ context.Context, name string, args ...string) ([]byte, error) {
	if name == "command" && len(args) == 2 && args[0] == "-v" && args[1] == "notify-send" {
		return []byte("/usr/bin/notify-send\n"), nil
	}
	return nil, os.ErrNotExist
}

// TestNotifyMode_OffSuppressesDispatch pins mode=off: no toast, no
// notify-send. The in-app surfaces are orthogonal (not this test's concern) so
// we only assert the OS side stays silent.
func TestNotifyMode_OffSuppressesDispatch(t *testing.T) {
	for _, literal := range []string{"off", "none", "disabled"} {
		t.Run(literal, func(t *testing.T) {
			home := t.TempDir()
			cmd := testAICommand(home)
			cmd.lookupEnv = func(name string) string {
				switch name {
				case "HOME":
					return home
				case desktopNotifyModeEnv:
					return literal
				}
				return ""
			}
			cmd.readCommand = notifySendIsAvailable

			notifier := aiDesktopNotifier{command: cmd}
			if err := notifier.Notify(aiNotification{
				Summary: "Hello", Body: "World", Urgency: "normal",
				AppName: desktopAppID, Icon: "dialog-information", Tag: "%8",
			}); err != nil {
				t.Fatalf("Notify(mode=%s) error = %v", literal, err)
			}
			for _, command := range cmdRecorder(cmd).commands {
				if command.name == "notify-send" {
					t.Fatalf("commands = %#v, did not expect notify-send dispatch when mode=%s", cmdRecorder(cmd).commands, literal)
				}
			}
		})
	}
}

// TestNotifyMode_NotifyDispatchesOnlyTheNotification pins the notify mode on
// the Linux path: notify-send fires and nothing else runs. In particular there
// must be no host-window focus adapter invocation (`wt.exe`, `kdotool`,
// `swaymsg`, `xdotool`, …) — desktop delivery may never steal terminal focus.
func TestNotifyMode_NotifyDispatchesOnlyTheNotification(t *testing.T) {
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
	cmd.readCommand = notifySendIsAvailable

	notifier := aiDesktopNotifier{command: cmd}
	if err := notifier.Notify(aiNotification{
		Summary: "Hello", Body: "World", Urgency: "normal",
		AppName: desktopAppID, Icon: "dialog-information", Tag: "%8",
	}); err != nil {
		t.Fatalf("Notify(mode=notify) error = %v", err)
	}

	var sawNotifySend bool
	for _, command := range cmdRecorder(cmd).commands {
		if command.name == "notify-send" {
			sawNotifySend = true
		}
		assertNoHostFocusDispatch(t, command.name, command.args)
	}
	if !sawNotifySend {
		t.Fatalf("commands = %#v, want notify-send dispatch when mode=notify", cmdRecorder(cmd).commands)
	}
}

// TestNotifyMode_LegacyRaiseEnvBehavesExactlyLikeNotify pins the alias on the
// runtime gate: an existing `PROJMUX_DESKTOP_NOTIFY_MODE=raise` delivers the
// plain notification and never re-enables a host-window raise.
func TestNotifyMode_LegacyRaiseEnvBehavesExactlyLikeNotify(t *testing.T) {
	for _, literal := range []string{"raise", "auto-raise", "AUTORAISE"} {
		t.Run(literal, func(t *testing.T) {
			home := t.TempDir()
			cmd := testAICommand(home)
			cmd.lookupEnv = func(name string) string {
				switch name {
				case "HOME":
					return home
				case desktopNotifyModeEnv:
					return literal
				}
				return ""
			}
			cmd.readCommand = notifySendIsAvailable

			if got, src := cmd.desktopNotifyModeResolution(); got != desktopNotifyModeNotify || src != desktopNotifySourceEnv {
				t.Fatalf("desktopNotifyModeResolution() with %q = (%q, %q), want (notify, env)", literal, got, src)
			}

			notifier := aiDesktopNotifier{command: cmd}
			if err := notifier.Notify(aiNotification{
				Summary: "Hello", Body: "World", Urgency: "normal",
				AppName: desktopAppID, Icon: "dialog-information", Tag: "%8",
			}); err != nil {
				t.Fatalf("Notify(mode=%s) error = %v", literal, err)
			}
			var sawNotifySend bool
			for _, command := range cmdRecorder(cmd).commands {
				if command.name == "notify-send" {
					sawNotifySend = true
				}
				assertNoHostFocusDispatch(t, command.name, command.args)
			}
			if !sawNotifySend {
				t.Fatalf("commands = %#v, want notify-send dispatch for legacy %q", cmdRecorder(cmd).commands, literal)
			}
		})
	}
}

// TestNotifyMode_WSLToastCarriesNoClickTargetOrProtocolRegistration pins the
// WSL Toast path: the Toast XML has no `launch` / `activationType` pair, the
// script never references the `projmux://` scheme, and no protocol-handler
// registration marker is written. The Toast payload must also not need the
// tmux socket path, so that read is gone too.
func TestNotifyMode_WSLToastCarriesNoClickTargetOrProtocolRegistration(t *testing.T) {
	for _, literal := range []string{"notify", "raise"} {
		t.Run(literal, func(t *testing.T) {
			home := t.TempDir()
			cmd := testAICommand(home)
			cmd.lookupEnv = func(name string) string {
				switch name {
				case "HOME":
					return home
				case "WSL_DISTRO_NAME":
					return "Ubuntu-24.04"
				case "WT_SESSION":
					return "abc-123"
				case desktopNotifyModeEnv:
					return literal
				}
				return ""
			}
			psPath := "/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe"
			cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == "command" && len(args) == 2 && args[0] == "-v" && args[1] == "powershell.exe" {
					return []byte(psPath + "\n"), nil
				}
				if name == "command" && len(args) == 2 && args[0] == "-v" && args[1] == "wsl-notify-send.exe" {
					return nil, os.ErrNotExist
				}
				if name == "tmux" && len(args) == 3 && args[0] == "display-message" && args[1] == "-p" && args[2] == "#{socket_path}" {
					t.Fatalf("toast dispatch must not read the tmux socket path (only the click URI needed it)")
				}
				return []byte("\n"), nil
			}

			notifier := aiDesktopNotifier{command: cmd}
			if err := notifier.Notify(aiNotification{
				Summary: "Hello", Body: "World", Urgency: "normal",
				AppName: desktopAppID, Tag: "%8", Group: "repo",
			}); err != nil {
				t.Fatalf("Notify(mode=%s WSL) error = %v", literal, err)
			}

			var toastScript string
			for _, command := range cmdRecorder(cmd).commands {
				assertNoHostFocusDispatch(t, command.name, command.args)
				for _, arg := range command.args {
					if strings.Contains(arg, "@projmux_uri_protocol_registered") {
						t.Fatalf("commands = %#v, did not expect a uri protocol marker write", cmdRecorder(cmd).commands)
					}
				}
				if command.name == psPath && len(command.args) >= 3 && command.args[0] == "-NoProfile" && command.args[2] == "-EncodedCommand" {
					script := decodePowerShellEncodedCommand(t, command)
					if strings.Contains(script, "CreateToastNotifier('"+desktopAppID+"').Show($toast)") {
						toastScript = script
					}
					for _, forbidden := range []string{`HKCU:\SOFTWARE\Classes\projmux"`, "URL Protocol", "uri-handler.vbs", "focus --uri"} {
						if strings.Contains(script, forbidden) {
							t.Fatalf("powershell script must not register a protocol handler (%q):\n%s", forbidden, script)
						}
					}
				}
			}
			if toastScript == "" {
				t.Fatalf("commands = %#v, want toast powershell command", cmdRecorder(cmd).commands)
			}
			for _, forbidden := range []string{`activationType="protocol"`, "launch=", "projmux://focus?", "pane_id="} {
				if strings.Contains(toastScript, forbidden) {
					t.Fatalf("toast script = %q, did not want click target substring %q", toastScript, forbidden)
				}
			}
		})
	}
}

// assertNoHostFocusDispatch fails when a recorded command looks like a
// host-window focus/raise adapter. The osfocus package is gone; this guards
// against a replacement sneaking back into the notify or focus hot path.
func assertNoHostFocusDispatch(t *testing.T, name string, args []string) {
	t.Helper()

	joined := strings.ToLower(name + " " + strings.Join(args, " "))
	for _, forbidden := range []string{
		"wt.exe",
		"focus-tab",
		"kdotool",
		"xdotool",
		"swaymsg",
		"setforegroundwindow",
		"org.gnome.shell.eval",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("command %q %v must not dispatch a host-window focus adapter (%q)", name, args, forbidden)
		}
	}
}

// TestFocus_NeverDispatchesHostWindowFocus pins acceptance criterion 5:
// `projmux focus` selects its tmux target and stops there, in every desktop
// notification mode including the retired `raise` literals.
func TestFocus_NeverDispatchesHostWindowFocus(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"", "off", "none", "notify", "raise", "auto-raise", "autoraise"} {
		t.Run("mode="+mode, func(t *testing.T) {
			t.Parallel()

			runner := &focusFakeRunner{
				respond: func(args []string) ([]byte, error) {
					switch {
					case containsArg(args, "list-sessions"):
						return []byte("100\tworkspace\t1\n"), nil
					case containsArg(args, "list-clients"):
						return []byte("/dev/pts/0\tworkspace\n"), nil
					}
					return nil, nil
				},
			}
			env := map[string]string{
				"WSL_DISTRO_NAME": "Ubuntu-24.04",
				"WT_SESSION":      "abc-123",
			}
			if mode != "" {
				env[desktopNotifyModeEnv] = mode
			}
			cmd := newFocusTestCommand(runner, env, nil)

			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			if err := cmd.Run([]string{"--target", "workspace:1.0"}, stdout, stderr); err != nil {
				t.Fatalf("Run returned error: %v (stderr=%s)", err, stderr.String())
			}

			var sawSwitchClient bool
			for _, call := range runner.calls {
				if containsArg(call.args, "switch-client") {
					sawSwitchClient = true
				}
				assertNoHostFocusDispatch(t, call.name, call.args)
			}
			if !sawSwitchClient {
				t.Fatalf("runner calls = %#v, want the tmux switch-client dispatch to still happen", runner.calls)
			}
		})
	}
}
