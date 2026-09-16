// Package termview reads what tmux panes look like, for the web client.
//
// The Registry stores identity, not content, so tmux is the only thing that
// knows what a pane shows. This package asks it — `capture-pane -e` for the
// grid and `list-panes` for the geometry — and returns attribute runs rather
// than raw terminal output, so the browser never interprets control sequences.
//
// It only reads. Nothing here sends keys or pastes into a pane; operator input
// is a separate, deliberately gated surface.
package termview

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Runner runs one external command and returns its output.
//
// internal/integrations/tmux.ExecRunner satisfies it. It is an interface so
// the package can be exercised against a fake, and so the caller — not this
// package — decides how processes are spawned.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// paneRuntimePattern is tmux's own pane id spelling. Anything else is refused
// before it reaches tmux: a target like `session:window.pane` or `{last}`
// would resolve against whatever the server considers current, which is not
// an address a caller meant.
var paneRuntimePattern = regexp.MustCompile(`^%\d+$`)

// Timeouts bound one capture. The layout gets more because it runs one
// capture per pane on top of the listing.
const (
	paneCaptureTimeout   = 10 * time.Second
	layoutCaptureTimeout = 20 * time.Second
)

// Screen is one capture of a pane's visible grid.
type Screen struct {
	Pane    string    `json:"pane"`
	Width   int       `json:"width"`
	Height  int       `json:"height"`
	Title   string    `json:"title,omitempty"`
	Command string    `json:"command,omitempty"`
	Lines   [][]Run   `json:"lines"`
	At      time.Time `json:"at"`
	Cursor  [2]int    `json:"cursor"`
	Alt     bool      `json:"alt"`
}

// Run is a stretch of characters sharing one set of attributes.
type Run struct {
	Text      string `json:"t"`
	FG        string `json:"f,omitempty"`
	BG        string `json:"b,omitempty"`
	Bold      bool   `json:"bo,omitempty"`
	Dim       bool   `json:"d,omitempty"`
	Italic    bool   `json:"i,omitempty"`
	Underline bool   `json:"u,omitempty"`
	Reverse   bool   `json:"r,omitempty"`
}

// CapturePane reads one pane's visible grid.
//
// server is the tmux argv prefix that selects the server (for example
// `-L projmux`); it is prepended to every call so a capture can never land on
// whatever server the process environment happens to point at.
func CapturePane(ctx context.Context, runner Runner, server []string, runtimeID string) (*Screen, error) {
	if runner == nil {
		return nil, errors.New("tmux runner is required")
	}
	if !paneRuntimePattern.MatchString(runtimeID) {
		return nil, fmt.Errorf("pane runtime id %q is not a tmux pane id", runtimeID)
	}
	callCtx, cancel := context.WithTimeout(ctx, paneCaptureTimeout)
	defer cancel()

	const format = "#{pane_width}\t#{pane_height}\t#{pane_current_command}\t" +
		"#{cursor_x}\t#{cursor_y}\t#{alternate_on}\t#{pane_title}"
	meta, err := tmux(callCtx, runner, server, "display-message", "-p", "-t", runtimeID, format)
	if err != nil {
		return nil, fmt.Errorf("read pane %s: %w", runtimeID, err)
	}
	fields := strings.Split(strings.TrimRight(meta, "\n"), "\t")
	for len(fields) < 7 {
		fields = append(fields, "")
	}

	body, err := capturePane(callCtx, runner, server, runtimeID)
	if err != nil {
		return nil, fmt.Errorf("capture pane %s: %w", runtimeID, err)
	}

	screen := &Screen{
		Pane:    runtimeID,
		Width:   atoiOr(fields[0], 0),
		Height:  atoiOr(fields[1], 0),
		Command: fields[2],
		Cursor:  [2]int{atoiOr(fields[3], 0), atoiOr(fields[4], 0)},
		Alt:     fields[5] == "1",
		Title:   fields[6],
		At:      time.Now(),
	}
	screen.Lines = parseLines(body)
	return screen, nil
}

// capturePane returns the pane's grid with its escapes.
//
// -e keeps the SGR escapes, and -N preserves trailing spaces so a filled row
// keeps its background.
func capturePane(ctx context.Context, runner Runner, server []string, runtimeID string) (string, error) {
	return tmux(ctx, runner, server, "capture-pane", "-p", "-e", "-N", "-t", runtimeID)
}

// parseLines splits a capture into rows of runs.
func parseLines(body string) [][]Run {
	rows := strings.Split(strings.TrimRight(body, "\n"), "\n")
	out := make([][]Run, 0, len(rows))
	for _, row := range rows {
		out = append(out, parseSGR(row))
	}
	return out
}

// tmux runs one tmux command against the selected server.
//
// The prefix is copied rather than appended to, so a caller's slice with
// spare capacity is never written through.
func tmux(ctx context.Context, runner Runner, server []string, args ...string) (string, error) {
	argv := make([]string, 0, len(server)+len(args))
	argv = append(argv, server...)
	argv = append(argv, args...)
	out, err := runner.Run(ctx, "tmux", argv...)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
