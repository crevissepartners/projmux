package termview_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/web/termview"
)

// The production runner is the one callers are expected to pass.
var _ termview.Runner = tmux.ExecRunner{}

// fakeRunner answers by subcommand and records every argv.
type fakeRunner struct {
	calls   [][]string
	answers map[string]string
	fail    map[string]bool
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "tmux" {
		return nil, fmt.Errorf("unexpected command %s", name)
	}
	f.calls = append(f.calls, append([]string(nil), args...))
	sub := ""
	for i, arg := range args {
		if !strings.HasPrefix(arg, "-") && (i == 0 || args[i-1] != "-L") {
			sub = arg
			break
		}
	}
	key := sub + " " + args[len(args)-1]
	if f.fail[key] {
		return nil, errors.New("pane gone")
	}
	return []byte(f.answers[key]), nil
}

func TestCapturePaneValidatesAndPrefixesServer(t *testing.T) {
	runner := &fakeRunner{answers: map[string]string{}}
	for _, id := range []string{"", "1", "%1a", "@1", "sess:0.1", "%1;kill-server"} {
		if _, err := termview.CapturePane(context.Background(), runner, []string{"-L", "x"}, id); err == nil {
			t.Errorf("pane id %q accepted", id)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatalf("invalid ids reached tmux: %v", runner.calls)
	}
	if _, err := termview.CapturePane(context.Background(), nil, nil, "%1"); err == nil {
		t.Fatal("nil runner accepted")
	}

	// The fake keys an answer by subcommand and last argument: the format for
	// display-message, the target for capture-pane.
	runner.answers["display-message "+displayFormat] = "80\t24\tzsh\t3\t1\t0\ttitle\n"
	runner.answers["capture-pane %7"] = "\x1b[1mhi\x1b[0m\nsecond\n"
	// Spare capacity lets the test see whether the prefix is written through.
	server := make([]string, 2, 8)
	copy(server, []string{"-L", "projmux"})

	screen, err := termview.CapturePane(context.Background(), runner, server, "%7")
	if err != nil {
		t.Fatal(err)
	}
	if screen.Pane != "%7" || screen.Width != 80 || screen.Height != 24 || screen.Command != "zsh" ||
		screen.Cursor != [2]int{3, 1} || screen.Alt || screen.Title != "title" {
		t.Fatalf("screen = %+v", screen)
	}
	wantLines := [][]termview.Run{{{Text: "hi", Bold: true}}, {{Text: "second"}}}
	if !reflect.DeepEqual(screen.Lines, wantLines) {
		t.Fatalf("lines = %#v", screen.Lines)
	}
	for _, call := range runner.calls {
		if len(call) < 2 || call[0] != "-L" || call[1] != "projmux" {
			t.Fatalf("call without server prefix: %v", call)
		}
	}
	if server[:cap(server)][2] != "" {
		t.Fatal("caller's server slice was written through")
	}
}

const displayFormat = "#{pane_width}\t#{pane_height}\t#{pane_current_command}\t" +
	"#{cursor_x}\t#{cursor_y}\t#{alternate_on}\t#{pane_title}"

const listFormat = "#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}\t" +
	"#{pane_active}\t#{pane_current_command}\t#{pane_title}"

func TestCaptureWindowLayoutWithFake(t *testing.T) {
	runner := &fakeRunner{
		answers: map[string]string{
			"list-panes " + listFormat: "%1\t0\t0\t40\t24\t1\tzsh\ta\n%2\t41\t0\t39\t12\t0\tvim\tb\n%3\t41\t13\t39\t11\t0\ttop\tc\n",
			"capture-pane %1":          "left\n",
			"capture-pane %2":          "\x1b[31mright\n",
		},
		fail: map[string]bool{"capture-pane %3": true},
	}
	if _, err := termview.CaptureWindowLayout(context.Background(), runner, nil, "%1", true); err == nil {
		t.Fatal("pane id accepted as a window id")
	}

	layout, err := termview.CaptureWindowLayout(context.Background(), runner, []string{"-L", "p"}, "@4", true)
	if err != nil {
		t.Fatal(err)
	}
	if layout.Window != "@4" || layout.Width != 80 || layout.Height != 24 || len(layout.Panes) != 3 {
		t.Fatalf("layout = %+v", layout)
	}
	if p := layout.Panes[1]; p.Runtime != "%2" || p.X != 41 || p.Active || p.Command != "vim" ||
		!reflect.DeepEqual(p.Lines, [][]termview.Run{{{Text: "right", FG: "#ff6b6b"}}}) {
		t.Fatalf("pane 2 = %+v", p)
	}
	if !layout.Panes[0].Active || layout.Panes[2].Lines != nil {
		t.Fatalf("panes = %+v", layout.Panes)
	}

	runner.calls = nil
	if _, err := termview.CaptureWindowLayout(context.Background(), runner, nil, "@4", false); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("geometry-only read made %d calls", len(runner.calls))
	}

	empty := &fakeRunner{answers: map[string]string{}}
	if _, err := termview.CaptureWindowLayout(context.Background(), empty, nil, "@4", false); err == nil {
		t.Fatal("empty window accepted")
	}
}

// TestCaptureRealTmux runs against a private tmux server that this test owns.
// It never addresses the default server or the projmux socket.
func TestCaptureRealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	socket := fmt.Sprintf("pf-termview-test-%d", os.Getpid())
	server := []string{"-L", socket}
	runner := tmux.ExecRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	run := func(args ...string) string {
		t.Helper()
		out, err := runner.Run(ctx, "tmux", append(append([]string(nil), server...), args...)...)
		if err != nil {
			t.Fatalf("tmux %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	var socketPath string
	t.Cleanup(func() {
		// Exactly this test's socket; a failure means it is already gone.
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
		// kill-server leaves the socket file behind.
		if socketPath != "" && strings.HasSuffix(socketPath, "/"+socket) {
			_ = os.Remove(socketPath)
		}
	})

	run("-f", "/dev/null", "new-session", "-d", "-s", "t", "-x", "80", "-y", "24",
		`printf "\033[31mred\033[0m\n"; sleep 30`)
	socketPath = run("display-message", "-p", "-t", "t", "#{socket_path}")
	pane := run("list-panes", "-t", "t", "-F", "#{pane_id}")
	window := run("display-message", "-p", "-t", pane, "#{window_id}")

	var screen *termview.Screen
	deadline := time.Now().Add(10 * time.Second)
	for {
		var err error
		screen, err = termview.CapturePane(ctx, runner, server, pane)
		if err != nil {
			t.Fatal(err)
		}
		if len(screen.Lines) > 0 && len(screen.Lines[0]) > 0 && strings.TrimSpace(screen.Lines[0][0].Text) != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane never showed output: %+v", screen)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if screen.Width != 80 || screen.Height != 24 || screen.Pane != pane {
		t.Fatalf("screen = %+v", screen)
	}
	first := screen.Lines[0][0]
	if first.Text != "red" || first.FG != "#ff6b6b" {
		t.Fatalf("first run = %+v", first)
	}

	run("split-window", "-h", "-t", pane, "sleep 30")
	layout, err := termview.CaptureWindowLayout(ctx, runner, server, window, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(layout.Panes) != 2 || layout.Width != 80 || layout.Height != 24 {
		t.Fatalf("layout = %+v", layout)
	}
	if layout.Panes[0].Lines == nil || layout.Panes[1].X == 0 {
		t.Fatalf("panes = %+v", layout.Panes)
	}
}
