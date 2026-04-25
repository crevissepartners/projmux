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

	inttmux "github.com/es5h/projmux/internal/integrations/tmux"
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
		Title:         "projmux: dev",
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
		Title:         "projmux switch",
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
		Title:         "projmux sessions",
		CloseBehavior: inttmux.PopupCloseOnExit,
	}
	if !reflect.DeepEqual(popup.options, wantOptions) {
		t.Fatalf("popup options = %#v, want %#v", popup.options, wantOptions)
	}
}

func TestAppRunTmuxPopupToggleOpensStandaloneSidebar(t *testing.T) {
	t.Parallel()

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
		"-w", "40",
		"-h", "50",
		"-T", "projmux sidebar",
	}
	if got.name != "tmux" || len(got.args) < len(wantPrefix)+1 || !reflect.DeepEqual(got.args[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("display call = %#v, want prefix %#v", got, wantPrefix)
	}
	command := got.args[len(got.args)-1]
	for _, want := range []string{
		"touch -- '/tmp/projmux-tmux-popup-_dev_pts_projmux-test-sidebar-sessionizer-sidebar.marker'",
		"cd -- '/tmp/work tree'",
		"TMUX_SESSIONIZER_CONTEXT_SESSION='work'",
		"TMUX_SESSIONIZER_CONTEXT_PANE='%1'",
		"'/tmp/proj mux/bin/projmux' 'switch' '--ui=sidebar'",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("popup command = %q, want substring %q", command, want)
		}
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
		"'/tmp/proj mux/bin/projmux' tmux popup-toggle sessionizer-sidebar",
		"bind-key -n User2 run-shell",
		"'/tmp/proj mux/bin/projmux' tmux popup-toggle session-popup",
		"bind-key -n User0 run-shell",
		"'/tmp/proj mux/bin/projmux' ai split right",
		"set-hook -g pane-focus-in",
		"'/tmp/proj mux/bin/projmux' attention clear #{pane_id}",
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
	if !strings.Contains(string(snippet), "'/tmp/projmux' tmux popup-toggle sessionizer") {
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
	if r.err != nil {
		return nil, r.err
	}
	return nil, nil
}
