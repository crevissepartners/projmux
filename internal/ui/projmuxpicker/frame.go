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
	cols := layout.Cols - 4
	if cols < 20 {
		cols = layout.Cols
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
	fmt.Fprintf(w, "%s%s%s\r\n", theme.BottomLeft, strings.Repeat(theme.Horizontal, innerWidth), theme.BottomRight)
}
