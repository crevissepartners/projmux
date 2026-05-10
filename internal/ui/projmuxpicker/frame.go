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

// Chip represents one segment of a titlebar chip strip. The renderer
// colours active chips with the tmux window-status active tone and inactive
// chips with the inactive tone, so the popup tab metaphor visually matches
// the tmux window list. Disabled chips render dim and convey "tab exists
// but is not selectable" without breaking the chip row geometry.
type Chip struct {
	Label    string
	Active   bool
	Disabled bool
}

func (r Renderer) ContentLayout(layout Layout) Layout {
	return r.ContentLayoutWithTitle(layout, "")
}

func (r Renderer) ContentLayoutWithTitle(layout Layout, title string) Layout {
	return r.contentLayoutReserving(layout, TitlebarRows(title))
}

// ContentLayoutWithChips matches ContentLayoutWithTitle but reserves a
// titlebar row whenever at least one non-empty chip is supplied.
func (r Renderer) ContentLayoutWithChips(layout Layout, chips []Chip) Layout {
	return r.contentLayoutReserving(layout, ChipsTitlebarRows(chips))
}

func (r Renderer) contentLayoutReserving(layout Layout, titlebarRows int) Layout {
	if layout.Rows <= 0 {
		layout.Rows = DefaultRows
	}
	if layout.Cols <= 0 {
		layout.Cols = DefaultCols
	}
	rows := layout.Rows - 2 - titlebarRows
	rows = max(rows, 1)
	cols := max(layout.Cols-2, 1)
	return Layout{Rows: rows, Cols: cols}
}

func (r Renderer) RenderFrame(w io.Writer, content string, layout Layout) {
	r.RenderFrameWithTitle(w, content, "", layout)
}

func (r Renderer) RenderFrameWithTitle(w io.Writer, content, title string, layout Layout) {
	r.renderFrame(w, content, layout, frameTitleHeader{title: title})
}

// RenderFrameWithChips renders a frame whose titlebar is a horizontal chip
// strip instead of a single title string. Each chip occupies a contiguous
// block with a one-cell gap separator. When chips is empty or contains only
// blank labels, this is equivalent to RenderFrame.
func (r Renderer) RenderFrameWithChips(w io.Writer, content string, chips []Chip, layout Layout) {
	r.renderFrame(w, content, layout, frameTitleHeader{chips: chips})
}

type frameTitleHeader struct {
	title string
	chips []Chip
}

func (h frameTitleHeader) titlebarRows() int {
	if len(h.chips) > 0 {
		return ChipsTitlebarRows(h.chips)
	}
	return TitlebarRows(h.title)
}

func (h frameTitleHeader) titlebarLine(theme Theme, innerWidth int) string {
	if len(h.chips) > 0 {
		return frameTitlebarChipsLine(theme, innerWidth, h.chips)
	}
	return frameTitlebarLine(theme, innerWidth, h.title)
}

func (r Renderer) renderFrame(w io.Writer, content string, layout Layout, header frameTitleHeader) {
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
	titlebarRows := header.titlebarRows()
	innerHeight := max(height-2-titlebarRows, 0)

	theme := r.Theme
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	fmt.Fprintf(w, "%s%s%s\r\n", theme.TopLeft, strings.Repeat(theme.Horizontal, innerWidth), theme.TopRight)
	if titlebarRows > 0 {
		fmt.Fprint(w, header.titlebarLine(theme, innerWidth))
		fmt.Fprint(w, "\r\n")
		fmt.Fprint(w, frameTitlebarDivider(theme, innerWidth))
		fmt.Fprint(w, "\r\n")
	}
	for i := range innerHeight {
		line := ""
		if i < len(lines) {
			line = TruncateANSI(strings.TrimRight(lines[i], "\r"), innerWidth)
		}
		fmt.Fprintf(w, "%s%s%s\r\n", theme.Vertical, PadStyledLine(line, innerWidth), theme.Vertical)
	}
	fmt.Fprintf(w, "%s%s%s", theme.BottomLeft, strings.Repeat(theme.Horizontal, innerWidth), theme.BottomRight)
}

func TitlebarRows(title string) int {
	if strings.TrimSpace(title) == "" {
		return 0
	}
	return 2
}

// ChipsTitlebarRows reports how many frame rows the chip strip occupies
// (chip row + divider). Returns 0 when no chip has visible content so an
// empty chip slice degrades to "no titlebar" rather than rendering a blank
// row.
func ChipsTitlebarRows(chips []Chip) int {
	for _, chip := range chips {
		if strings.TrimSpace(chip.Label) != "" {
			return 2
		}
	}
	return 0
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

func frameTitlebarChipsLine(theme Theme, innerWidth int, chips []Chip) string {
	if innerWidth < 4 {
		return theme.Vertical + TitlebarStart + strings.Repeat(" ", innerWidth) + Reset + theme.Vertical
	}
	rendered, used := renderChipStrip(chips, innerWidth-1)
	if used == 0 {
		return theme.Vertical + TitlebarStart + strings.Repeat(" ", innerWidth) + Reset + theme.Vertical
	}
	leading := " "
	pad := max(innerWidth-used-VisibleLen(leading), 0)
	return theme.Vertical +
		TitlebarStart +
		leading +
		rendered +
		TitlebarStart +
		strings.Repeat(" ", pad) +
		Reset +
		theme.Vertical
}

// renderChipStrip lays out the chip slice into a single visible-width-bound
// string and returns the visible width consumed. Chips render with a single
// cell of padding on each side ("[ Label ]") and are separated by a single
// gap cell coloured with the inactive-chip background so the gap reads as
// part of the tab strip rather than as titlebar void.
func renderChipStrip(chips []Chip, maxWidth int) (string, int) {
	if maxWidth <= 0 {
		return "", 0
	}
	var out strings.Builder
	used := 0
	first := true
	for _, chip := range chips {
		label := strings.TrimSpace(chip.Label)
		if label == "" {
			continue
		}
		if !first {
			gap := chipGapSegment()
			gapWidth := 1
			if used+gapWidth > maxWidth {
				break
			}
			out.WriteString(gap)
			used += gapWidth
		}
		first = false
		labelWidth := VisibleLen(label)
		remaining := maxWidth - used
		if remaining <= 2 {
			break
		}
		// Reserve two cells for the padding around the label.
		labelBudget := remaining - 2
		if labelWidth > labelBudget {
			label = TruncateANSI(label, labelBudget)
			labelWidth = VisibleLen(label)
		}
		chipWidth := labelWidth + 2
		out.WriteString(chipSegment(chip, label))
		used += chipWidth
	}
	return out.String(), used
}

func chipSegment(chip Chip, label string) string {
	start := ChipInactiveStart
	switch {
	case chip.Disabled:
		start = ChipDisabledStart
	case chip.Active:
		start = ChipActiveStart
	}
	return start + " " + label + " " + Reset
}

func chipGapSegment() string {
	return ChipInactiveStart + " " + Reset
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
