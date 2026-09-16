package termview

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// A layout preview is the whole tmux Window at once: every Pane in the place
// tmux actually put it, with what is on it.
//
// One pane's grid says what a session is doing; the arrangement says where it
// sits, which is the thing a person recognizes a window by. tmux publishes the
// geometry per pane in cells (`pane_left`, `pane_top`, `pane_width`,
// `pane_height`), so the preview is a scaled copy of the real layout rather
// than a guess reconstructed from a layout string.

// windowRuntimePattern is tmux's window id spelling.
var windowRuntimePattern = regexp.MustCompile(`^@\d+$`)

// WindowLayout is one capture of a whole window.
type WindowLayout struct {
	Window string       `json:"window"`
	Width  int          `json:"width"`
	Height int          `json:"height"`
	Panes  []LayoutPane `json:"panes"`
	At     time.Time    `json:"at"`
}

// LayoutPane is one pane's place in the window, and its contents.
type LayoutPane struct {
	Runtime string  `json:"runtime"`
	X       int     `json:"x"`
	Y       int     `json:"y"`
	Width   int     `json:"width"`
	Height  int     `json:"height"`
	Active  bool    `json:"active"`
	Command string  `json:"command,omitempty"`
	Title   string  `json:"title,omitempty"`
	Lines   [][]Run `json:"lines,omitempty"`
}

// CaptureWindowLayout reads every pane of one window, with its geometry.
//
// withContents decides whether each pane is also captured. The geometry alone
// is one tmux call for the whole window; the contents are one more per pane,
// so a caller that only needs the arrangement should not pay for them.
//
// server is the tmux argv prefix that selects the server, as for CapturePane.
func CaptureWindowLayout(ctx context.Context, runner Runner, server []string, windowRuntimeID string, withContents bool) (*WindowLayout, error) {
	if runner == nil {
		return nil, errors.New("tmux runner is required")
	}
	if !windowRuntimePattern.MatchString(windowRuntimeID) {
		return nil, fmt.Errorf("window runtime id %q is not a tmux window id", windowRuntimeID)
	}
	callCtx, cancel := context.WithTimeout(ctx, layoutCaptureTimeout)
	defer cancel()

	const format = "#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}\t" +
		"#{pane_active}\t#{pane_current_command}\t#{pane_title}"
	listing, err := tmux(callCtx, runner, server, "list-panes", "-t", windowRuntimeID, "-F", format)
	if err != nil {
		return nil, fmt.Errorf("read window %s: %w", windowRuntimeID, err)
	}

	layout := &WindowLayout{Window: windowRuntimeID, At: time.Now()}
	for line := range strings.SplitSeq(strings.TrimRight(listing, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		for len(fields) < 8 {
			fields = append(fields, "")
		}
		pane := LayoutPane{
			Runtime: fields[0],
			X:       atoiOr(fields[1], 0),
			Y:       atoiOr(fields[2], 0),
			Width:   atoiOr(fields[3], 0),
			Height:  atoiOr(fields[4], 0),
			Active:  fields[5] == "1",
			Command: fields[6],
			Title:   fields[7],
		}
		// The listing comes from tmux, but it is still an id that goes back
		// into a target; one that is not a pane id is not captured.
		if withContents && paneRuntimePattern.MatchString(pane.Runtime) {
			// A pane that vanishes between the listing and its capture is
			// skipped rather than failing the whole read: the next poll settles
			// it.
			if body, err := capturePane(callCtx, runner, server, pane.Runtime); err == nil {
				pane.Lines = parseLines(body)
			}
		}
		// The window's own size is the far edge of its panes. tmux does not
		// report it on a pane, and the separator rows between panes mean the
		// sum of the pane sizes is not it either.
		if right := pane.X + pane.Width; right > layout.Width {
			layout.Width = right
		}
		if bottom := pane.Y + pane.Height; bottom > layout.Height {
			layout.Height = bottom
		}
		layout.Panes = append(layout.Panes, pane)
	}
	if len(layout.Panes) == 0 {
		return nil, fmt.Errorf("window %s has no panes to read", windowRuntimeID)
	}
	return layout, nil
}
