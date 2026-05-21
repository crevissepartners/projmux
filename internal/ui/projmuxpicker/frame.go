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
//
// ClickValue is the picker Result.Value emitted when the chip is clicked
// with the primary mouse button. Empty ClickValue or a Disabled chip make
// the click a no-op so the picker keeps the keyboard chord and mouse
// click metaphors in lockstep.
type Chip struct {
	Label      string
	Active     bool
	Disabled   bool
	ClickValue string
}

// ChipHit reports the visible column range a chip occupies in the rendered
// chip strip. Columns are 1-based outer-frame coordinates so callers can
// match mouse SGR events directly (the leftmost border column is 1).
type ChipHit struct {
	Index    int
	Disabled bool
	ColStart int
	ColEnd   int
	Value    string
}

// ChipsTitlebarRow returns the 1-based outer-frame row on which the chip
// strip is rendered. The chip strip always sits on row 2 (top border is
// row 1) so callers can collapse the entire layout calculation into a
// single constant — kept as a function so the relation stays explicit and
// is easy to extend if the frame grows additional decoration rows.
func ChipsTitlebarRow() int {
	return 2
}

// ChipsHitRegions returns the click-target columns each non-blank chip
// occupies for the supplied innerWidth (frame inner width, i.e. outer
// width minus the two border cells). The layout mirrors
// frameTitlebarChipsLine: a single leading cell after the left border
// followed by each chip body (" Label ") separated by one-cell gaps. The
// caller is responsible for skipping disabled chips when matching, but
// disabled chips still appear in the slice so geometry stays stable.
func ChipsHitRegions(chips []Chip, innerWidth int) []ChipHit {
	if innerWidth < 4 || len(chips) == 0 {
		return nil
	}
	hits := make([]ChipHit, 0, len(chips))
	// Column 1 is the left border, column 2 is the chip-strip leading
	// padding cell, so the first chip body starts at column 3.
	cursor := 3
	maxBody := innerWidth - 2
	used := 0
	first := true
	for index, chip := range chips {
		label := strings.TrimSpace(chip.Label)
		if label == "" {
			continue
		}
		if !first {
			gapWidth := 1
			if used+gapWidth > maxBody {
				break
			}
			cursor += gapWidth
			used += gapWidth
		}
		first = false
		remaining := maxBody - used
		if remaining <= 2 {
			break
		}
		labelBudget := remaining - 2
		labelWidth := VisibleLen(label)
		if labelWidth > labelBudget {
			label = TruncateANSI(label, labelBudget)
			labelWidth = VisibleLen(label)
		}
		chipWidth := labelWidth + 2
		hits = append(hits, ChipHit{
			Index:    index,
			Disabled: chip.Disabled,
			ColStart: cursor,
			ColEnd:   cursor + chipWidth - 1,
			Value:    chip.ClickValue,
		})
		cursor += chipWidth
		used += chipWidth
	}
	return hits
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
		return frameTitlebarStyledLine(theme, strings.Repeat(" ", innerWidth))
	}
	labelWidthLimit := max(innerWidth-2, 1)
	label := TruncateANSI(title, labelWidthLimit)
	label = strings.ReplaceAll(label, Reset, Reset+TitlebarStart)
	titleBlock := " " + label + " "
	titleBlockWidth := VisibleLen(titleBlock)
	body := titleBlock + strings.Repeat(" ", max(innerWidth-titleBlockWidth, 0))
	return frameTitlebarStyledLine(theme, body)
}

func frameTitlebarChipsLine(theme Theme, innerWidth int, chips []Chip) string {
	if innerWidth < 4 {
		return frameTitlebarStyledLine(theme, strings.Repeat(" ", innerWidth))
	}
	rendered, used := renderChipStrip(chips, innerWidth-2)
	if used == 0 {
		return frameTitlebarStyledLine(theme, strings.Repeat(" ", innerWidth))
	}
	leading := " "
	pad := max(innerWidth-used-VisibleLen(leading), 0)
	body := leading + rendered + TitlebarStart + strings.Repeat(" ", pad)
	return frameTitlebarStyledLine(theme, body)
}

func frameTitlebarStyledLine(theme Theme, body string) string {
	// Keep both border cells and the interior padding explicitly styled so
	// terminal defaults cannot leak through titlebar, chip-strip, or divider gaps.
	return TitlebarRule + theme.Vertical + TitlebarStart + body + TitlebarRule + theme.Vertical + Reset
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
	return TitlebarRule + "├" + strings.Repeat(theme.Horizontal, innerWidth) + "┤" + Reset
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
