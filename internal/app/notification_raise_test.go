package app

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/osfocus"
)

// recordingOSFocusChain is the test stub plugged into
// `aiDesktopNotifier.osFocusChain` so the mode=raise auto-raise hook can
// be observed without shelling out to the production WT × WSL adapter.
type recordingOSFocusChain struct {
	mu    sync.Mutex
	calls []osfocus.Target
	err   error
}

func (c *recordingOSFocusChain) Focus(target osfocus.Target) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, target)
	return c.err
}

func (c *recordingOSFocusChain) snapshot() []osfocus.Target {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]osfocus.Target, len(c.calls))
	copy(out, c.calls)
	return out
}

func notifySendIsAvailable(_ context.Context, name string, args ...string) ([]byte, error) {
	if name == "command" && len(args) == 2 && args[0] == "-v" && args[1] == "notify-send" {
		return []byte("/usr/bin/notify-send\n"), nil
	}
	return nil, os.ErrNotExist
}

// TestNotifyMode_NoneSuppressesDispatchAndRaise pins the mode=none path:
// no toast, no notify-send, no auto-raise. The in-app surfaces are
// orthogonal (not the concern of this test) so we just assert the OS
// side stays silent.
func TestNotifyMode_NoneSuppressesDispatchAndRaise(t *testing.T) {
	home := t.TempDir()
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
	cmd.readCommand = notifySendIsAvailable

	chain := &recordingOSFocusChain{}
	notifier := aiDesktopNotifier{command: cmd, osFocusChain: chain}
	if err := notifier.Notify(aiNotification{
		Summary: "Hello", Body: "World", Urgency: "normal",
		AppName: desktopAppID, Icon: "dialog-information", Tag: "%8",
	}); err != nil {
		t.Fatalf("Notify(mode=none) error = %v", err)
	}
	for _, command := range cmdRecorder(cmd).commands {
		if command.name == "notify-send" {
			t.Fatalf("commands = %#v, did not expect notify-send dispatch when mode=none", cmdRecorder(cmd).commands)
		}
	}
	if got := chain.snapshot(); len(got) != 0 {
		t.Fatalf("osfocus chain calls = %#v, did not expect a raise when mode=none", got)
	}
}

// TestNotifyMode_NotifyDispatchesButDoesNotRaise pins the mode=notify
// path: notify-send fires, but the osfocus chain is NOT invoked.
func TestNotifyMode_NotifyDispatchesButDoesNotRaise(t *testing.T) {
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

	chain := &recordingOSFocusChain{}
	notifier := aiDesktopNotifier{command: cmd, osFocusChain: chain}
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
			break
		}
	}
	if !sawNotifySend {
		t.Fatalf("commands = %#v, want notify-send dispatch when mode=notify", cmdRecorder(cmd).commands)
	}
	if got := chain.snapshot(); len(got) != 0 {
		t.Fatalf("osfocus chain calls = %#v, did not expect a raise when mode=notify", got)
	}
}

// TestNotifyMode_RaiseDispatchesAndRaises pins the mode=raise path:
// notify-send fires AND the osfocus chain receives a Focus() call with
// the originating pane id threaded through. Tier-1 adapters ignore the
// pane field; tier-2 adapters that target specific windows/tabs can use
// it as a hint.
func TestNotifyMode_RaiseDispatchesAndRaises(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case desktopNotifyModeEnv:
			return "raise"
		}
		return ""
	}
	cmd.readCommand = notifySendIsAvailable

	chain := &recordingOSFocusChain{}
	notifier := aiDesktopNotifier{command: cmd, osFocusChain: chain}
	if err := notifier.Notify(aiNotification{
		Summary: "Hello", Body: "World", Urgency: "normal",
		AppName: desktopAppID, Icon: "dialog-information", Tag: "%8",
	}); err != nil {
		t.Fatalf("Notify(mode=raise) error = %v", err)
	}
	var sawNotifySend bool
	for _, command := range cmdRecorder(cmd).commands {
		if command.name == "notify-send" {
			sawNotifySend = true
			break
		}
	}
	if !sawNotifySend {
		t.Fatalf("commands = %#v, want notify-send dispatch when mode=raise", cmdRecorder(cmd).commands)
	}
	calls := chain.snapshot()
	if len(calls) != 1 {
		t.Fatalf("osfocus chain calls = %#v, want exactly one Focus() when mode=raise", calls)
	}
	if got, want := calls[0].Pane, "%8"; got != want {
		t.Fatalf("osfocus.Target.Pane = %q, want %q (originating pane id should be threaded through)", got, want)
	}
}

// TestNotifyMode_RaiseSkippedWhenDispatchFails confirms the raise call
// is gated on a *successful* notification dispatch. If notify-send is
// unavailable, the user would see a raised terminal with no message
// explaining why — we explicitly avoid that surprise.
func TestNotifyMode_RaiseSkippedWhenDispatchFails(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case desktopNotifyModeEnv:
			return "raise"
		}
		return ""
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		// notify-send is NOT available — `command -v notify-send` fails.
		return nil, os.ErrNotExist
	}

	chain := &recordingOSFocusChain{}
	notifier := aiDesktopNotifier{command: cmd, osFocusChain: chain}
	err := notifier.Notify(aiNotification{
		Summary: "Hello", Body: "World", Urgency: "normal",
		AppName: desktopAppID, Icon: "dialog-information", Tag: "%8",
	})
	if err == nil {
		t.Fatalf("Notify(mode=raise) with notify-send unavailable should return error, got nil")
	}
	if got := chain.snapshot(); len(got) != 0 {
		t.Fatalf("osfocus chain calls = %#v, raise must NOT fire when dispatch failed", got)
	}
}
