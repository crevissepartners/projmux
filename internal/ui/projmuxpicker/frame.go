package projmuxpicker

import (
	"fmt"
	"io"
	"strings"
)

const (
	DefaultRows = 24
	DefaultCols = 80
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
		fmt.Fprint(w, SyncUpdateEnter+cursorHome+frame+"\r"+SyncUpdateLeave)
		r.previous = frame
		return
	}
	if r.previous == frame {
		return
	}
	var update strings.Builder
	update.WriteString(SyncUpdateEnter)
	writeFrameDiff(&update, r.previous, frame)
	update.WriteString("\r")
	update.WriteString(SyncUpdateLeave)
	fmt.Fprint(w, update.String())
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
	rows := max(layout.Rows-2, 1)
	cols := max(layout.Cols-2, 1)
	return Layout{Rows: rows, Cols: cols}
}

func (r Renderer) RenderFrame(w io.Writer, content string, layout Layout) {
	r.RenderFrameWithTitle(w, content, "", layout)
}

func (r Renderer) RenderFrameWithTitle(w io.Writer, content, title string, layout Layout) {
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
	innerHeight := max(height-2, 1)

	theme := r.Theme
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	fmt.Fprint(w, frameTopBorder(theme, innerWidth, title))
	fmt.Fprint(w, "\r\n")
	for i := range innerHeight {
		line := ""
		if i < len(lines) {
			line = TruncateANSI(strings.TrimRight(lines[i], "\r"), innerWidth)
		}
		fmt.Fprintf(w, "%s%s%s\r\n", theme.Vertical, PadRight(line, innerWidth), theme.Vertical)
	}
	fmt.Fprintf(w, "%s%s%s", theme.BottomLeft, strings.Repeat(theme.Horizontal, innerWidth), theme.BottomRight)
}

func frameTopBorder(theme Theme, innerWidth int, title string) string {
	title = strings.TrimSpace(title)
	if title == "" || innerWidth < 4 {
		return theme.TopLeft + strings.Repeat(theme.Horizontal, innerWidth) + theme.TopRight
	}
	labelWidthLimit := innerWidth - 2
	label := " " + TruncateANSI(title, labelWidthLimit) + " "
	labelWidth := VisibleLen(label)
	if labelWidth > innerWidth {
		label = TruncateANSI(label, innerWidth)
		labelWidth = VisibleLen(label)
	}
	leftRuleWidth := 1
	rightRuleWidth := max(innerWidth-leftRuleWidth-labelWidth, 0)
	return theme.TopLeft +
		strings.Repeat(theme.Horizontal, leftRuleWidth) +
		label +
		strings.Repeat(theme.Horizontal, rightRuleWidth) +
		theme.TopRight
}

func writeFrameDiff(w io.Writer, previous, next string) {
	previousLines := splitFrameLines(previous)
	nextLines := splitFrameLines(next)
	limit := max(len(previousLines), len(nextLines))
	for i := range limit {
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
