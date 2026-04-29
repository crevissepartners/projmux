package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
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
		Width:         "80%",
		Height:        "70%",
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
		CloseBehavior: inttmux.PopupCloseOnExit,
	}
	if !reflect.DeepEqual(popup.options, wantOptions) {
		t.Fatalf("popup options = %#v, want %#v", popup.options, wantOptions)
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
				Width:         "120",
				Height:        "28",
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
	}

	if err := cmd.Run([]string{"popup-toggle", "sessionizer-sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := runner.calls[len(runner.calls)-1]
	wantPrefix := []string{
		"display-popup",
		"-t", "%1",
		"-E",
		"-d", "/tmp/work tree",
		"-e", "TMUX_SESSIONIZER_CONTEXT_DIR=/tmp/work tree",
		"-e", "TMUX_SESSIONIZER_CONTEXT_PANE=%1",
		"-e", "TMUX_SESSIONIZER_CONTEXT_SESSION=work",
		"-x", "0",
		"-y", "0",
		"-w", "56",
		"-h", "50",
	}
	if got.name != "tmux" || len(got.args) < len(wantPrefix)+1 || !reflect.DeepEqual(got.args[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("display call = %#v, want prefix %#v", got, wantPrefix)
	}
	command := got.args[len(got.args)-1]
	for _, want := range []string{
		"cd -- '/tmp/work tree'",
		"TMUX_SESSIONIZER_CONTEXT_SESSION='work'",
		"TMUX_SESSIONIZER_CONTEXT_PANE='%1'",
		"'/tmp/proj mux/bin/projmux' 'switch' '--ui=sidebar'",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("popup command = %q, want substring %q", command, want)
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
		"-d", "/tmp/work tree",
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
		"bind-key -n User2 run-shell",
		"'/tmp/proj mux/bin/projmux' tmux popup-toggle --client #{client_tty} session-popup",
		"bind-key -n User0 run-shell",
		"'/tmp/proj mux/bin/projmux' ai split right",
		"set -s user-keys[10] \"\\033[9011u\"",
		"set -s user-keys[11] \"\\033[9012u\"",
		"bind-key -n M-r command-prompt",
		"rename-window -- '%%'",
		"bind-key -n User10 command-prompt",
		"bind-key -n User11 command-prompt",
		"select-pane -T '%1'",
		"set-option -p @projmux_ai_topic '%1'",
		"set-option -p @projmux_ai_topic_manual 1",
		"bind-key R command-prompt",
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
		"#[bold,fg=colour16,bg=colour45] projmux #[default]",
		"'/tmp/proj mux/bin/projmux' status kube",
		"'/tmp/proj mux/bin/projmux' status git",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("print-config output = %q, want substring %q", output, want)
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
		"set -g default-shell /usr/bin/zsh",
		"set -g default-command \"\"",
		"set -ga update-environment \"WSL_DISTRO_NAME\"",
		"set -ga update-environment \"VSCODE_IPC_HOOK_CLI\"",
		"set -ga update-environment \"TERM_PROGRAM_VERSION\"",
		"set -g status-position bottom",
		"set -g status-keys vi",
		"set -g window-status-separator \" \"",
		"set -g automatic-rename on",
		"set -g automatic-rename-format \"#{pane_title}\"",
		"set -s user-keys[11] \"\\033[9012u\"",
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
		"set -s user-keys[7] \"\\033[9008u\"",
		"set -s user-keys[8] \"\\033[9009u\"",
		"set -s user-keys[9] \"\\033[9010u\"",
		"set -s user-keys[10] \"\\033[9011u\"",
		"bind-key -n User11 command-prompt",
		"select-pane -T '%1'",
		"set-option -p @projmux_ai_topic '%1'",
		"set-option -p @projmux_ai_topic_manual 1",
		"bind-key -n M-Left select-pane -L",
		"bind-key -n M-Right select-pane -R",
		"bind-key -n M-Up select-pane -U",
		"bind-key -n M-Down select-pane -D",
		"bind-key -n C-n new-window -c \"#{pane_current_path}\"",
		"bind-key -n M-S-Left previous-window",
		"bind-key -n M-S-Right next-window",
		"bind-key -n M-r command-prompt",
		"bind-key -n User7 new-window -c \"#{pane_current_path}\"",
		"bind-key -n User8 previous-window",
		"bind-key -n User9 next-window",
		"bind-key -n User10 command-prompt",
		"bind-key R command-prompt",
		"bind-key M if -F \"#{mouse}\"",
		"set -g status-left-length 20",
		"set -g status-left \"#[bold,fg=colour231,bg=colour90] #{s|^[^-]*-||:session_name} #[default]\"",
		"#[bold,fg=colour231,bg=colour90] #{s|^[^-]*-||:session_name} #[default]",
		"'/tmp/projmux' tmux popup-toggle --client #{client_tty} sessionizer-sidebar",
		"%Y-%m-%d %H:%M#[default]",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("print-app-config output = %q, want substring %q", output, want)
		}
	}
	if strings.Contains(output, "#[bold,fg=colour16,bg=colour45] app #[default]") {
		t.Fatalf("print-app-config output = %q, did not expect duplicate app status badge", output)
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
	if output, ok := r.outputs[strings.Join(append([]string{name}, args...), "\x00")]; ok {
		return []byte(output), nil
	}
	if r.err != nil {
		return nil, r.err
	}
	return nil, nil
}
