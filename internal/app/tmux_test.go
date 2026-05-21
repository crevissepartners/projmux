package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

func TestAppRunTmuxPopupPreviewUsesDefaultOptions(t *testing.T) {
	t.Parallel()

	popup := &stubTmuxPopupClient{}
	app := &App{
		tmux: &tmuxCommand{
			popup: popup,
			executable: func() (string, error) {
				return "/tmp/proj mux/bin/projmux", nil
			},
			popupOptions: defaultPopupPreviewOptions,
		},
	}

	var stdout bytes.Buffer
	if err := app.Run([]string{"tmux", "popup-preview", "dev"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	const wantCommand = "exec '/tmp/proj mux/bin/projmux' 'session-popup' 'preview' 'dev'"
	if popup.command != wantCommand {
		t.Fatalf("popup command = %q, want %q", popup.command, wantCommand)
	}

	wantOptions := inttmux.PopupOptions{
		Width:         "80%",
		Height:        "80%",
		CloseBehavior: inttmux.PopupCloseOnExit,
	}
	if !reflect.DeepEqual(popup.options, wantOptions) {
		t.Fatalf("popup options = %#v, want %#v", popup.options, wantOptions)
	}
}

func TestAppRunTmuxPopupSwitchUsesCurrentPanePathAndDefaultOptions(t *testing.T) {
	t.Parallel()

	popup := &stubTmuxPopupClient{currentPanePath: "/tmp/work tree"}
	app := &App{
		tmux: &tmuxCommand{
			popup:       popup,
			executable:  func() (string, error) { return "/tmp/proj mux/bin/projmux", nil },
			switchPopup: defaultPopupSwitchOptions,
		},
	}

	var stdout bytes.Buffer
	if err := app.Run([]string{"tmux", "popup-switch"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	const wantCommand = "cd -- '/tmp/work tree' && exec '/tmp/proj mux/bin/projmux' 'switch' '--ui=popup'"
	if popup.command != wantCommand {
		t.Fatalf("popup command = %q, want %q", popup.command, wantCommand)
	}

	wantOptions := inttmux.PopupOptions{
		Width:  "80%",
		Height: "70%",
		Env: map[string]string{
			hookTrustInlineEnv:   "1",
			intpicker.BackendEnv: string(intpicker.BackendNative),
		},
		NoBorder:      true,
		CloseBehavior: inttmux.PopupCloseOnExit,
	}
	if !reflect.DeepEqual(popup.options, wantOptions) {
		t.Fatalf("popup options = %#v, want %#v", popup.options, wantOptions)
	}
}

func TestAppRunTmuxPopupSessionsUsesDefaultOptions(t *testing.T) {
	t.Parallel()

	popup := &stubTmuxPopupClient{}
	app := &App{
		tmux: &tmuxCommand{
			popup:         popup,
			executable:    func() (string, error) { return "/tmp/proj mux/bin/projmux", nil },
			sessionsPopup: defaultPopupSessionsOptions,
		},
	}

	var stdout bytes.Buffer
	if err := app.Run([]string{"tmux", "popup-sessions"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	const wantCommand = "exec '/tmp/proj mux/bin/projmux' 'sessions' '--ui=popup'"
	if popup.command != wantCommand {
		t.Fatalf("popup command = %q, want %q", popup.command, wantCommand)
	}

	wantOptions := inttmux.PopupOptions{
		Width:         "80%",
		Height:        "75%",
		Env:           map[string]string{intpicker.BackendEnv: string(intpicker.BackendNative)},
		NoBorder:      true,
		CloseBehavior: inttmux.PopupCloseOnExit,
	}
	if !reflect.DeepEqual(popup.options, wantOptions) {
		t.Fatalf("popup options = %#v, want %#v", popup.options, wantOptions)
	}
}

func TestAppRunTmuxPopupSwitchAndSessionsUseSavedNativeBackendChrome(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	saveNativePickerBackend(t, home)

	tests := []struct {
		name    string
		args    []string
		popup   *stubTmuxPopupClient
		command string
	}{
		{
			name:    "switch",
			args:    []string{"tmux", "popup-switch"},
			popup:   &stubTmuxPopupClient{currentPanePath: "/tmp/work tree"},
			command: "cd -- '/tmp/work tree' && exec '/tmp/projmux' 'switch' '--ui=popup'",
		},
		{
			name:    "sessions",
			args:    []string{"tmux", "popup-sessions"},
			popup:   &stubTmuxPopupClient{},
			command: "exec '/tmp/projmux' 'sessions' '--ui=popup'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app := &App{
				tmux: &tmuxCommand{
					popup:      tt.popup,
					executable: func() (string, error) { return "/tmp/projmux", nil },
					homeDir:    func() (string, error) { return home, nil },
					lookupEnv:  func(string) string { return "" },
				},
			}

			if err := app.Run(tt.args, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if tt.popup.command != tt.command {
				t.Fatalf("popup command = %q, want %q", tt.popup.command, tt.command)
			}
			if !tt.popup.options.NoBorder {
				t.Fatalf("popup options = %#v, want NoBorder for saved native backend", tt.popup.options)
			}
			if got := tt.popup.options.Env[intpicker.BackendEnv]; got != string(intpicker.BackendNative) {
				t.Fatalf("popup backend env = %q, want native", got)
			}
		})
	}
}

func TestAppRunTmuxPopupCommandsUseMinimumReadableSizes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		args        []string
		popup       *stubTmuxPopupClient
		wantOptions inttmux.PopupOptions
	}{
		{
			name:  "preview",
			args:  []string{"tmux", "popup-preview", "dev"},
			popup: &stubTmuxPopupClient{},
			wantOptions: inttmux.PopupOptions{
				Width:         "120",
				Height:        "30",
				CloseBehavior: inttmux.PopupCloseOnExit,
			},
		},
		{
			name:  "switch",
			args:  []string{"tmux", "popup-switch"},
			popup: &stubTmuxPopupClient{currentPanePath: "/tmp/work tree"},
			wantOptions: inttmux.PopupOptions{
				Width:  "120",
				Height: "28",
				Env: map[string]string{
					hookTrustInlineEnv:   "1",
					intpicker.BackendEnv: string(intpicker.BackendNative),
				},
				NoBorder:      true,
				CloseBehavior: inttmux.PopupCloseOnExit,
			},
		},
		{
			name:  "sessions",
			args:  []string{"tmux", "popup-sessions"},
			popup: &stubTmuxPopupClient{},
			wantOptions: inttmux.PopupOptions{
				Width:         "120",
				Height:        "28",
				Env:           map[string]string{intpicker.BackendEnv: string(intpicker.BackendNative)},
				NoBorder:      true,
				CloseBehavior: inttmux.PopupCloseOnExit,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app := &App{
				tmux: &tmuxCommand{
					popup:      tt.popup,
					executable: func() (string, error) { return "/tmp/projmux", nil },
					runner: &recordingTmuxRunner{formats: map[string]string{
						"#{client_tty}":    "/dev/pts/7",
						"#{pane_id}":       "%9",
						"#S":               "dev",
						"#{client_width}":  "140",
						"#{client_height}": "36",
					}},
				},
			}

			if err := app.Run(tt.args, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if !reflect.DeepEqual(tt.popup.options, tt.wantOptions) {
				t.Fatalf("popup options = %#v, want %#v", tt.popup.options, tt.wantOptions)
			}
		})
	}
}

func TestAppRunTmuxPopupToggleOpensStandaloneSidebar(t *testing.T) {
	t.Parallel()

	marker := popupMarkerPath(sanitizePopupKey("/dev/pts/projmux-test-sidebar"), "sessionizer-sidebar")
	_ = os.Remove(marker)
	defer os.Remove(marker)

	runner := &recordingTmuxRunner{formats: map[string]string{
		"#{client_tty}":        "/dev/pts/projmux-test-sidebar",
		"#{pane_id}":           "%1",
		"#S":                   "work",
		"#{pane_current_path}": "/tmp/work tree",
		"#{client_width}":      "200",
		"#{client_height}":     "50",
	}}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/proj mux/bin/projmux", nil },
		homeDir:    func() (string, error) { return t.TempDir(), nil },
		lookupEnv:  func(string) string { return "" },
	}

	if err := cmd.Run([]string{"popup-toggle", "sessionizer-sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := runner.calls[len(runner.calls)-1]
	wantPrefix := []string{
		"display-popup",
		"-t", "%1",
		"-E",
		"-B",
		"-d", "/tmp/work tree",
		"-e", "PROJMUX_HOOK_TRUST_TARGET_CLIENT=/dev/pts/projmux-test-sidebar",
		"-e", "PROJMUX_NATIVE_LAUNCH_KEY=alt-1",
		"-e", "PROJMUX_PICKER_BACKEND=native",
		"-e", "PROJMUX_SWITCH_TARGET_CLIENT=/dev/pts/projmux-test-sidebar",
		"-e", "TMUX_SESSIONIZER_CONTEXT_DIR=/tmp/work tree",
		"-e", "TMUX_SESSIONIZER_CONTEXT_PANE=%1",
		"-e", "TMUX_SESSIONIZER_CONTEXT_SESSION=work",
		"-x", "0",
		"-y", "0",
		"-w", "40",
		"-h", "48",
	}
	if got.name != "tmux" || len(got.args) < len(wantPrefix)+1 || !reflect.DeepEqual(got.args[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("display call = %#v, want prefix %#v", got, wantPrefix)
	}
	command := got.args[len(got.args)-1]
	for _, want := range []string{
		"cd -- '/tmp/work tree'",
		"PROJMUX_HOOK_TRUST_TARGET_CLIENT='/dev/pts/projmux-test-sidebar'",
		"PROJMUX_SWITCH_TARGET_CLIENT='/dev/pts/projmux-test-sidebar'",
		"TMUX_SESSIONIZER_CONTEXT_SESSION='work'",
		"TMUX_SESSIONIZER_CONTEXT_PANE='%1'",
		"'/tmp/proj mux/bin/projmux' 'switch' '--ui=sidebar'",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("popup command = %q, want substring %q", command, want)
		}
	}
	for _, unwanted := range []string{
		"PROJMUX_HOOK_TRUST_INLINE",
		"PROJMUX_HOOK_TRUST_TARGET_PANE",
	} {
		if strings.Contains(command, unwanted) {
			t.Fatalf("popup command = %q, unwanted substring %q", command, unwanted)
		}
	}
	content, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker error = %v", err)
	}
	if got, want := string(content), "%1\n"; got != want {
		t.Fatalf("marker content = %q, want %q", got, want)
	}
}

func TestAppRunTmuxPopupToggleUsesBorderlessPopupForNativeBackend(t *testing.T) {
	t.Setenv("PROJMUX_PICKER_BACKEND", "native")

	marker := popupMarkerPath(sanitizePopupKey("/dev/pts/projmux-test-native-border"), "sessionizer-sidebar")
	_ = os.Remove(marker)
	defer os.Remove(marker)

	runner := &recordingTmuxRunner{formats: map[string]string{
		"#{client_tty}":        "/dev/pts/projmux-test-native-border",
		"#{pane_id}":           "%1",
		"#S":                   "work",
		"#{pane_current_path}": "/tmp/work tree",
		"#{client_width}":      "200",
		"#{client_height}":     "50",
	}}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
	}

	if err := cmd.Run([]string{"popup-toggle", "sessionizer-sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := runner.calls[len(runner.calls)-1]
	if !slices.Contains(got.args, "-B") {
		t.Fatalf("display call = %#v, want -B for native backend popup so only native picker draws the frame", got)
	}
	if !containsTmuxArgPair(got.args, "-e", "PROJMUX_PICKER_BACKEND=native") {
		t.Fatalf("display call = %#v, want native backend env propagated", got)
	}
	if !containsTmuxArgPair(got.args, "-w", "40") {
		t.Fatalf("display call = %#v, want native sidebar to keep compact responsive width", got)
	}
}

func TestSessionizerSidebarWidthUsesNativeCompactMinimum(t *testing.T) {
	t.Parallel()

	if got, want := sessionizerSidebarWidth(200, intpicker.BackendNative), "40"; got != want {
		t.Fatalf("sessionizerSidebarWidth(native) = %q, want compact %q", got, want)
	}
	if got, want := sessionizerSidebarWidth(0, intpicker.BackendNative), "20%"; got != want {
		t.Fatalf("sessionizerSidebarWidth(native unknown client) = %q, want %q", got, want)
	}
}

func TestNotifySidebarWidthMatchesFZFBaseline(t *testing.T) {
	t.Parallel()

	if got, want := notifySidebarWidth(200), "64"; got != want {
		t.Fatalf("notifySidebarWidth(200) = %q, want fzf minimum %q", got, want)
	}
	if got, want := notifySidebarWidth(300), "72"; got != want {
		t.Fatalf("notifySidebarWidth(300) = %q, want fzf percent %q", got, want)
	}
	if got, want := notifySidebarWidth(0), "24%"; got != want {
		t.Fatalf("notifySidebarWidth(unknown) = %q, want fzf percent fallback %q", got, want)
	}
}

func TestSidebarPopupHeightLeavesStatusbarRows(t *testing.T) {
	t.Parallel()

	if got, want := sidebarPopupHeight(50), "48"; got != want {
		t.Fatalf("sidebarPopupHeight(50) = %q, want %q", got, want)
	}
	if got, want := sidebarPopupHeight(1), "1"; got != want {
		t.Fatalf("sidebarPopupHeight(1) = %q, want minimum %q", got, want)
	}
	if got, want := sidebarPopupHeight(0), "100%"; got != want {
		t.Fatalf("sidebarPopupHeight(unknown) = %q, want fallback %q", got, want)
	}
}

func TestAppRunTmuxPopupToggleUsesSavedNativeBackendForPopupChrome(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	saveNativePickerBackend(t, home)

	marker := popupMarkerPath(sanitizePopupKey("/dev/pts/projmux-test-saved-native-border"), "sessionizer-sidebar")
	_ = os.Remove(marker)
	defer os.Remove(marker)

	runner := &recordingTmuxRunner{formats: map[string]string{
		"#{client_tty}":        "/dev/pts/projmux-test-saved-native-border",
		"#{pane_id}":           "%1",
		"#S":                   "work",
		"#{pane_current_path}": "/tmp/work tree",
		"#{client_width}":      "200",
		"#{client_height}":     "50",
	}}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
	}

	if err := cmd.Run([]string{"popup-toggle", "sessionizer-sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := runner.calls[len(runner.calls)-1]
	if !slices.Contains(got.args, "-B") {
		t.Fatalf("display call = %#v, want -B for saved native backend popup", got)
	}
	if !containsTmuxArgPair(got.args, "-e", "PROJMUX_PICKER_BACKEND=native") {
		t.Fatalf("display call = %#v, want native backend env for child", got)
	}
	command := got.args[len(got.args)-1]
	if !strings.Contains(command, "PROJMUX_PICKER_BACKEND='native'") {
		t.Fatalf("popup command = %q, want native backend env assignment", command)
	}
}

func TestAppRunTmuxPopupToggleKeepsNotifySidebarFZFSizingForNative(t *testing.T) {
	t.Setenv("PROJMUX_PICKER_BACKEND", "native")

	clientKey := "/dev/pts/projmux-test-native-notify"
	marker := popupMarkerPath(sanitizePopupKey(clientKey), "notify-sidebar")
	_ = os.Remove(marker)
	defer os.Remove(marker)

	runner := &recordingTmuxRunner{formats: map[string]string{
		"#{client_tty}":    clientKey,
		"#{pane_id}":       "%1",
		"#{client_width}":  "200",
		"#{client_height}": "50",
	}}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
	}

	if err := cmd.Run([]string{"popup-toggle", "notify-sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := runner.calls[len(runner.calls)-1]
	if !slices.Contains(got.args, "-B") {
		t.Fatalf("display call = %#v, want native notify popup to keep native-owned frame", got)
	}
	if !containsTmuxArgPair(got.args, "-w", "64") {
		t.Fatalf("display call = %#v, want native notify sidebar to keep fzf baseline width", got)
	}
	if !containsTmuxArgPair(got.args, "-x", "136") {
		t.Fatalf("display call = %#v, want right edge position based on fzf baseline width", got)
	}
	if !containsTmuxArgPair(got.args, "-h", "48") {
		t.Fatalf("display call = %#v, want native notify sidebar to leave statusbar rows uncovered", got)
	}
}

func TestAppRunTmuxPopupToggleOpensNotifySidebarOnRight(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		decoration string
	}{
		{name: "off", decoration: "off"},
		{name: "symbol", decoration: "symbol"},
		{name: "emoji", decoration: "emoji"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clientKey := "/dev/pts/projmux-test-notify-" + tt.name
			marker := popupMarkerPath(sanitizePopupKey(clientKey), "notify-sidebar")
			_ = os.Remove(marker)
			defer os.Remove(marker)

			runner := &recordingTmuxRunner{formats: map[string]string{
				"#{client_tty}":                    clientKey,
				"#{pane_id}":                       "%1",
				"#S":                               "work",
				"#{client_width}":                  "200",
				"#{client_height}":                 "50",
				"#{@projmux_statusbar_decoration}": tt.decoration,
			}}
			cmd := &tmuxCommand{
				runner:     runner,
				executable: func() (string, error) { return "/tmp/proj mux/bin/projmux", nil },
			}

			if err := cmd.Run([]string{"popup-toggle", "notify-sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run() error = %v", err)
			}

			got := runner.calls[len(runner.calls)-1]
			wantPrefix := []string{
				"display-popup",
				"-c", clientKey,
				"-E",
				"-B",
				"-e", "PROJMUX_NATIVE_LAUNCH_KEY=alt-2",
				"-e", "PROJMUX_PICKER_BACKEND=native",
				"-x", "136",
				"-y", "0",
				"-w", "64",
				"-h", "48",
			}
			if got.name != "tmux" || len(got.args) < len(wantPrefix)+1 || !reflect.DeepEqual(got.args[:len(wantPrefix)], wantPrefix) {
				t.Fatalf("display call = %#v, want prefix %#v", got, wantPrefix)
			}
			if slices.Contains(got.args, "-T") {
				t.Fatalf("display call = %#v, want no title option", got)
			}
			command := got.args[len(got.args)-1]
			if !strings.Contains(command, "'/tmp/proj mux/bin/projmux' 'notify' 'list' '--ui=sidebar' '--client' '"+clientKey+"'") {
				t.Fatalf("popup command = %q, want notify sidebar command", command)
			}
		})
	}
}

func TestAppRunTmuxPopupToggleClosesNotifySidebarWithMarkerPane(t *testing.T) {
	t.Parallel()

	clientKey := "/dev/pts/projmux-test-notify-close"
	marker := popupMarkerPath(sanitizePopupKey(clientKey), "notify-sidebar")
	_ = os.Remove(marker)
	defer os.Remove(marker)
	if err := os.WriteFile(marker, []byte("%original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingTmuxRunner{formats: map[string]string{
		"#{client_tty}": clientKey,
		"#{pane_id}":    "%active",
	}}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
	}

	if err := cmd.Run([]string{"popup-toggle", "--client", clientKey, "notify-sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := runner.calls[len(runner.calls)-1]
	want := recordedTmuxCall{name: "tmux", args: []string{"display-popup", "-c", clientKey, "-C"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("close call = %#v, want %#v", got, want)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker stat error = %v, want not exist", err)
	}
}

func TestPopupRightXFallsBackWhenClientWidthUnknown(t *testing.T) {
	t.Parallel()

	if got, want := popupRightX(0, "64"), "R"; got != want {
		t.Fatalf("popupRightX = %q, want %q", got, want)
	}
	if got, want := popupRightX(200, "64"), "136"; got != want {
		t.Fatalf("popupRightX = %q, want %q", got, want)
	}
}

func TestAppRunTmuxPopupToggleOpensSettingsHub(t *testing.T) {
	t.Parallel()

	marker := popupMarkerPath(sanitizePopupKey("/dev/pts/projmux-test-settings"), "ai-split-settings")
	_ = os.Remove(marker)
	defer os.Remove(marker)

	runner := &recordingTmuxRunner{formats: map[string]string{
		"#{client_tty}":    "/dev/pts/projmux-test-settings",
		"#{pane_id}":       "%1",
		"#{client_width}":  "200",
		"#{client_height}": "50",
	}}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
	}

	if err := cmd.Run([]string{"popup-toggle", "ai-split-settings"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := runner.calls[len(runner.calls)-1]
	if !containsTmuxArgPair(got.args, "-e", "PROJMUX_NATIVE_LAUNCH_KEY=alt-5") {
		t.Fatalf("display call = %#v, want native launch key env", got)
	}
	command := got.args[len(got.args)-1]
	if !strings.Contains(command, "'/tmp/projmux' 'settings'") {
		t.Fatalf("popup command = %q, want settings command", command)
	}
	if strings.Contains(command, "'ai' 'settings'") {
		t.Fatalf("popup command = %q, want unified settings hub", command)
	}
}

func TestAppRunTmuxPopupToggleOpensWideAIPicker(t *testing.T) {
	t.Parallel()

	marker := popupMarkerPath(sanitizePopupKey("/dev/pts/projmux-test-ai-picker"), "ai-split-picker")
	_ = os.Remove(marker)
	defer os.Remove(marker)

	runner := &recordingTmuxRunner{formats: map[string]string{
		"#{client_tty}":        "/dev/pts/projmux-test-ai-picker",
		"#{pane_id}":           "%1",
		"#{pane_current_path}": "/tmp/work tree",
		"#{client_width}":      "200",
		"#{client_height}":     "50",
	}}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
	}

	if err := cmd.Run([]string{"popup-toggle", "ai-split-picker-right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := runner.calls[len(runner.calls)-1]
	wantPrefix := []string{
		"display-popup",
		"-t", "%1",
		"-E",
		"-B",
		"-d", "/tmp/work tree",
		"-e", "PROJMUX_NATIVE_LAUNCH_KEY=alt-4",
		"-e", "PROJMUX_PICKER_BACKEND=native",
		"-e", "TMUX_SPLIT_CONTEXT_DIR=/tmp/work tree",
		"-e", "TMUX_SPLIT_TARGET_PANE=%1",
		"-w", "96",
		"-h", "22",
	}
	if got.name != "tmux" || len(got.args) < len(wantPrefix)+1 || !reflect.DeepEqual(got.args[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("display call = %#v, want prefix %#v", got, wantPrefix)
	}
	command := got.args[len(got.args)-1]
	if !strings.Contains(command, "'/tmp/projmux' 'ai' 'picker' '--inside' 'right'") {
		t.Fatalf("popup command = %q, want ai picker command", command)
	}
}

func TestAppRunTmuxPopupToggleClosesExistingMarkerWithClientOverride(t *testing.T) {
	t.Parallel()

	clientKey := "/dev/pts/original-client"
	marker := popupMarkerPath(sanitizePopupKey(clientKey), "ai-split-picker")
	_ = os.Remove(marker)
	defer os.Remove(marker)
	if err := os.WriteFile(marker, []byte("%original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingTmuxRunner{formats: map[string]string{
		"#{client_tty}": "/dev/pts/popup-client",
		"#{pane_id}":    "%popup",
	}}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
	}

	if err := cmd.Run([]string{"popup-toggle", "--client", clientKey, "ai-split-picker-right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := runner.calls[len(runner.calls)-1]
	want := recordedTmuxCall{name: "tmux", args: []string{"display-popup", "-t", "%original", "-C"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("close call = %#v, want %#v", got, want)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker stat error = %v, want not exist", err)
	}
}

func TestAppRunTmuxPopupToggleRecoversStaleSettingsMarkerAndReopens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{name: "exit status one", err: errors.New("tmux display-popup -t %20 -C: exit status 1")},
		{name: "target not found", err: errors.New("tmux display-popup -t %20 -C: can't find pane: %20")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clientKey := "/dev/pts/projmux-test-stale-settings-" + sanitizePopupKey(tt.name)
			marker := popupMarkerPath(sanitizePopupKey(clientKey), "ai-split-settings")
			_ = os.Remove(marker)
			defer os.Remove(marker)
			if err := os.WriteFile(marker, []byte("%20\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runner := &recordingTmuxRunner{
				formats: map[string]string{
					"#{client_tty}":    clientKey,
					"#{pane_id}":       "%active",
					"#{client_width}":  "200",
					"#{client_height}": "50",
				},
				errors: map[string]error{
					recordedTmuxCallKey("tmux", "display-popup", "-t", "%20", "-C"): tt.err,
				},
			}
			cmd := &tmuxCommand{
				runner:     runner,
				executable: func() (string, error) { return "/tmp/projmux", nil },
			}

			if err := cmd.Run([]string{"popup-toggle", "--client", clientKey, "ai-split-settings"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run() error = %v, want nil", err)
			}

			closeIdx := -1
			openIdx := -1
			for i, call := range runner.calls {
				if reflect.DeepEqual(call, recordedTmuxCall{name: "tmux", args: []string{"display-popup", "-t", "%20", "-C"}}) {
					closeIdx = i
					continue
				}
				if call.name == "tmux" && len(call.args) > 0 && call.args[0] == "display-popup" && !slices.Contains(call.args, "-C") {
					openIdx = i
				}
			}
			if closeIdx < 0 {
				t.Fatalf("tmux calls = %#v, want stale close attempt", runner.calls)
			}
			if openIdx <= closeIdx {
				t.Fatalf("tmux calls = %#v, want open display-popup after stale close recovery", runner.calls)
			}
			content, err := os.ReadFile(marker)
			if err != nil {
				t.Fatalf("read marker error = %v", err)
			}
			if got, want := string(content), "%active\n"; got != want {
				t.Fatalf("marker content = %q, want recovered current pane marker %q", got, want)
			}
		})
	}
}

func TestAppRunTmuxPopupToggleKeepsGenuineCloseFailure(t *testing.T) {
	t.Parallel()

	clientKey := "/dev/pts/projmux-test-settings-close-failure"
	marker := popupMarkerPath(sanitizePopupKey(clientKey), "ai-split-settings")
	_ = os.Remove(marker)
	defer os.Remove(marker)
	if err := os.WriteFile(marker, []byte("%20\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingTmuxRunner{
		formats: map[string]string{
			"#{client_tty}": clientKey,
			"#{pane_id}":    "%active",
		},
		errors: map[string]error{
			recordedTmuxCallKey("tmux", "display-popup", "-t", "%20", "-C"): errors.New("tmux display-popup -t %20 -C: exit status 2: bad command"),
		},
	}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
	}

	if err := cmd.Run([]string{"popup-toggle", "--client", clientKey, "ai-split-settings"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("Run() error = nil, want close failure")
	}
	for _, call := range runner.calls {
		if call.name == "tmux" && len(call.args) > 0 && call.args[0] == "display-popup" && !slices.Contains(call.args, "-C") {
			t.Fatalf("tmux calls = %#v, want no reopen for genuine close failure", runner.calls)
		}
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "%20\n" {
		t.Fatalf("marker content = %q, err = %v; want stale marker preserved on genuine close failure", string(content), err)
	}
}

func TestAppRunTmuxPopupToggleTreatsClosedPopupAsNoOp(t *testing.T) {
	t.Parallel()

	runner := &recordingTmuxRunner{
		formats: map[string]string{
			"#{client_tty}": "/dev/pts/projmux-test-close",
		},
		err: errors.New("tmux display-popup: exit status 129"),
	}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
	}

	if err := cmd.Run([]string{"popup-toggle", "session-popup"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
}

func TestNativeLaunchKeyForPopupMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mode string
		want string
	}{
		{mode: "sessionizer-sidebar", want: "alt-1"},
		{mode: "notify-sidebar", want: "alt-2"},
		{mode: "session-popup", want: "alt-3"},
		{mode: "ai-split-picker-right", want: "alt-4"},
		{mode: "ai-split-picker-down", want: "alt-4"},
		{mode: "ai-split-settings", want: "alt-5"},
		{mode: "sessionizer", want: "alt-6"},
		{mode: "unknown", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			t.Parallel()

			if got := nativeLaunchKeyForPopupMode(tt.mode); got != tt.want {
				t.Fatalf("nativeLaunchKeyForPopupMode(%q) = %q, want %q", tt.mode, got, tt.want)
			}
		})
	}
}

func TestBuildPopupTogglePropagatesPickerEnvironment(t *testing.T) {
	t.Setenv("PROJMUX_PICKER_BACKEND", "native")
	t.Setenv("PROJMUX_NATIVE_DEBUG_LOG", "/tmp/projmux-popup.log")
	t.Setenv("PROJMUX_NATIVE_TTY_FALLBACK", "0")
	t.Setenv("PROJMUX_PROJDIR", "/workspace/projects")
	t.Setenv("PROJMUX_MANAGED_ROOTS", "/workspace/projects")

	command, options, err := buildPopupToggle(
		tmuxPopupToggleMode{Raw: "sessionizer-sidebar", Canonical: "sessionizer-sidebar"},
		"/tmp/projmux",
		"/tmp/marker",
		tmuxPopupContext{
			OriginPane:    "%1",
			TargetClient:  "/dev/pts/7",
			OriginSession: "main",
			ContextDir:    "/workspace/projects/alpha-api",
			ClientWidth:   200,
			ClientHeight:  50,
		},
	)
	if err != nil {
		t.Fatalf("buildPopupToggle() error = %v", err)
	}
	for _, want := range []string{
		"PROJMUX_PICKER_BACKEND='native'",
		"PROJMUX_NATIVE_DEBUG_LOG='/tmp/projmux-popup.log'",
		"PROJMUX_NATIVE_TTY_FALLBACK='0'",
		"PROJMUX_PROJDIR='/workspace/projects'",
		"PROJMUX_MANAGED_ROOTS='/workspace/projects'",
		hookTrustPopupTargetClientEnv + "='/dev/pts/7'",
		inttmux.SwitchTargetClientEnv + "='/dev/pts/7'",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("popup command = %q, want substring %q", command, want)
		}
	}
	for _, unwanted := range []string{hookTrustPopupTargetPaneEnv} {
		if strings.Contains(command, unwanted) {
			t.Fatalf("popup command = %q, unwanted substring %q", command, unwanted)
		}
	}
	for key, want := range map[string]string{
		"PROJMUX_PICKER_BACKEND":      "native",
		"PROJMUX_NATIVE_DEBUG_LOG":    "/tmp/projmux-popup.log",
		"PROJMUX_NATIVE_TTY_FALLBACK": "0",
		"PROJMUX_PROJDIR":             "/workspace/projects",
		"PROJMUX_MANAGED_ROOTS":       "/workspace/projects",
		hookTrustPopupTargetClientEnv: "/dev/pts/7",
		inttmux.SwitchTargetClientEnv: "/dev/pts/7",
	} {
		if got := options.Env[key]; got != want {
			t.Fatalf("options.Env[%q] = %q, want %q", key, got, want)
		}
	}
	for _, key := range []string{hookTrustInlineEnv, hookTrustPopupTargetPaneEnv} {
		if value, ok := options.Env[key]; ok {
			t.Fatalf("options.Env[%q] = %q, want absent", key, value)
		}
	}
}

func TestBuildPopupToggleSessionizerTrustEnvUsesClientOnly(t *testing.T) {
	command, options, err := buildPopupToggleWithPickerBackend(
		tmuxPopupToggleMode{Raw: "sessionizer", Canonical: "sessionizer"},
		"/tmp/projmux",
		"/tmp/marker",
		tmuxPopupContext{
			TargetClient:  "/dev/pts/8",
			OriginPane:    "%2",
			OriginSession: "main",
			ContextDir:    "/workspace/projects/alpha-api",
			ClientWidth:   200,
			ClientHeight:  50,
		},
		intpicker.Backend("legacy"),
		func(string) string { return "" },
	)
	if err != nil {
		t.Fatalf("buildPopupToggleWithPickerBackend() error = %v", err)
	}
	if !strings.Contains(command, hookTrustPopupTargetClientEnv+"='/dev/pts/8'") {
		t.Fatalf("popup command = %q, want target client env", command)
	}
	if !strings.Contains(command, inttmux.SwitchTargetClientEnv+"='/dev/pts/8'") {
		t.Fatalf("popup command = %q, want switch target client env", command)
	}
	if !strings.Contains(command, hookTrustInlineEnv+"='1'") {
		t.Fatalf("popup command = %q, want inline trust env", command)
	}
	for _, unwanted := range []string{hookTrustPopupTargetPaneEnv} {
		if strings.Contains(command, unwanted) {
			t.Fatalf("popup command = %q, unwanted substring %q", command, unwanted)
		}
		if value, ok := options.Env[unwanted]; ok {
			t.Fatalf("options.Env[%q] = %q, want absent", unwanted, value)
		}
	}
	if got := options.Env[hookTrustPopupTargetClientEnv]; got != "/dev/pts/8" {
		t.Fatalf("options.Env[%q] = %q, want target client", hookTrustPopupTargetClientEnv, got)
	}
	if got := options.Env[inttmux.SwitchTargetClientEnv]; got != "/dev/pts/8" {
		t.Fatalf("options.Env[%q] = %q, want target client", inttmux.SwitchTargetClientEnv, got)
	}
	if got := options.Env[hookTrustInlineEnv]; got != "1" {
		t.Fatalf("options.Env[%q] = %q, want inline trust", hookTrustInlineEnv, got)
	}
}

func TestBuildPopupToggleSessionPopupPropagatesSwitchTargetClient(t *testing.T) {
	command, options, err := buildPopupToggleWithPickerBackend(
		tmuxPopupToggleMode{Raw: "session-popup", Canonical: "session-popup"},
		"/tmp/projmux",
		"/tmp/marker",
		tmuxPopupContext{
			TargetClient: "/dev/pts/8",
			OriginPane:   "%2",
			ClientWidth:  200,
			ClientHeight: 50,
		},
		intpicker.Backend("legacy"),
		func(string) string { return "" },
	)
	if err != nil {
		t.Fatalf("buildPopupToggleWithPickerBackend() error = %v", err)
	}
	if !strings.Contains(command, inttmux.SwitchTargetClientEnv+"='/dev/pts/8'") {
		t.Fatalf("popup command = %q, want switch target client env", command)
	}
	if got := options.Env[inttmux.SwitchTargetClientEnv]; got != "/dev/pts/8" {
		t.Fatalf("options.Env[%q] = %q, want target client", inttmux.SwitchTargetClientEnv, got)
	}
}

func TestAppRunTmuxPopupTogglePropagatesLegacySessionizerRoots(t *testing.T) {
	t.Parallel()

	marker := popupMarkerPath(sanitizePopupKey("/dev/pts/projmux-test-legacy-roots"), "sessionizer-sidebar")
	_ = os.Remove(marker)
	defer os.Remove(marker)

	runner := &recordingTmuxRunner{formats: map[string]string{
		"#{client_tty}":        "/dev/pts/projmux-test-legacy-roots",
		"#{pane_id}":           "%1",
		"#S":                   "work",
		"#{pane_current_path}": "/tmp/work tree",
		"#{client_width}":      "200",
		"#{client_height}":     "50",
	}}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
		lookupEnv: func(name string) string {
			if name == "TMUX_SESSIONIZER_ROOTS" {
				return "/legacy/projects"
			}
			return ""
		},
	}

	if err := cmd.Run([]string{"popup-toggle", "sessionizer-sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := runner.calls[len(runner.calls)-1]
	if !containsTmuxArgPair(got.args, "-e", "TMUX_SESSIONIZER_ROOTS=/legacy/projects") {
		t.Fatalf("display call = %#v, want legacy roots env propagated", got)
	}
	command := got.args[len(got.args)-1]
	if !strings.Contains(command, "TMUX_SESSIONIZER_ROOTS='/legacy/projects'") {
		t.Fatalf("popup command = %q, want legacy roots env assignment", command)
	}
}

func TestTmuxPrintConfigUsesStandaloneBindings(t *testing.T) {
	t.Parallel()

	cmd := &tmuxCommand{executable: func() (string, error) { return "/tmp/proj mux/bin/projmux", nil }}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := stdout.String()
	for _, want := range []string{
		"bind-key -n M-1 run-shell",
		"'/tmp/proj mux/bin/projmux' tmux popup-toggle --client #{client_tty} sessionizer-sidebar",
		"bind-key -n M-2 run-shell",
		"'/tmp/proj mux/bin/projmux' tmux popup-toggle --client #{client_tty} notify-sidebar",
		"bind-key -n M-3 run-shell",
		"'/tmp/proj mux/bin/projmux' tmux popup-toggle --client #{client_tty} session-popup",
		"unbind-key -q R",
		"set-hook -g pane-focus-out",
		"'/tmp/proj mux/bin/projmux' attention arm #{hook_pane}",
		"set-hook -g pane-focus-in",
		"'/tmp/proj mux/bin/projmux' attention clear #{hook_pane}",
		"set-hook -g after-select-pane",
		"'/tmp/proj mux/bin/projmux' attention clear #{pane_id}",
		"set-hook -g pane-exited",
		"sleep 0.05; '/tmp/proj mux/bin/projmux' tmux rebalance-panes",
		"set-hook -g after-kill-pane",
		"'/tmp/proj mux/bin/projmux' attention window #{window_id}",
		"#[bold,fg=colour230,bg=colour31]#[range=user|settings]  projmux #[norange]#[default]",
		"'/tmp/proj mux/bin/projmux' status kube",
		"'/tmp/proj mux/bin/projmux' status git",
		"set -g @projmux_statusbar_decoration off",
		"set -g @projmux_statusbar_decoration_cwd off",
		"set -g @projmux_statusbar_decoration_git off",
		"set -g @projmux_statusbar_decoration_notify off",
		"set -g status 2",
		"set -g status-left-length 20",
		"#[range=user|session][#S] #[norange] ",
		"#{n:window_name}",
		"#{=/7/...:window_name}",
		"@projmux_statusbar_decoration_cwd",
		"#[fg=colour220] ",
		"#[fg=colour220]📁 ",
		"#[fg=colour250]#{=-28/...:pane_current_path}#[norange]",
		" %Y-%m-%d %H:%M",
		"range=user|notify",
		"range=user|usage",
		"set -g status-format[0]",
		"set -g status-format[1]",
		"#[align=left range=user|notify]#('/tmp/proj mux/bin/projmux' status notify --max-width 80)#[norange]#[align=right range=user|usage]#('/tmp/proj mux/bin/projmux' status usage --max-width 120)#[norange]",
		"align=left",
		"align=right",
		"set -gu status-format[2]",
		"unbind-key -q -n M-6",
		"unbind-key -q -n C-n",
		"unbind-key -q -n M-r",
		"unbind-key -q -n User11",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("print-config output = %q, want substring %q", output, want)
		}
	}
	for _, banned := range []string{
		"set -g status 3",
		"set -g status-format[2] \"",
		"tmux autosave-session-state --quiet",
		"bind-key R command-prompt",
		"set -s user-keys",
		"bind-key -n User",
		"\\033[900",
		"\\033[901",
		"range=user|sessionstate",
		"statusbar click sessionstate",
	} {
		if strings.Contains(output, banned) {
			t.Fatalf("print-config output = %q, did not expect substring %q", output, banned)
		}
	}
}

func TestTmuxPrintConfigMissingKeymapKeepsDefaultOutput(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
		readFile:   os.ReadFile,
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := tmuxStandaloneConfig("/tmp/projmux", loadStatusbarDecoration(cmd.homeDir, cmd.lookupEnv))
	if got := stdout.String(); got != want {
		t.Fatalf("print-config output changed without keymap\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestTmuxPrintConfigNilHomeDirIgnoresRelativeKeymap(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	keymapPath := filepath.Join(tmp, ".config", "projmux", "keymap.toml")
	if err := os.MkdirAll(filepath.Dir(keymapPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keymapPath, []byte("[bindings.sessionizer-sidebar]\nplain = \"M-a\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		readFile:   os.ReadFile,
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "bind-key -n M-1 run-shell") {
		t.Fatalf("print-config output = %q, want default M-1 binding", output)
	}
	if strings.Contains(output, "bind-key -n M-a run-shell") {
		t.Fatalf("print-config output = %q, did not expect relative keymap override", output)
	}
}

func TestTmuxPrintConfigKeymapOverrideChangesBindAndUnbindsStaleDefault(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	keymapPath := filepath.Join(home, ".config", "projmux", "keymap.toml")
	if err := os.MkdirAll(filepath.Dir(keymapPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keymapPath, []byte("[bindings.sessionizer-sidebar]\nplain = \"M-a\"\nprefix = \"A\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
		readFile:   os.ReadFile,
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := stdout.String()
	for _, want := range []string{
		"unbind-key -q -n M-1",
		"unbind-key -q -n M-a",
		"unbind-key -q F",
		"unbind-key -q A",
		"bind-key -n M-a run-shell",
		"'/tmp/projmux' tmux popup-toggle --client #{client_tty} sessionizer-sidebar",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("print-config output = %q, want substring %q", output, want)
		}
	}
	if strings.Contains(output, "bind-key -n M-1 run-shell") {
		t.Fatalf("print-config output = %q, did not expect stale M-1 bind", output)
	}
	if strings.Contains(output, "bind-key F run-shell") {
		t.Fatalf("print-config output = %q, did not expect stale F bind", output)
	}
	if strings.Contains(output, "bind-key A run-shell") {
		t.Fatalf("print-config output = %q, did not expect prefix override bind", output)
	}
}

func TestTmuxPrintConfigInvalidKeymapReportsUsefulErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "unknown action",
			body: "[bindings.no-such-action]\nplain = \"M-a\"\n",
			want: "unknown action id",
		},
		{
			name: "invalid chord",
			body: "[bindings.sessionizer-sidebar]\nplain = \"M x\"\n",
			want: "contains unsupported tmux config characters",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			keymapPath := filepath.Join(home, ".config", "projmux", "keymap.toml")
			if err := os.MkdirAll(filepath.Dir(keymapPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(keymapPath, []byte(tt.body), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := &tmuxCommand{
				executable: func() (string, error) { return "/tmp/projmux", nil },
				homeDir:    func() (string, error) { return home, nil },
				lookupEnv:  func(string) string { return "" },
				readFile:   os.ReadFile,
			}
			err := cmd.Run([]string{"print-config"}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "keymap.toml") {
				t.Fatalf("Run() error = %v, want %q with keymap path", err, tt.want)
			}
		})
	}
}

func TestTmuxPrintConfigUsesSavedStatusbarDecoration(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	paths, err := config.Homes{HomeDir: home}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveStatusbarDecorationFile(paths.StatusbarDecorationFile(), config.StatusbarDecorationSymbol); err != nil {
		t.Fatal(err)
	}

	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := stdout.String(), "set -g @projmux_statusbar_decoration symbol"; !strings.Contains(got, want) {
		t.Fatalf("print-config output = %q, want substring %q", got, want)
	}
	for _, want := range []string{
		"set -g @projmux_statusbar_decoration_cwd symbol",
		"set -g @projmux_statusbar_decoration_git symbol",
		"set -g @projmux_statusbar_decoration_notify symbol",
	} {
		if got := stdout.String(); !strings.Contains(got, want) {
			t.Fatalf("print-config output = %q, want substring %q", got, want)
		}
	}
}

func TestTmuxPrintConfigShortCircuitsWindowListClicksToNativeSelectWindow(t *testing.T) {
	t.Parallel()

	// Empirically tmux 3.4+ fires `MouseDown1Status` with
	// `#{mouse_status_range}` set to the bare string "window" and an empty
	// `#{mouse_window}` for window-list clicks. The `select-window -t =`
	// idiom only resolves through tmux's internal mouse context, which a
	// `run-shell` subprocess cannot see — so the projmux dispatcher would
	// no-op silently. The bind must short-circuit that case via
	// `if-shell -F` to a native `select-window -t =` *before* invoking
	// projmux. This test pins the rendered config so the regression cannot
	// silently come back.
	cmd := &tmuxCommand{executable: func() (string, error) { return "/tmp/proj mux/bin/projmux", nil }}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := stdout.String()
	for _, want := range []string{
		// Must short-circuit the bare "window" range to native select-window
		// using tmux block syntax (avoids invalid triple-escaped quoting that
		// breaks the tmux config parser).
		"bind-key -n MouseDown1Status if-shell -F \"#{==:#{mouse_status_range},window}\"",
		"{ select-window -t = }",
		// Projmux fallback path for non-window ranges still goes through run-shell,
		// now wrapped in a `{ ... }` block instead of an extra layer of quoting.
		`{ run-shell "'/tmp/proj mux/bin/projmux' statusbar click \"#{mouse_status_range}\" --client \"#{client_tty}\" --mouse-window \"#{mouse_window}\"" }`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("print-config output = %q, want substring %q", output, want)
		}
	}
	// The previous unconditional `run-shell` bind (no if-shell guard) must be gone.
	for _, banned := range []string{
		"bind-key -n MouseDown1Status run-shell ",
	} {
		if strings.Contains(output, banned) {
			t.Fatalf("print-config output = %q, did not expect substring %q", output, banned)
		}
	}
}

func TestTmuxRebalancePanesSelectsMultiPaneWindows(t *testing.T) {
	t.Parallel()

	runner := &recordingTmuxRunner{
		outputs: map[string]string{
			strings.Join([]string{"tmux", "list-windows", "-a", "-F", "#{window_id}\t#{window_panes}"}, "\x00"): "@1\t1\n@2\t3\n@3\t2\n",
		},
	}
	cmd := &tmuxCommand{runner: runner}

	if err := cmd.Run([]string{"rebalance-panes"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []recordedTmuxCall{
		{name: "tmux", args: []string{"list-windows", "-a", "-F", "#{window_id}\t#{window_panes}"}},
		{name: "tmux", args: []string{"select-layout", "-t", "@2", "-E"}},
		{name: "tmux", args: []string{"select-layout", "-t", "@3", "-E"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestTmuxRenamePaneSetsTitleAndAITopic(t *testing.T) {
	t.Parallel()

	runner := &recordingTmuxRunner{}
	cmd := &tmuxCommand{runner: runner}

	if err := cmd.Run([]string{"rename-pane", "%42", "projmux-2"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []recordedTmuxCall{
		{name: "tmux", args: []string{"select-pane", "-T", "projmux-2", "-t", "%42"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%42", "@projmux_ai_topic", "projmux-2"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%42", "@projmux_ai_topic_manual", "1"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestTmuxRenamePaneEmptyClearsManualAITopic(t *testing.T) {
	t.Parallel()

	runner := &recordingTmuxRunner{}
	cmd := &tmuxCommand{runner: runner}

	if err := cmd.Run([]string{"rename-pane", "%42", ""}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []recordedTmuxCall{
		{name: "tmux", args: []string{"select-pane", "-T", "", "-t", "%42"}},
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%42", "@projmux_ai_topic", ""}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%42", "@projmux_ai_topic_manual"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestTmuxPrintAppConfigUsesIsolatedAppSettings(t *testing.T) {
	t.Parallel()

	cmd := &tmuxCommand{executable: func() (string, error) { return "/tmp/projmux", nil }}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-app-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := stdout.String()
	for _, want := range []string{
		"Generated by projmux. Used by `projmux shell`.",
		"set -g @projmux_app 1",
		"set -g history-limit 10000",
		"set -g set-clipboard on",
		"set -g default-shell \"/bin/sh\"",
		"set -g default-command \"\"",
		"set -ga update-environment \"WSL_DISTRO_NAME\"",
		"set -ga update-environment \"VSCODE_IPC_HOOK_CLI\"",
		"set -ga update-environment \"TERM_PROGRAM_VERSION\"",
		"set -ga update-environment \"PROJMUX_WELCOME\"",
		"set-hook -g client-attached",
		"run-shell -b",
		"'/tmp/projmux' welcome --popup >/dev/null 2>&1",
		"set -g status-position bottom",
		"set -g status-keys vi",
		"set -g window-status-separator \" \"",
		"set -g allow-rename off",
		"set -g automatic-rename on",
		"set -g automatic-rename-format " + tmuxConfigQuote(tmuxVisiblePaneLabelFormat()),
		"set -g mode-keys vi",
		"set -sg escape-time 100",
		"set -g pane-border-style \"fg=colour236\"",
		"set -g pane-active-border-style \"fg=colour51,bold\"",
		"set -g pane-border-status top",
		"set -g pane-border-format \"#{?pane_active,#[bold#,fg=colour16#,bg=colour45] > ",
		"#[bold#,fg=colour220] ● ",
		"#[bold#,fg=colour46] ● ",
		"#[fg=colour244] ",
		"#{@projmux_ai_topic}",
		"#{pane_current_command},#{pane_title}",
		"unbind-key -q R",
		"unbind-key -q M",
		"bind-key -n M-Left select-pane -L",
		"bind-key -n M-Right select-pane -R",
		"bind-key -n M-Up select-pane -U",
		"bind-key -n M-Down select-pane -D",
		"bind-key -n M-S-Left previous-window",
		"bind-key -n M-S-Right next-window",
		"set -g status-left-length 20",
		"set -g status-left \"#[range=user|session]#[bold,fg=colour231,bg=colour90] #{s|^[^-]*-||:session_name} #[default]#[norange] \"",
		"#[bold,fg=colour231,bg=colour90] #{s|^[^-]*-||:session_name} #[default]",
		"#{n:window_name}",
		"#{=/7/...:window_name}",
		"set -g @projmux_statusbar_decoration off",
		"set -g @projmux_statusbar_decoration_cwd off",
		"set -g @projmux_statusbar_decoration_git off",
		"set -g @projmux_statusbar_decoration_notify off",
		"@projmux_statusbar_decoration_cwd",
		"#[fg=colour220] ",
		"#[fg=colour220]📁 ",
		"#[fg=colour250]#{=-28/...:pane_current_path}#[norange]",
		"'/tmp/projmux' tmux popup-toggle --client #{client_tty} sessionizer-sidebar",
		" %Y-%m-%d %H:%M #[bold,fg=colour230,bg=colour31]#[range=user|settings]  #[norange]#[default]",
		"set -g status 2",
		"range=user|notify",
		"range=user|usage",
		"set -g status-format[0]",
		"set -g status-format[1]",
		"#[align=left range=user|notify]#('/tmp/projmux' status notify --max-width 80)#[norange]#[align=right range=user|usage]#('/tmp/projmux' status usage --max-width 120)#[norange]",
		"#('/tmp/projmux' tmux autosave-session-state --quiet)",
		"align=left",
		"align=right",
		"set -gu status-format[2]",
		"unbind-key -q -n M-6",
		"unbind-key -q -n C-n",
		"unbind-key -q -n M-r",
		"unbind-key -q -n User11",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("print-app-config output = %q, want substring %q", output, want)
		}
	}
	if strings.Contains(output, "#[bold,fg=colour16,bg=colour45] app #[default]") {
		t.Fatalf("print-app-config output = %q, did not expect duplicate app status badge", output)
	}
	for _, banned := range []string{
		"set -g status 3",
		"set -g status-format[2] \"",
		"set -s user-keys",
		"set -s user-keys[",
		"\\033[900",
		"\\033[901",
		"bind-key -n User",
		"bind-key -n User8",
		"bind-key -n User9",
		"9009u",
		"9010u",
		"range=user|sessionstate",
		"statusbar click sessionstate",
		"$env:PROJMUX_MUX_BACKEND",
		"$env:PROJMUX_PICKER_BACKEND",
		"$env:PROJMUX_NATIVE_LINE_MODE",
		"psmux",
	} {
		if strings.Contains(output, banned) {
			t.Fatalf("print-app-config output = %q, did not expect substring %q", output, banned)
		}
	}
}

func TestTmuxAppNamingFormatsUseVisiblePaneLabel(t *testing.T) {
	t.Parallel()

	visibleLabel := tmuxVisiblePaneLabelFormat()
	shellLabel := tmuxShellPaneLabelFormat()
	paneBorder := tmuxPaneBorderFormat()
	configText := tmuxAppConfig("/tmp/projmux", "/bin/sh", config.StatusbarDecorationOff)

	wantShellLabel := "#{?#{||:#{||:#{||:#{==:#{pane_current_command},zsh},#{==:#{pane_current_command},bash}},#{||:#{==:#{pane_current_command},fish},#{==:#{pane_current_command},sh}}},#{||:#{==:#{pane_current_command},nu},#{==:#{pane_current_command},xonsh}}},#{pane_current_command},#{pane_title}}"
	if shellLabel != wantShellLabel {
		t.Fatalf("shell label format = %q, want %q", shellLabel, wantShellLabel)
	}
	if zshIndex, titleIndex := strings.Index(shellLabel, "#{==:#{pane_current_command},zsh}"), strings.Index(shellLabel, "#{pane_title}"); zshIndex < 0 || titleIndex < 0 || zshIndex > titleIndex {
		t.Fatalf("shell label format = %q, want zsh command match before raw pane_title fallback", shellLabel)
	}

	wantVisibleLabel := "#{?#{&&:#{!=:#{@projmux_ai_agent},},#{!=:#{@projmux_ai_topic},}},#{@projmux_ai_topic}," + shellLabel + "}"
	if visibleLabel != wantVisibleLabel {
		t.Fatalf("visible pane label format = %q, want AI topic before shell fallback: %q", visibleLabel, wantVisibleLabel)
	}
	if topicIndex, shellIndex := strings.Index(visibleLabel, "#{@projmux_ai_topic}"), strings.Index(visibleLabel, shellLabel); topicIndex < 0 || shellIndex < 0 || topicIndex > shellIndex {
		t.Fatalf("visible pane label format = %q, want @projmux_ai_topic before shell/current-command fallback", visibleLabel)
	}
	if !strings.Contains(paneBorder, visibleLabel) {
		t.Fatalf("pane border format = %q, want visible label %q", paneBorder, visibleLabel)
	}

	wantPaneBorderLine := "set -g pane-border-format " + tmuxConfigQuote(paneBorder)
	if !strings.Contains(configText, wantPaneBorderLine+"\n") {
		t.Fatalf("app config = %q, want pane border to use exact shared visible label line %q", configText, wantPaneBorderLine)
	}
	wantAutomaticRenameLine := "set -g automatic-rename-format " + tmuxConfigQuote(visibleLabel)
	if !strings.Contains(configText, wantAutomaticRenameLine+"\n") {
		t.Fatalf("app config = %q, want automatic rename to use exact visible label helper line %q", configText, wantAutomaticRenameLine)
	}
	if strings.Contains(configText, "set -g automatic-rename-format \"#{pane_title}\"") {
		t.Fatalf("app config = %q, automatic rename must not depend solely on raw pane_title", configText)
	}
}

func TestTmuxAppShellTitlePolicyDisablesProgramWindowRename(t *testing.T) {
	t.Parallel()

	configText := tmuxAppConfig("/tmp/projmux", "/bin/sh", config.StatusbarDecorationOff)
	for _, want := range []string{
		"set -g allow-rename off",
		"set -g automatic-rename on",
		"set -g automatic-rename-format " + tmuxConfigQuote(tmuxVisiblePaneLabelFormat()),
	} {
		if !strings.Contains(configText, want) {
			t.Fatalf("app config = %q, want shell title policy line %q", configText, want)
		}
	}
}

func TestTmuxAutosaveSessionStateForceCapturesAndStoresCurrentSession(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC)
	dir := t.TempDir()
	windowFormat := strings.Join([]string{"#{window_index}", "#{window_name}", "#{window_layout}"}, "\x1f")
	paneFormat := strings.Join([]string{
		"#{window_index}",
		"#{pane_index}",
		"#{pane_title}",
		"#{?pane_active,1,0}",
		"#{pane_current_path}",
		"#{@projmux_recipe_kind}",
		"#{@projmux_startup_command}",
		"#{@projmux_ai_managed}",
		"#{@projmux_ai_agent}",
		"#{@projmux_ai_topic}",
		"#{@projmux_ai_resume_id}",
		"#{@projmux_ai_resume_source}",
		"#{@projmux_ai_resume_updated_at}",
	}, "\x1f")
	runner := &recordingTmuxRunner{
		outputs: map[string]string{
			strings.Join([]string{"tmux", "display-message", "-p", "#{session_name}"}, "\x00"):              "workspace\n",
			strings.Join([]string{"tmux", "list-windows", "-t", "workspace", "-F", windowFormat}, "\x00"):   "0\x1fshell\x1flayout\n",
			strings.Join([]string{"tmux", "list-panes", "-s", "-t", "workspace", "-F", paneFormat}, "\x00"): "0\x1f0\x1fshell\x1f1\x1f/tmp\x1f\x1f\x1f\x1f\x1f\x1f\n",
		},
	}
	cmd := &tmuxCommand{
		runner:  runner,
		now:     func() time.Time { return now },
		homeDir: func() (string, error) { return t.TempDir(), nil },
		lookupEnv: func(name string) string {
			if name == sessionStateAutosaveEnv {
				return "on"
			}
			return ""
		},
		sessionStore: func() (sessionstate.Store, error) {
			return sessionstate.NewStore(dir), nil
		},
	}

	if err := cmd.Run([]string{"autosave-session-state", "--force"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	loaded, err := sessionstate.NewStore(dir).Load("workspace")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Session != "workspace" || loaded.DefaultCWD != "/tmp" || len(loaded.Windows) != 1 {
		t.Fatalf("snapshot = %#v, want current workspace session snapshot", loaded)
	}
	wantGate := recordedTmuxCall{name: "tmux", args: []string{"set-option", "-t", "workspace", "-q", "@projmux_sessionstate_autosave_at", "1778555045"}}
	if got := runner.calls[len(runner.calls)-1]; !reflect.DeepEqual(got, wantGate) {
		t.Fatalf("last tmux call = %#v, want %#v", got, wantGate)
	}
}

func TestTmuxAutosaveSessionStateSkipsWhenDebounceGateIsFresh(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC)
	runner := &recordingTmuxRunner{
		outputs: map[string]string{
			strings.Join([]string{"tmux", "display-message", "-p", "#{session_name}"}, "\x00"):                                         "workspace\n",
			strings.Join([]string{"tmux", "display-message", "-p", "-t", "workspace", "#{@projmux_sessionstate_source}"}, "\x00"):      "\n",
			strings.Join([]string{"tmux", "display-message", "-p", "-t", "workspace", "#{@projmux_sessionstate_autosave_at}"}, "\x00"): "1778555030\n",
		},
	}
	cmd := &tmuxCommand{
		runner:  runner,
		now:     func() time.Time { return now },
		homeDir: func() (string, error) { return t.TempDir(), nil },
		lookupEnv: func(name string) string {
			if name == sessionStateAutosaveEnv {
				return "on"
			}
			return ""
		},
		sessionStore: func() (sessionstate.Store, error) { return sessionstate.NewStore(t.TempDir()), nil },
	}

	if err := cmd.Run([]string{"autosave-session-state"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("tmux calls = %#v, want session resolution, source marker, and debounce gate read only", runner.calls)
	}
}

func TestTmuxAutosaveSessionStateUsesConfiguredInterval(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC)
	configHome := t.TempDir()
	paths := config.DefaultPaths(configHome, t.TempDir())
	if err := config.SaveSessionStateDurationFile(paths.SessionStateAutosaveIntervalFile(), 10*time.Second); err != nil {
		t.Fatalf("SaveSessionStateDurationFile() error = %v", err)
	}
	dir := t.TempDir()
	windowFormat := strings.Join([]string{"#{window_index}", "#{window_name}", "#{window_layout}"}, "\x1f")
	paneFormat := strings.Join([]string{
		"#{window_index}",
		"#{pane_index}",
		"#{pane_title}",
		"#{?pane_active,1,0}",
		"#{pane_current_path}",
		"#{@projmux_recipe_kind}",
		"#{@projmux_startup_command}",
		"#{@projmux_ai_managed}",
		"#{@projmux_ai_agent}",
		"#{@projmux_ai_topic}",
		"#{@projmux_ai_resume_id}",
		"#{@projmux_ai_resume_source}",
		"#{@projmux_ai_resume_updated_at}",
	}, "\x1f")
	runner := &recordingTmuxRunner{
		outputs: map[string]string{
			strings.Join([]string{"tmux", "display-message", "-p", "#{session_name}"}, "\x00"):                                         "workspace\n",
			strings.Join([]string{"tmux", "display-message", "-p", "-t", "workspace", "#{@projmux_sessionstate_source}"}, "\x00"):      "\n",
			strings.Join([]string{"tmux", "display-message", "-p", "-t", "workspace", "#{@projmux_sessionstate_autosave_at}"}, "\x00"): "1778555030\n",
			strings.Join([]string{"tmux", "list-windows", "-t", "workspace", "-F", windowFormat}, "\x00"):                              "0\x1fshell\x1flayout\n",
			strings.Join([]string{"tmux", "list-panes", "-s", "-t", "workspace", "-F", paneFormat}, "\x00"):                            "0\x1f0\x1fshell\x1f1\x1f/tmp\x1f\x1f\x1f\x1f\x1f\x1f\n",
		},
	}
	cmd := &tmuxCommand{
		runner:  runner,
		now:     func() time.Time { return now },
		homeDir: func() (string, error) { return t.TempDir(), nil },
		lookupEnv: func(name string) string {
			switch name {
			case "XDG_CONFIG_HOME":
				return configHome
			case sessionStateAutosaveEnv:
				return "on"
			default:
				return ""
			}
		},
		sessionStore: func() (sessionstate.Store, error) { return sessionstate.NewStore(dir), nil },
	}

	if err := cmd.Run([]string{"autosave-session-state"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := sessionstate.NewStore(dir).Load("workspace"); err != nil {
		t.Fatalf("Load() error = %v, want autosave after configured interval", err)
	}
	wantGate := recordedTmuxCall{name: "tmux", args: []string{"set-option", "-t", "workspace", "-q", "@projmux_sessionstate_autosave_at", "1778555045"}}
	if got := runner.calls[len(runner.calls)-1]; !reflect.DeepEqual(got, wantGate) {
		t.Fatalf("last tmux call = %#v, want %#v", got, wantGate)
	}
}

func TestTmuxAutosaveSessionStateSkipsFreshSource(t *testing.T) {
	t.Parallel()

	runner := &recordingTmuxRunner{
		outputs: map[string]string{
			strings.Join([]string{"tmux", "display-message", "-p", "#{session_name}"}, "\x00"):                                    "workspace\n",
			strings.Join([]string{"tmux", "display-message", "-p", "-t", "workspace", "#{@projmux_sessionstate_source}"}, "\x00"): "fresh\n",
		},
	}
	cmd := &tmuxCommand{
		runner:  runner,
		now:     func() time.Time { return time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC) },
		homeDir: func() (string, error) { return t.TempDir(), nil },
		lookupEnv: func(name string) string {
			if name == sessionStateAutosaveEnv {
				return "on"
			}
			return ""
		},
		sessionStore: func() (sessionstate.Store, error) { return sessionstate.NewStore(t.TempDir()), nil },
	}

	if err := cmd.Run([]string{"autosave-session-state", "--force"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []recordedTmuxCall{
		{name: "tmux", args: []string{"display-message", "-p", "#{session_name}"}},
		{name: "tmux", args: []string{"display-message", "-p", "-t", "workspace", "#{@projmux_sessionstate_source}"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("tmux calls = %#v, want fresh source gate only", runner.calls)
	}
}

func TestTmuxAutosaveSessionStateSkipsWhenDisabled(t *testing.T) {
	t.Parallel()

	runner := &recordingTmuxRunner{
		outputs: map[string]string{
			strings.Join([]string{"tmux", "display-message", "-p", "#{session_name}"}, "\x00"): "workspace\n",
		},
	}
	cmd := &tmuxCommand{
		runner:  runner,
		now:     func() time.Time { return time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC) },
		homeDir: func() (string, error) { return t.TempDir(), nil },
		lookupEnv: func(name string) string {
			if name == sessionStateAutosaveEnv {
				return "off"
			}
			return ""
		},
		sessionStore: func() (sessionstate.Store, error) { return sessionstate.NewStore(t.TempDir()), nil },
	}

	if err := cmd.Run([]string{"autosave-session-state"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []recordedTmuxCall{{name: "tmux", args: []string{"display-message", "-p", "#{session_name}"}}}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("tmux calls = %#v, want session resolution only when autosave disabled", runner.calls)
	}
}

func TestTmuxAutosaveSessionStateProjectOverridePrecedence(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		global      config.SessionStateToggle
		env         string
		project     config.SessionStateProjectToggle
		wantSaved   bool
		wantTmuxMin int
	}{
		{name: "project off global on", global: config.SessionStateToggleOn, project: config.SessionStateProjectOff, wantSaved: false, wantTmuxMin: 1},
		{name: "project on global off", global: config.SessionStateToggleOff, project: config.SessionStateProjectOn, wantSaved: true, wantTmuxMin: 5},
		{name: "project inherit global off", global: config.SessionStateToggleOff, project: config.SessionStateProjectInherit, wantSaved: false, wantTmuxMin: 1},
		{name: "project on env off", global: config.SessionStateToggleOn, env: "off", project: config.SessionStateProjectOn, wantSaved: true, wantTmuxMin: 5},
		{name: "project off env on", global: config.SessionStateToggleOff, env: "on", project: config.SessionStateProjectOff, wantSaved: false, wantTmuxMin: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			dir := filepath.Join(home, "state")
			saveGlobalAutosaveForTest(t, home, tc.global)
			saveProjectAutosaveForTest(t, home, "workspace", tc.project)
			runner := autosaveCaptureRunner("workspace", "/repo")
			cmd := &tmuxCommand{
				runner:  runner,
				now:     func() time.Time { return time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC) },
				homeDir: func() (string, error) { return home, nil },
				lookupEnv: func(name string) string {
					switch name {
					case "XDG_CONFIG_HOME":
						return filepath.Join(home, "config")
					case sessionStateAutosaveEnv:
						return tc.env
					default:
						return ""
					}
				},
				sessionStore: func() (sessionstate.Store, error) {
					return sessionstate.NewStore(dir), nil
				},
			}

			if err := cmd.Run([]string{"autosave-session-state", "--force"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			_, err := sessionstate.NewStore(dir).Load("workspace")
			if tc.wantSaved && err != nil {
				t.Fatalf("Load() error = %v, want saved snapshot", err)
			}
			if !tc.wantSaved && err == nil {
				t.Fatalf("Load() succeeded, want no autosaved snapshot")
			}
			if len(runner.calls) < tc.wantTmuxMin {
				t.Fatalf("tmux calls = %#v, want at least %d calls", runner.calls, tc.wantTmuxMin)
			}
		})
	}
}

func TestTmuxAutosaveSessionStateNoProjectSettingUsesGlobalFallback(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dir := filepath.Join(home, "state")
	saveGlobalAutosaveForTest(t, home, config.SessionStateToggleOn)
	runner := autosaveCaptureRunner("workspace", "/repo")
	cmd := &tmuxCommand{
		runner:  runner,
		now:     func() time.Time { return time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC) },
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "XDG_CONFIG_HOME" {
				return filepath.Join(home, "config")
			}
			return ""
		},
		sessionStore: func() (sessionstate.Store, error) {
			return sessionstate.NewStore(dir), nil
		},
	}

	if err := cmd.Run([]string{"autosave-session-state", "--force"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := sessionstate.NewStore(dir).Load("workspace"); err != nil {
		t.Fatalf("Load() error = %v, want global fallback autosave", err)
	}
}

func TestTmuxAutosaveSessionStateQuietSwallowsRuntimeErrors(t *testing.T) {
	t.Parallel()

	runner := &recordingTmuxRunner{err: errors.New("tmux unavailable")}
	cmd := &tmuxCommand{
		runner:       runner,
		now:          func() time.Time { return time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC) },
		homeDir:      func() (string, error) { return t.TempDir(), nil },
		lookupEnv:    func(string) string { return "" },
		sessionStore: func() (sessionstate.Store, error) { return sessionstate.NewStore(t.TempDir()), nil },
	}

	var stderr bytes.Buffer
	if err := cmd.Run([]string{"autosave-session-state", "--quiet"}, &bytes.Buffer{}, &stderr); err != nil {
		t.Fatalf("Run() error = %v, want quiet nil", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want quiet", stderr.String())
	}
}

func TestTmuxPrintAppConfigKeepsStandaloneAndAppKeymapScopesSeparated(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	keymapPath := filepath.Join(home, ".config", "projmux", "keymap.toml")
	if err := os.MkdirAll(filepath.Dir(keymapPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keymapPath, []byte("[bindings.sessionizer-sidebar]\nplain = \"M-a\"\n\n[bindings.new-window]\nplain = \"C-t\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
		readFile:   os.ReadFile,
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-app-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := stdout.String()
	for _, want := range []string{
		"bind-key -n M-a run-shell",
		"'/tmp/projmux' tmux popup-toggle --client #{client_tty} sessionizer-sidebar",
		"bind-key -n C-t new-window -c \"#{pane_current_path}\"",
		"unbind-key -q -n C-t",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("print-app-config output = %q, want substring %q", output, want)
		}
	}
	if strings.Contains(output, "bind-key -n C-t run-shell") {
		t.Fatalf("print-app-config output = %q, app chord unexpectedly used standalone action", output)
	}
	if strings.Contains(output, "bind-key -n M-a new-window") {
		t.Fatalf("print-app-config output = %q, standalone chord unexpectedly used app action", output)
	}
}

func TestTmuxPrintAppConfigAddsPlainAliasForTransportAction(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	keymapPath := filepath.Join(home, ".config", "projmux", "keymap.toml")
	if err := os.MkdirAll(filepath.Dir(keymapPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keymapPath, []byte("[bindings.previous-window]\nkeys = [\"M-[\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
		readFile:   os.ReadFile,
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-app-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := stdout.String()
	for _, want := range []string{
		"bind-key -n M-S-Left previous-window",
		"bind-key -n M-[ previous-window",
		"bind-key -n M-S-Right next-window",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("print-app-config output = %q, want substring %q", output, want)
		}
	}
}

func TestTmuxInstallWritesSnippetAndIncludesIt(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configPath := filepath.Join(home, ".tmux.conf")
	includePath := filepath.Join(home, ".config", "tmux", "projmux.conf")
	if err := os.WriteFile(configPath, []byte("set -g mouse on\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		lookupEnv:  func(name string) string { return home },
		writeFile:  os.WriteFile,
		readFile:   os.ReadFile,
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"install", "--config", configPath, "--include", includePath}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	snippet, err := os.ReadFile(includePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(snippet), "'/tmp/projmux' tmux popup-toggle --client #{client_tty} sessionizer") {
		t.Fatalf("snippet = %q, want projmux binding", string(snippet))
	}
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "source-file \""+includePath+"\"") {
		t.Fatalf("config = %q, want source-file include", string(config))
	}
	if !strings.Contains(stdout.String(), "included from "+configPath) {
		t.Fatalf("stdout = %q, want install summary", stdout.String())
	}
}

func TestTmuxInstallAppWritesAppConfig(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		lookupEnv:  func(name string) string { return home },
		writeFile:  os.WriteFile,
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"install-app", "--config", configPath}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "set -g @projmux_app 1") {
		t.Fatalf("config = %q, want app marker", string(content))
	}
	if !strings.Contains(stdout.String(), "wrote "+configPath) {
		t.Fatalf("stdout = %q, want write summary", stdout.String())
	}
}

func TestTmuxApplySkipsReloadWhenServerMissing(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	runner := &recordingTmuxRunner{err: errors.New("no server running on /tmp/tmux-1000/projmux")}
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		lookupEnv:  func(name string) string { return home },
		writeFile:  os.WriteFile,
		runner:     runner,
	}

	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"apply", "--config", configPath, "--socket", "projmux-test"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "set -g @projmux_app 1") {
		t.Fatalf("config = %q, want app marker", string(content))
	}
	if !strings.Contains(stdout.String(), "wrote "+configPath) {
		t.Fatalf("stdout = %q, want wrote line", stdout.String())
	}
	if !strings.Contains(stdout.String(), "skipped reload: no live tmux server -L projmux-test") {
		t.Fatalf("stdout = %q, want skip message", stdout.String())
	}

	if len(runner.calls) == 0 {
		t.Fatalf("expected at least one tmux probe call")
	}
	first := runner.calls[0]
	if first.name != "tmux" || len(first.args) < 4 || first.args[0] != "-L" || first.args[1] != "projmux-test" || first.args[2] != "list-sessions" {
		t.Fatalf("first call = %+v, want list-sessions probe", first)
	}
	for _, c := range runner.calls {
		if len(c.args) >= 3 && c.args[2] == "source-file" {
			t.Fatalf("source-file should not run when probe failed: %+v", c)
		}
	}
}

func TestTmuxApplyReloadsLiveServer(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	probeKey := strings.Join([]string{"tmux", "-L", "projmux", "list-sessions", "-F", "#{session_id}"}, "\x00")
	sourceKey := strings.Join([]string{"tmux", "-L", "projmux", "source-file", configPath}, "\x00")
	runner := &recordingTmuxRunner{
		outputs: map[string]string{
			probeKey:  "$0\n$1\n$2\n",
			sourceKey: "",
		},
	}
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		lookupEnv:  func(name string) string { return home },
		writeFile:  os.WriteFile,
		runner:     runner,
	}

	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"apply", "--config", configPath}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !strings.Contains(stdout.String(), "wrote "+configPath) {
		t.Fatalf("stdout = %q, want wrote line", stdout.String())
	}
	if !strings.Contains(stdout.String(), "reloaded tmux server -L projmux: 3 sessions") {
		t.Fatalf("stdout = %q, want reload summary with 3 sessions", stdout.String())
	}

	var sawSource bool
	for _, c := range runner.calls {
		if len(c.args) >= 4 && c.args[2] == "source-file" && c.args[3] == configPath {
			sawSource = true
		}
	}
	if !sawSource {
		t.Fatalf("expected source-file call, calls = %+v", runner.calls)
	}
}

func TestTmuxCommandRejectsInvalidUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing subcommand", args: nil, want: "tmux requires a subcommand"},
		{name: "unknown subcommand", args: []string{"nope"}, want: "unknown tmux subcommand: nope"},
		{name: "missing popup args", args: []string{"popup-preview"}, want: "tmux popup-preview requires exactly 1 argument"},
		{name: "blank session", args: []string{"popup-preview", " "}, want: "tmux popup-preview requires a non-empty <session> argument"},
		{name: "popup-switch extra args", args: []string{"popup-switch", "extra"}, want: "tmux popup-switch accepts no arguments"},
		{name: "popup-sessions extra args", args: []string{"popup-sessions", "extra"}, want: "tmux popup-sessions accepts no arguments"},
		{name: "missing popup-toggle mode", args: []string{"popup-toggle"}, want: "tmux popup-toggle requires exactly 1 argument"},
		{name: "unknown popup-toggle mode", args: []string{"popup-toggle", "nope"}, want: "unknown tmux popup-toggle mode: nope"},
		{name: "missing rename-pane args", args: []string{"rename-pane", "%1"}, want: "tmux rename-pane requires <pane> <title>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer
			err := (&tmuxCommand{}).Run(tt.args, &bytes.Buffer{}, &stderr)
			if err == nil {
				t.Fatal("expected error")
			}
			if !contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
			if !contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr = %q, want usage text", stderr.String())
			}
		})
	}
}

func TestTmuxCommandReportsConfigurationAndRuntimeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  *tmuxCommand
		want string
	}{
		{name: "missing popup client", cmd: &tmuxCommand{executable: func() (string, error) { return "/tmp/projmux", nil }}, want: "configure tmux popup client"},
		{name: "missing executable resolver", cmd: &tmuxCommand{popup: &stubTmuxPopupClient{}}, want: "configure tmux popup executable"},
		{name: "resolve executable", cmd: &tmuxCommand{popup: &stubTmuxPopupClient{}, executable: func() (string, error) { return "", errors.New("not found") }}, want: "resolve tmux popup executable"},
		{name: "display popup", cmd: &tmuxCommand{popup: &stubTmuxPopupClient{err: errors.New("tmux failed")}, executable: func() (string, error) { return "/tmp/projmux", nil }}, want: "display tmux popup preview"},
		{name: "resolve current pane", cmd: &tmuxCommand{popup: &stubTmuxPopupClient{currentPaneErr: errors.New("tmux unavailable")}, executable: func() (string, error) { return "/tmp/projmux", nil }}, want: "resolve tmux popup switch cwd"},
		{name: "display sessions popup", cmd: &tmuxCommand{popup: &stubTmuxPopupClient{err: errors.New("tmux failed")}, executable: func() (string, error) { return "/tmp/projmux", nil }}, want: "display tmux popup sessions"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			args := []string{"popup-preview", "dev"}
			if tt.want == "resolve tmux popup switch cwd" {
				args = []string{"popup-switch"}
			}
			if tt.want == "display tmux popup sessions" {
				args = []string{"popup-sessions"}
			}

			err := tt.cmd.Run(args, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatal("expected error")
			}
			if !contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func saveNativePickerBackend(t *testing.T, home string) {
	t.Helper()

	paths, err := config.Homes{HomeDir: home}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SavePickerBackendFile(paths.PickerBackendFile(), config.PickerBackendNative); err != nil {
		t.Fatalf("SavePickerBackendFile() error = %v", err)
	}
}

func saveGlobalAutosaveForTest(t *testing.T, home string, mode config.SessionStateToggle) {
	t.Helper()

	paths, err := config.Homes{HomeDir: home, ConfigHome: filepath.Join(home, "config")}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSessionStateToggleFile(paths.SessionStateAutosaveFile(), mode); err != nil {
		t.Fatalf("SaveSessionStateToggleFile(autosave) error = %v", err)
	}
}

func saveProjectAutosaveForTest(t *testing.T, home, sessionName string, mode config.SessionStateProjectToggle) {
	t.Helper()

	paths, err := config.Homes{HomeDir: home, ConfigHome: filepath.Join(home, "config")}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSessionStateProjectToggleFile(paths.ProjectSessionStateAutosaveFile(sessionName), mode); err != nil {
		t.Fatalf("SaveSessionStateProjectToggleFile() error = %v", err)
	}
}

func autosaveCaptureRunner(sessionName, cwd string) *recordingTmuxRunner {
	windowFormat := strings.Join([]string{"#{window_index}", "#{window_name}", "#{window_layout}"}, "\x1f")
	paneFormat := strings.Join([]string{
		"#{window_index}",
		"#{pane_index}",
		"#{pane_title}",
		"#{?pane_active,1,0}",
		"#{pane_current_path}",
		"#{@projmux_recipe_kind}",
		"#{@projmux_startup_command}",
		"#{@projmux_ai_managed}",
		"#{@projmux_ai_agent}",
		"#{@projmux_ai_topic}",
		"#{@projmux_ai_resume_id}",
		"#{@projmux_ai_resume_source}",
		"#{@projmux_ai_resume_updated_at}",
	}, "\x1f")
	return &recordingTmuxRunner{
		outputs: map[string]string{
			strings.Join([]string{"tmux", "display-message", "-p", "#{session_name}"}, "\x00"):                                    sessionName + "\n",
			strings.Join([]string{"tmux", "display-message", "-p", "-t", sessionName, "#{@projmux_sessionstate_source}"}, "\x00"): "\n",
			strings.Join([]string{"tmux", "list-windows", "-t", sessionName, "-F", windowFormat}, "\x00"):                         "0\x1fshell\x1flayout\n",
			strings.Join([]string{"tmux", "list-panes", "-s", "-t", sessionName, "-F", paneFormat}, "\x00"):                       "0\x1f0\x1fshell\x1f1\x1f" + cwd + "\x1f\x1f\x1f\x1f\x1f\x1f\n",
		},
	}
}

type stubTmuxPopupClient struct {
	currentPanePath string
	currentPaneErr  error
	command         string
	options         inttmux.PopupOptions
	err             error
}

func (s *stubTmuxPopupClient) CurrentPanePath(context.Context) (string, error) {
	if s.currentPaneErr != nil {
		return "", s.currentPaneErr
	}
	return s.currentPanePath, nil
}

func (s *stubTmuxPopupClient) DisplayPopupWithOptions(_ context.Context, command string, options inttmux.PopupOptions) error {
	s.command = command
	s.options = options
	return s.err
}

type recordingTmuxRunner struct {
	formats map[string]string
	outputs map[string]string
	errors  map[string]error
	calls   []recordedTmuxCall
	err     error
}

type recordedTmuxCall struct {
	name string
	args []string
}

func (r *recordingTmuxRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, recordedTmuxCall{name: name, args: append([]string(nil), args...)})
	if name == "tmux" && len(args) == 4 && reflect.DeepEqual(args[:3], []string{"display-message", "-p", "-F"}) {
		return []byte(r.formats[args[3]] + "\n"), nil
	}
	key := recordedTmuxCallKey(name, args...)
	if err, ok := r.errors[key]; ok {
		return nil, err
	}
	if output, ok := r.outputs[key]; ok {
		return []byte(output), nil
	}
	if r.err != nil {
		return nil, r.err
	}
	return nil, nil
}

func recordedTmuxCallKey(name string, args ...string) string {
	return strings.Join(append([]string{name}, args...), "\x00")
}

func containsTmuxArgPair(args []string, key, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}
