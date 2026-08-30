package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
	"github.com/crevissepartners/projmux/internal/version"
)

func TestShellWelcomeExitGuidanceFallbackAndLocales(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		text       string
		wantPicker string
	}{
		{name: "fallback", text: shellWelcomeExitFallback, wantPicker: "interactive action picker"},
		{name: "en-US", text: localizeText(i18n.FallbackLocale, i18n.KeyWelcomeShellExit, "missing"), wantPicker: "interactive action picker"},
		{name: "ko-KR", text: localizeText(i18n.Locale("ko-KR"), i18n.KeyWelcomeShellExit, "missing"), wantPicker: "대화형 작업 선택기"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := strings.Count(test.text, "projmux quit"); got != 1 {
				t.Fatalf("guidance = %q, projmux quit count = %d, want 1", test.text, got)
			}
			if !strings.Contains(test.text, test.wantPicker) {
				t.Fatalf("guidance = %q, want default interactive picker meaning %q", test.text, test.wantPicker)
			}
			for _, forbidden := range []string{"tmux -L projmux kill-server", "--yes"} {
				if strings.Contains(test.text, forbidden) {
					t.Fatalf("guidance = %q, must not promote %q", test.text, forbidden)
				}
			}

			for _, width := range []int{20, 36, 76} {
				wrapped := wrapWelcomeLine(test.text, width)
				if got := strings.Join(wrapped, " "); got != test.text {
					t.Fatalf("wrapWelcomeLine(%d) = %q, want lossless %q", width, got, test.text)
				}
				for _, line := range wrapped {
					if got := i18n.TerminalCellWidth(line); got > width {
						t.Fatalf("wrapped line width = %d, want <= %d: %q", got, width, line)
					}
					var rendered bytes.Buffer
					if err := writeWelcomeBoxLine(&rendered, line, width); err != nil {
						t.Fatalf("writeWelcomeBoxLine() error = %v", err)
					}
					if got, want := projmuxpicker.VisibleLen(strings.TrimSuffix(rendered.String(), "\n")), width+4; got != want {
						t.Fatalf("rendered row width = %d, want %d: %q", got, want, rendered.String())
					}
				}
			}
		})
	}
}

func TestWelcomeCommandWritesShellGuide(t *testing.T) {
	t.Parallel()

	cmd := newWelcomeCommand(nil)

	var stdout, stderr bytes.Buffer
	if err := cmd.Run(nil, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	assertWelcomeOutput(t, "stdout", stdout.String())
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestWelcomeCommandRejectsPositionalArgs(t *testing.T) {
	t.Parallel()

	cmd := newWelcomeCommand(nil)

	var stdout, stderr bytes.Buffer
	err := cmd.Run([]string{"extra"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("Run(extra) returned nil, want error")
	}
	if !strings.Contains(err.Error(), "does not accept positional arguments") {
		t.Fatalf("err = %v, want positional-arg error", err)
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("stderr = %q, want usage", stderr.String())
	}
}

func TestAppDispatchesWelcomeCommand(t *testing.T) {
	t.Parallel()

	app := New()

	var stdout, stderr bytes.Buffer
	if err := app.Run([]string{"welcome"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run(welcome) error = %v", err)
	}
	assertWelcomeOutput(t, "stdout", stdout.String())
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestWelcomePopupClaimsPendingAttachWelcomeOnce(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := testWelcomePopupCommand(t, home)
	writePendingWelcomeState(t, cmd, true)

	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"--popup"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run(--popup) error = %v", err)
	}
	if len(cmd.runner.(*recordingTmuxRunner).calls) != 1 {
		t.Fatalf("calls = %#v, want one popup call", cmd.runner.(*recordingTmuxRunner).calls)
	}
	state := readWelcomeState(t, cmd)
	if state.PendingAttachWelcome {
		t.Fatalf("state = %+v, want pending_attach_welcome=false", state)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q, want quiet popup helper", stdout.String(), stderr.String())
	}

	if err := cmd.Run([]string{"--popup"}, &stdout, &stderr); err != nil {
		t.Fatalf("second Run(--popup) error = %v", err)
	}
	if len(cmd.runner.(*recordingTmuxRunner).calls) != 1 {
		t.Fatalf("second calls = %#v, want no repeated popup", cmd.runner.(*recordingTmuxRunner).calls)
	}
}

func TestWelcomePopupHonorsEnvSuppression(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := testWelcomePopupCommand(t, home)
	cmd.lookupEnv = func(name string) string {
		if name == "PROJMUX_WELCOME" {
			return "off"
		}
		return ""
	}
	writePendingWelcomeState(t, cmd, true)

	if err := cmd.Run([]string{"--popup"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run(--popup) error = %v", err)
	}
	if calls := cmd.runner.(*recordingTmuxRunner).calls; len(calls) != 0 {
		t.Fatalf("calls = %#v, want no popup when disabled", calls)
	}
	state := readWelcomeState(t, cmd)
	if !state.PendingAttachWelcome {
		t.Fatalf("state = %+v, want pending marker preserved while disabled", state)
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	assertWelcomeOutput(t, "stdout", stdout.String())
}

func TestWelcomePopupForceShowsWithoutPendingState(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := testWelcomePopupCommand(t, home)
	cmd.lookupEnv = func(name string) string {
		if name == "PROJMUX_WELCOME" {
			return "off"
		}
		return ""
	}

	if err := cmd.Run([]string{"--popup", "--force"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run(--popup --force) error = %v", err)
	}
	if calls := cmd.runner.(*recordingTmuxRunner).calls; len(calls) != 1 {
		t.Fatalf("calls = %#v, want forced popup without pending state", calls)
	}
}

func TestWelcomePopupSkipsMissingCorruptAndNonPendingStateQuietly(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := testWelcomePopupCommand(t, home)
	for _, name := range []string{"missing", "corrupt", "not-pending"} {
		t.Run(name, func(t *testing.T) {
			runner := &recordingTmuxRunner{}
			cmd.runner = runner
			path, err := cmd.welcomeStatePath(version.String())
			if err != nil {
				t.Fatal(err)
			}
			_ = os.Remove(path)
			switch name {
			case "corrupt":
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{not-json"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "not-pending":
				writePendingWelcomeState(t, cmd, false)
			}

			var stdout, stderr bytes.Buffer
			if err := cmd.Run([]string{"--popup"}, &stdout, &stderr); err != nil {
				t.Fatalf("Run(--popup) error = %v", err)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("calls = %#v, want no popup", runner.calls)
			}
			if stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("stdout=%q stderr=%q, want quiet no-op", stdout.String(), stderr.String())
			}
		})
	}
}

func TestWelcomePopupDisplaysWelcomePayloadInTmux(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := testWelcomePopupCommand(t, home)
	writePendingWelcomeState(t, cmd, true)

	if err := cmd.Run([]string{"--popup"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run(--popup) error = %v", err)
	}

	calls := cmd.runner.(*recordingTmuxRunner).calls
	if len(calls) != 1 {
		t.Fatalf("calls = %#v, want one popup call", calls)
	}
	call := calls[0]
	if call.name != "tmux" || len(call.args) < 2 || call.args[0] != "display-popup" {
		t.Fatalf("call = %#v, want tmux display-popup", call)
	}
	for _, want := range []string{"-E", "-B", "-w", "-h"} {
		if !slices.Contains(call.args, want) {
			t.Fatalf("display-popup args = %#v, want %s", call.args, want)
		}
	}
	command := call.args[len(call.args)-1]
	assertWelcomeOutput(t, "popup command", command)
	for _, want := range []string{
		displayOnlyPopupClosePrompt,
		"popup-wait-key",
		"'/tmp/proj mux/bin/projmux'",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("popup command = %q, want substring %q", command, want)
		}
	}
}

func assertWelcomeOutput(t *testing.T, name, output string) {
	t.Helper()

	for _, want := range []string{"projmux", version.String()} {
		if !strings.Contains(output, want) {
			t.Fatalf("%s = %q, want substring %q", name, output, want)
		}
	}
	assertNoHardcodedWelcomeLaunchGuide(t, name, output)
}

func assertNoHardcodedWelcomeLaunchGuide(t *testing.T, name, output string) {
	t.Helper()

	for _, unwanted := range []string{"Alt-1", "Alt-3", "Alt-5"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("%s = %q, did not want hardcoded welcome key guide %q", name, output, unwanted)
		}
	}
}

func testWelcomePopupCommand(t *testing.T, home string) *welcomeCommand {
	t.Helper()

	return &welcomeCommand{
		executable: func() (string, error) { return "/tmp/proj mux/bin/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
		readFile:   os.ReadFile,
		removeFile: os.Remove,
		renameFile: os.Rename,
		runner:     &recordingTmuxRunner{},
		writeFile:  os.WriteFile,
	}
}

func writePendingWelcomeState(t *testing.T, cmd *welcomeCommand, pending bool) {
	t.Helper()

	path, err := cmd.welcomeStatePath(version.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	state := shellWelcomeState{
		Version:              welcomeStateVersion,
		LastWelcomedVersion:  version.String(),
		WelcomedAt:           time.Date(2026, 5, 12, 9, 0, 0, 0, time.UTC),
		PendingAttachWelcome: pending,
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readWelcomeState(t *testing.T, cmd *welcomeCommand) shellWelcomeState {
	t.Helper()

	path, err := cmd.welcomeStatePath(version.String())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state shellWelcomeState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}
