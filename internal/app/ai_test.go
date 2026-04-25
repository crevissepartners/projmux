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

	intfzf "github.com/es5h/projmux/internal/ui/fzf"
)

func TestAISettingsGetAndSetMode(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"settings", "--get"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run settings --get error = %v", err)
	}
	if got, want := stdout.String(), "selective\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}

	if err := cmd.Run([]string{"settings", "--set", "codex"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run settings --set error = %v", err)
	}
	stdout.Reset()
	if err := cmd.Run([]string{"settings", "--get"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run settings --get after set error = %v", err)
	}
	if got, want := stdout.String(), "codex\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestAISettingsPickerSetsSelectedMode(t *testing.T) {
	home := t.TempDir()
	runner := &capturingAIRunner{result: intfzf.Result{Key: "enter", Value: "shell"}}
	cmd := testAICommand(home)
	cmd.runner = runner

	if err := cmd.Run([]string{"settings"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run settings picker error = %v", err)
	}
	if got, want := runner.options.UI, "ai-settings"; got != want {
		t.Fatalf("runner UI = %q, want %q", got, want)
	}
	if got, want := runner.options.Prompt, "AI Setting > "; got != want {
		t.Fatalf("runner prompt = %q, want %q", got, want)
	}
	if got, want := runner.options.Footer, "[projmux]\nEnter: set default  |  Esc/Alt+5/Ctrl+Alt+S: close"; got != want {
		t.Fatalf("runner footer = %q, want %q", got, want)
	}
	if got, want := readModeFile(t, home), "shell\n"; got != want {
		t.Fatalf("mode file = %q, want %q", got, want)
	}
}

func TestAIPickerLabelsProjmuxFooter(t *testing.T) {
	home := t.TempDir()
	runner := &capturingAIRunner{}
	cmd := testAICommand(home)
	cmd.runner = runner

	if _, err := cmd.runAgentPicker("right"); err != nil {
		t.Fatalf("runAgentPicker error = %v", err)
	}
	if got, want := runner.options.UI, "ai-picker"; got != want {
		t.Fatalf("runner UI = %q, want %q", got, want)
	}
	if got, want := runner.options.Footer, "[projmux]\nEnter: launch  |  Esc/Alt+4/Alt+5/Ctrl+Alt+S: close"; got != want {
		t.Fatalf("runner footer = %q, want %q", got, want)
	}
}

func TestAIPickerMarksAgentReadyWhenBinaryExistsWithoutLegacyWrapper(t *testing.T) {
	home := t.TempDir()
	codexBin := writeExecutable(t, filepath.Join(home, "bin", "codex"))
	claudeBin := writeExecutable(t, filepath.Join(home, "bin", "claude"))
	cmd := testAICommand(home)
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "codex"}) {
			return []byte(codexBin + "\n"), nil
		}
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "claude"}) {
			return []byte(claudeBin + "\n"), nil
		}
		return nil, os.ErrNotExist
	}

	rows := cmd.agentRows()
	if len(rows) < 2 {
		t.Fatalf("agentRows len = %d, want at least 2", len(rows))
	}
	for _, row := range rows[:2] {
		if !strings.Contains(row.Label, "[READY]") {
			t.Fatalf("row label = %q, want READY without legacy wrapper", row.Label)
		}
	}
}

func TestAISplitSelectiveDelegatesToPopupToggle(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.executable = func() (string, error) { return "/tmp/projmux bin", nil }
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{client_tty}"}) {
			return []byte("/dev/pts/7\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"split", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split error = %v", err)
	}

	want := []recordedAICommand{{
		name: "/tmp/projmux bin",
		args: []string{"tmux", "popup-toggle", "--client", "/dev/pts/7", "ai-split-picker-right"},
	}}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
}

func TestAISplitCodexRunsNativeTmuxSplitAndStartsWatcher(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	codexBin := writeExecutable(t, filepath.Join(home, "bin", "codex"))
	cmd := testAICommand(home)
	if err := cmd.setMode("codex"); err != nil {
		t.Fatal(err)
	}
	cmdRecorder(cmd).commands = nil
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "TMUX":
			return "/tmp/tmux"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmdRecorder(cmd).commands = append(cmdRecorder(cmd).commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "codex"}) {
			return []byte(codexBin + "\n"), nil
		}
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_id}"}) {
			return []byte("%7\n"), nil
		}
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-F", "#{pane_current_path}"}) {
			return []byte(work + "\n"), nil
		}
		if name == "tmux" && len(args) >= 6 && reflect.DeepEqual(args[:6], []string{"split-window", "-P", "-F", "#{pane_id}", "-h", "-t"}) {
			return []byte("%9\n"), nil
		}
		if name == "tmux" && reflect.DeepEqual(args, []string{"list-panes", "-t", "%7", "-F", "#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}"}) {
			return []byte("%2\t0\t0\t20\t10\n%7\t21\t0\t10\t10\n%9\t32\t0\t10\t10\n%8\t0\t11\t42\t10\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"split", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split codex error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, "tmux", []string{"split-window", "-P", "-F", "#{pane_id}", "-h", "-t", "%7", "-c", work, "zsh", "-lc"}) {
		t.Fatalf("commands = %#v, want native tmux split-window", commands)
	}
	for _, want := range [][]string{
		{"resize-pane", "-t", "%2", "-x", "14"},
		{"resize-pane", "-t", "%7", "-x", "13"},
		{"resize-pane", "-t", "%9", "-x", "13"},
	} {
		if !containsAICommandArgs(commands, "tmux", want) {
			t.Fatalf("commands = %#v, want scoped row resize %v", commands, want)
		}
	}
	if containsAICommandArgs(commands, "tmux", []string{"resize-pane", "-t", "%8", "-x", "13"}) {
		t.Fatalf("commands = %#v, did not expect resize outside target row", commands)
	}
	if !containsAICommandArgs(commands, "tmux", []string{"run-shell", "-b", "'/tmp/projmux' ai watch-title '%9'"}) {
		t.Fatalf("commands = %#v, want codex watch-title run-shell", commands)
	}
	if !containsAICommandArgSubstring(commands, "cd '"+work+"' && __codex_title='codex:repo'") {
		t.Fatalf("commands = %#v, want codex launch command with context title", commands)
	}
}

func TestAISplitSelectiveTreatsCancelledPickerAsNoOp(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.executable = func() (string, error) { return "/tmp/projmux", nil }
	cmd.lookupEnv = func(name string) string {
		if name == "TMUX" {
			return "/tmp/tmux"
		}
		return ""
	}
	cmd.runCommand = func(context.Context, string, ...string) error {
		return errors.New("exit status 1")
	}

	if err := cmd.Run([]string{"split", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split canceled picker error = %v, want nil", err)
	}
}

func TestAISplitSelectiveTreatsClosedPopupAsNoOp(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.executable = func() (string, error) { return "/tmp/projmux", nil }
	cmd.lookupEnv = func(name string) string {
		if name == "TMUX" {
			return "/tmp/tmux"
		}
		return ""
	}
	cmd.runCommand = func(context.Context, string, ...string) error {
		return errors.New("exit status 129")
	}

	if err := cmd.Run([]string{"split", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split closed popup error = %v, want nil", err)
	}
}

func TestAISplitShellUsesTmuxSplitWindow(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := testAICommand(home)
	if err := cmd.setMode("shell"); err != nil {
		t.Fatal(err)
	}
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "TMUX":
			return "/tmp/tmux"
		case "TMUX_SPLIT_CONTEXT_DIR":
			return work
		case "TMUX_SPLIT_TARGET_PANE":
			return "%9"
		default:
			return ""
		}
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%9", "-F", "#{pane_id}"}) {
			return []byte("%9\n"), nil
		}
		if name == "tmux" && reflect.DeepEqual(args, []string{"list-panes", "-t", "%9", "-F", "#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}"}) {
			return []byte("%1\t0\t0\t80\t10\n%9\t0\t11\t80\t5\n%10\t0\t17\t80\t5\n%11\t81\t0\t20\t22\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"split", "down"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run split shell error = %v", err)
	}

	want := []recordedAICommand{
		{name: "tmux", args: []string{"display-message", "ai split default: shell"}},
		{name: "tmux", args: []string{"split-window", "-v", "-t", "%9", "-c", work, "zsh", "-l"}},
		{name: "tmux", args: []string{"resize-pane", "-t", "%1", "-y", "7"}},
		{name: "tmux", args: []string{"resize-pane", "-t", "%9", "-y", "7"}},
		{name: "tmux", args: []string{"resize-pane", "-t", "%10", "-y", "6"}},
	}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
}

func TestSplitLayoutPeersPreserveOtherAxes(t *testing.T) {
	panes := []aiPaneGeometry{
		{id: "%1", left: 0, top: 0, width: 20, height: 10},
		{id: "%2", left: 21, top: 0, width: 10, height: 10},
		{id: "%3", left: 0, top: 11, width: 31, height: 10},
	}
	rightPeers := splitLayoutPeers(panes, panes[1], "right")
	if got, want := paneGeometryIDs(rightPeers), []string{"%1", "%2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("right peers = %#v, want %#v", got, want)
	}

	panes = []aiPaneGeometry{
		{id: "%1", left: 0, top: 0, width: 40, height: 10},
		{id: "%2", left: 0, top: 11, width: 40, height: 5},
		{id: "%3", left: 41, top: 0, width: 20, height: 16},
	}
	downPeers := splitLayoutPeers(panes, panes[1], "down")
	if got, want := paneGeometryIDs(downPeers), []string{"%1", "%2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("down peers = %#v, want %#v", got, want)
	}
}

func TestAIStatusSetThinkingMarksPaneBusy(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%1", "#{pane_title}"}) {
			return []byte("codex: repo\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"status", "set", "thinking", "%1"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run status set thinking error = %v", err)
	}

	want := []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%1", "@dotfiles_attention_state", "busy"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%1", "@dotfiles_attention_ack"}},
		{name: "tmux", args: []string{"select-pane", "-T", "⠹ codex: repo", "-t", "%1"}},
	}
	if !reflect.DeepEqual(cmdRecorder(cmd).commands, want) {
		t.Fatalf("commands = %#v, want %#v", cmdRecorder(cmd).commands, want)
	}
}

func TestAIStatusSetWaitingMarksPaneReplyAndNotifies(t *testing.T) {
	home := t.TempDir()
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
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{pane_title}"}):
			return []byte("Codex: approval needed\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{@dotfiles_desktop_notified}"}),
			reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{@dotfiles_desktop_notification_key}"}),
			reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{@dotfiles_desktop_notification_at}"}):
			return []byte("\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#S"}):
			return []byte("repo\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#W"}):
			return []byte("dev\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%2", "#{pane_current_path}"}):
			return []byte(home + "\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"status", "set", "waiting", "%2"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run status set waiting error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	wantPrefix := []recordedAICommand{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%2", "@dotfiles_attention_state", "reply"}},
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%2", "@dotfiles_attention_ack"}},
		{name: "tmux", args: []string{"select-pane", "-T", "✳ Codex: approval needed", "-t", "%2"}},
	}
	if len(commands) < len(wantPrefix) || !reflect.DeepEqual(commands[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("command prefix = %#v, want %#v", commands, wantPrefix)
	}
	if !containsAICommand(commands, "notify-send") {
		t.Fatalf("commands = %#v, want notify-send dispatch", commands)
	}
	if !containsAICommandArg(commands, "@dotfiles_desktop_notified") {
		t.Fatalf("commands = %#v, want notification record", commands)
	}
}

func TestAINotifySkipsRecentDuplicateButRefreshesRecord(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.now = func() time.Time { return time.Unix(1000, 0) }
	cmd.lookupEnv = func(name string) string {
		if name == "DOTFILES_TMUX_NOTIFY_DEDUPE_SECONDS" {
			return "120"
		}
		return ""
	}
	key := "input_required|waiting for input"
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{@dotfiles_desktop_notified}"}):
			return []byte("\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{pane_title}"}):
			return []byte("waiting for input\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{@dotfiles_desktop_notification_key}"}):
			return []byte(key + "\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{@dotfiles_desktop_notification_at}"}):
			return []byte("950\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"notify", "notify", "%3"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run notify error = %v", err)
	}
	commands := cmdRecorder(cmd).commands
	if containsAICommand(commands, "notify-send") {
		t.Fatalf("commands = %#v, did not expect notify-send for duplicate", commands)
	}
	if !containsAICommandArg(commands, "@dotfiles_desktop_notification_at") {
		t.Fatalf("commands = %#v, want refreshed notification timestamp", commands)
	}
}

func TestAIWatchTitlePromotesBusyPaneToThinking(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	checks := 0
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%4", "#{pane_id}"}):
			checks++
			if checks > 1 {
				return nil, os.ErrNotExist
			}
			return []byte("%4\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%4", "#{pane_title}__DOTFILES_TMUX_AI_SEP__#{@dotfiles_attention_state}__DOTFILES_TMUX_AI_SEP__#{@dotfiles_attention_ack}"}):
			return []byte("thinking hard__DOTFILES_TMUX_AI_SEP____DOTFILES_TMUX_AI_SEP__\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%4", "#{pane_title}"}):
			return []byte("thinking hard\n"), nil
		}
		return nil, os.ErrNotExist
	}

	if err := cmd.Run([]string{"watch-title", "%4"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run watch-title error = %v", err)
	}

	if !containsAICommandArg(cmdRecorder(cmd).commands, "busy") {
		t.Fatalf("commands = %#v, want busy attention state", cmdRecorder(cmd).commands)
	}
}

func TestAIWatchTitleSettledBusyReturnsIdleWithoutNotification(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "DOTFILES_CODEX_REPLY_SETTLE_LOOPS":
			return "2"
		default:
			return ""
		}
	}
	checks := 0
	snapshots := []string{
		"thinking hard__DOTFILES_TMUX_AI_SEP____DOTFILES_TMUX_AI_SEP__",
		"repo__DOTFILES_TMUX_AI_SEP__busy__DOTFILES_TMUX_AI_SEP__",
		"repo__DOTFILES_TMUX_AI_SEP__busy__DOTFILES_TMUX_AI_SEP__",
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "notify-send"}) {
			return []byte("/usr/bin/notify-send\n"), nil
		}
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%6", "#{pane_id}"}):
			checks++
			if checks > len(snapshots) {
				return nil, os.ErrNotExist
			}
			return []byte("%6\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%6", "#{pane_title}__DOTFILES_TMUX_AI_SEP__#{@dotfiles_attention_state}__DOTFILES_TMUX_AI_SEP__#{@dotfiles_attention_ack}"}):
			return []byte(snapshots[checks-1] + "\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%6", "#{pane_title}"}):
			if checks <= 1 {
				return []byte("thinking hard\n"), nil
			}
			return []byte("repo\n"), nil
		}
		return []byte("\n"), nil
	}

	if err := cmd.Run([]string{"watch-title", "%6"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run watch-title error = %v", err)
	}

	commands := cmdRecorder(cmd).commands
	if !containsAICommandArgs(commands, "tmux", []string{"select-pane", "-T", "repo", "-t", "%6"}) {
		t.Fatalf("commands = %#v, want idle title restore", commands)
	}
	if containsAICommand(commands, "notify-send") {
		t.Fatalf("commands = %#v, did not expect notify-send after settled busy", commands)
	}
}

func TestAIReplyTitleIgnoresProjmuxAttentionMarkers(t *testing.T) {
	for _, title := range []string{"✳ repo", "✔ repo"} {
		if isAIReplyTitle(title) {
			t.Fatalf("isAIReplyTitle(%q) = true, want false for projmux marker", title)
		}
	}
}

type capturingAIRunner struct {
	options intfzf.Options
	result  intfzf.Result
	err     error
}

func (r *capturingAIRunner) Run(options intfzf.Options) (intfzf.Result, error) {
	r.options = options
	return r.result, r.err
}

type recordedAICommand struct {
	name string
	args []string
}

type aiCommandRecorder struct {
	commands []recordedAICommand
}

func testAICommand(home string) *aiCommand {
	recorder := &aiCommandRecorder{}
	cmd := &aiCommand{
		runner:     &capturingAIRunner{},
		executable: func() (string, error) { return "/tmp/projmux", nil },
		lookupEnv: func(name string) string {
			switch name {
			case "HOME":
				return home
			default:
				return ""
			}
		},
		homeDir: func() (string, error) { return home, nil },
		runCommand: func(_ context.Context, name string, args ...string) error {
			recorder.commands = append(recorder.commands, recordedAICommand{name: name, args: append([]string(nil), args...)})
			return nil
		},
		readCommand: func(context.Context, string, ...string) ([]byte, error) {
			return nil, os.ErrNotExist
		},
	}
	cmd.now = func() time.Time { return time.Unix(0, 0) }
	cmd.sleep = func(time.Duration) {}
	aiRecorders[cmd] = recorder
	return cmd
}

var aiRecorders = map[*aiCommand]*aiCommandRecorder{}

func cmdRecorder(cmd *aiCommand) *aiCommandRecorder {
	return aiRecorders[cmd]
}

func readModeFile(t *testing.T, home string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(home, ".config", "dotfiles", "tmux-ai-split-mode"))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func containsAICommand(commands []recordedAICommand, name string) bool {
	for _, command := range commands {
		if command.name == name {
			return true
		}
	}
	return false
}

func containsAICommandArgs(commands []recordedAICommand, name string, prefix []string) bool {
	for _, command := range commands {
		if command.name != name || len(command.args) < len(prefix) {
			continue
		}
		if reflect.DeepEqual(command.args[:len(prefix)], prefix) {
			return true
		}
	}
	return false
}

func writeExecutable(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func containsAICommandArg(commands []recordedAICommand, arg string) bool {
	for _, command := range commands {
		if slices.Contains(command.args, arg) {
			return true
		}
	}
	return false
}

func containsAICommandArgSubstring(commands []recordedAICommand, value string) bool {
	for _, command := range commands {
		for _, commandArg := range command.args {
			if strings.Contains(commandArg, value) {
				return true
			}
		}
	}
	return false
}

func paneGeometryIDs(panes []aiPaneGeometry) []string {
	ids := make([]string, 0, len(panes))
	for _, pane := range panes {
		ids = append(ids, pane.id)
	}
	return ids
}
