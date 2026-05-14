package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
)

func TestAINotifyDedupeSecondsResolutionPrecedence(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	paths := config.DefaultPaths(filepath.Join(home, ".config"), filepath.Join(home, ".local", "state"))
	lookup := func(name string) string {
		if name == "HOME" {
			return home
		}
		return ""
	}

	got := resolveAINotifyDedupeSeconds(func() (string, error) { return home, nil }, lookup)
	if got.Seconds != defaultAINotifyDedupeSeconds || got.Source != aiNotifyDedupeSourceDefault {
		t.Fatalf("default resolution = %#v", got)
	}

	if err := config.SaveAINotifyDedupeSecondsFile(paths.AINotifyDedupeSecondsFile(), 45); err != nil {
		t.Fatal(err)
	}
	got = resolveAINotifyDedupeSeconds(func() (string, error) { return home, nil }, lookup)
	if got.Seconds != 45 || got.Source != aiNotifyDedupeSourceSetting {
		t.Fatalf("setting resolution = %#v, want 45/setting", got)
	}

	envLookup := func(name string) string {
		switch name {
		case "HOME":
			return home
		case aiNotifyDedupeSecondsEnv:
			return "90"
		default:
			return ""
		}
	}
	got = resolveAINotifyDedupeSeconds(func() (string, error) { return home, nil }, envLookup)
	if got.Seconds != 90 || got.Source != aiNotifyDedupeSourceEnv {
		t.Fatalf("env resolution = %#v, want 90/env", got)
	}
}

func TestAINotifyConfiguredDedupeWindowSuppressesThenAllowsSend(t *testing.T) {
	home := t.TempDir()
	paths := config.DefaultPaths(filepath.Join(home, ".config"), filepath.Join(home, ".local", "state"))
	if err := config.SaveAINotifyDedupeSecondsFile(paths.AINotifyDedupeSecondsFile(), 45); err != nil {
		t.Fatal(err)
	}

	key := "input_required|waiting for input"
	for _, tc := range []struct {
		name           string
		lastAt         string
		wantNotifySend bool
	}{
		{name: "inside configured window", lastAt: "970", wantNotifySend: false},
		{name: "outside configured window", lastAt: "940", wantNotifySend: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := testAICommand(home)
			cmd.now = func() time.Time { return time.Unix(1000, 0) }
			cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == "command" && reflect.DeepEqual(args, []string{"-v", "notify-send"}) {
					return []byte("/usr/bin/notify-send\n"), nil
				}
				if name != "tmux" {
					return nil, os.ErrNotExist
				}
				switch {
				case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{pane_title}"}):
					return []byte("waiting for input\n"), nil
				case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{@projmux_desktop_notification_key}"}):
					return []byte(key + "\n"), nil
				case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{@projmux_desktop_notification_at}"}):
					return []byte(tc.lastAt + "\n"), nil
				}
				return []byte("\n"), nil
			}

			if err := cmd.notifyAI("%3"); err != nil {
				t.Fatalf("notifyAI error = %v", err)
			}
			if got := containsAICommand(cmdRecorder(cmd).commands, "notify-send"); got != tc.wantNotifySend {
				t.Fatalf("notify-send dispatched = %v, want %v; commands = %#v", got, tc.wantNotifySend, cmdRecorder(cmd).commands)
			}
		})
	}
}
