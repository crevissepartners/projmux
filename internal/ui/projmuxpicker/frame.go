package projmuxpicker

import (
	"fmt"
	"io"
	"strings"
)

const (
	DefaultRows = 30
	DefaultCols = 100
)

type Layout struct {
	Rows int
	Cols int
}

type Theme struct {
	TopLeft     string
	TopRight    string
	BottomLeft  string
	BottomRight string
	Horizontal  string
	Vertical    string
}

type Renderer struct {
	Theme Theme
}

const (
	SyncUpdateEnter = "\x1b[?2026h"
	SyncUpdateLeave = "\x1b[?2026l"
	cursorHome      = "\x1b[H"
)

type FrameUpdateRenderer struct {
	previous string
}

var DefaultTheme = Theme{
	TopLeft:     "╭",
	TopRight:    "╮",
	BottomLeft:  "╰",
	BottomRight: "╯",
	Horizontal:  "─",
	Vertical:    "│",
}

func NewRenderer(theme Theme) Renderer {
	if theme.TopLeft == "" {
		theme = DefaultTheme
	}
	return Renderer{Theme: theme}
}

func DefaultRenderer() Renderer {
	return NewRenderer(DefaultTheme)
}

func (r *FrameUpdateRenderer) Render(w io.Writer, frame string) {
	if r.previous == "" {
		fmt.Fprint(w, SyncUpdateEnter)
		defer fmt.Fprint(w, SyncUpdateLeave)
		fmt.Fprint(w, cursorHome+frame+"\r")
		r.previous = frame
		return
	}
	if r.previous == frame {
		return
	}
	fmt.Fprint(w, SyncUpdateEnter)
	defer fmt.Fprint(w, SyncUpdateLeave)
	writeFrameDiff(w, r.previous, frame)
	fmt.Fprint(w, "\r")
	r.previous = frame
}

func RenderFullFrameUpdate(w io.Writer, frame string) {
	fmt.Fprint(w, SyncUpdateEnter+cursorHome+frame+"\r"+SyncUpdateLeave)
}

func (r Renderer) ContentLayout(layout Layout) Layout {
	if layout.Rows <= 0 {
		layout.Rows = DefaultRows
	}
	if layout.Cols <= 0 {
		layout.Cols = DefaultCols
	}
	rows := layout.Rows - 2
	if rows < 1 {
		rows = 1
	}
	cols := layout.Cols - 2
	if cols < 1 {
		cols = 1
	}
	return Layout{Rows: rows, Cols: cols}
}

func (r Renderer) RenderFrame(w io.Writer, content string, layout Layout) {
	width := layout.Cols
	if width <= 0 {
		width = DefaultCols
	}
	if width < 4 {
		fmt.Fprint(w, content)
		return
	}
	height := layout.Rows
	if height <= 0 {
		height = DefaultRows
	}
	innerWidth := width - 2
	innerHeight := height - 2
	if innerHeight < 1 {
		innerHeight = 1
	}

	theme := r.Theme
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	fmt.Fprintf(w, "%s%s%s\r\n", theme.TopLeft, strings.Repeat(theme.Horizontal, innerWidth), theme.TopRight)
	for i := 0; i < innerHeight; i++ {
		line := ""
		if i < len(lines) {
			line = TruncateANSI(strings.TrimRight(lines[i], "\r"), innerWidth)
		}
		fmt.Fprintf(w, "%s%s%s\r\n", theme.Vertical, PadRight(line, innerWidth), theme.Vertical)
	}
	fmt.Fprintf(w, "%s%s%s", theme.BottomLeft, strings.Repeat(theme.Horizontal, innerWidth), theme.BottomRight)
}

func writeFrameDiff(w io.Writer, previous, next string) {
	previousLines := splitFrameLines(previous)
	nextLines := splitFrameLines(next)
	limit := len(nextLines)
	if len(previousLines) > limit {
		limit = len(previousLines)
	}
	for i := 0; i < limit; i++ {
		previousLine := ""
		if i < len(previousLines) {
			previousLine = previousLines[i]
		}
		nextLine := ""
		if i < len(nextLines) {
			nextLine = nextLines[i]
		}
		if previousLine == nextLine {
			continue
		}
		fmt.Fprintf(w, "\x1b[%d;1H%s", i+1, nextLine)
	}
}

func splitFrameLines(frame string) []string {
	if strings.Contains(frame, "\r\n") {
		return strings.Split(frame, "\r\n")
	}
	return strings.Split(frame, "\n")
}
