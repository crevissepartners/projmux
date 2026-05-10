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
	return r.ContentLayoutWithTitle(layout, "")
}

func (r Renderer) ContentLayoutWithTitle(layout Layout, title string) Layout {
	if layout.Rows <= 0 {
		layout.Rows = DefaultRows
	}
	if layout.Cols <= 0 {
		layout.Cols = DefaultCols
	}
	rows := layout.Rows - 2 - TitlebarRows(title)
	rows = max(rows, 1)
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
	titlebarRows := TitlebarRows(title)
	innerHeight := max(height-2-titlebarRows, 0)

	theme := r.Theme
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	fmt.Fprintf(w, "%s%s%s\r\n", theme.TopLeft, strings.Repeat(theme.Horizontal, innerWidth), theme.TopRight)
	if titlebarRows > 0 {
		fmt.Fprint(w, frameTitlebarLine(theme, innerWidth, title))
		fmt.Fprint(w, "\r\n")
		fmt.Fprint(w, frameTitlebarDivider(theme, innerWidth))
		fmt.Fprint(w, "\r\n")
	}
	for i := range innerHeight {
		line := ""
		if i < len(lines) {
			line = TruncateANSI(strings.TrimRight(lines[i], "\r"), innerWidth)
		}
		fmt.Fprintf(w, "%s%s%s\r\n", theme.Vertical, PadRight(line, innerWidth), theme.Vertical)
	}
	fmt.Fprintf(w, "%s%s%s", theme.BottomLeft, strings.Repeat(theme.Horizontal, innerWidth), theme.BottomRight)
}

func TitlebarRows(title string) int {
	if strings.TrimSpace(title) == "" {
		return 0
	}
	return 2
}

func frameTitlebarLine(theme Theme, innerWidth int, title string) string {
	title = strings.TrimSpace(title)
	if title == "" || innerWidth < 4 {
		return theme.Vertical + TitlebarStart + strings.Repeat(" ", innerWidth) + Reset + theme.Vertical
	}
	labelWidthLimit := max(innerWidth-2, 1)
	label := TruncateANSI(title, labelWidthLimit)
	label = strings.ReplaceAll(label, Reset, Reset+TitlebarStart)
	titleBlock := " " + label + " "
	titleBlockWidth := VisibleLen(titleBlock)
	return theme.Vertical +
		TitlebarStart +
		titleBlock +
		strings.Repeat(" ", max(innerWidth-titleBlockWidth, 0)) +
		Reset +
		theme.Vertical
}

func frameTitlebarDivider(theme Theme, innerWidth int) string {
	return "├" + TitlebarRule + strings.Repeat(theme.Horizontal, innerWidth) + Reset + "┤"
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
