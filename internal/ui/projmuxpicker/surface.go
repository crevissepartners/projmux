package projmuxpicker

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

const gapSentinel = "\x00projmux-picker-gap\x00"

type Row struct {
	Label     string
	MetaLines []string
}

func WriteContentWithFooter(w io.Writer, top, main, footer string, layout Layout) {
	var screen strings.Builder
	screen.WriteString(top)
	screen.WriteString(main)
	footerLines := FooterBlockLines(footer, layout.Cols)
	if len(footerLines) == 0 {
		fmt.Fprint(w, screen.String())
		return
	}
	remaining := layout.Rows - RenderedTextLineCount(screen.String()) - len(footerLines)
	for range remaining {
		fmt.Fprintln(&screen)
	}
	for _, line := range footerLines {
		fmt.Fprintln(&screen, line)
	}
	fmt.Fprint(w, screen.String())
}

func FooterBlockLines(footer string, cols int) []string {
	return FooterBlockLinesWithTheme(DefaultTheme, footer, cols)
}

func FooterBlockLinesWithTheme(pickerTheme Theme, footer string, cols int) []string {
	footer = strings.TrimSpace(footer)
	if footer == "" {
		return nil
	}
	if cols <= 0 {
		cols = DefaultCols
	}
	lines := []string{SeparatorLineWithTheme(pickerTheme, cols)}
	for line := range strings.SplitSeq(footer, "\n") {
		lines = append(lines, ChromeLineWithTheme(pickerTheme, strings.TrimRight(line, "\r"), cols))
	}
	return lines
}

func HeaderLine(header string, cols int) string {
	return HeaderLineWithTheme(DefaultTheme, header, cols)
}

func HeaderLineWithTheme(pickerTheme Theme, header string, cols int) string {
	return ChromeLineWithTheme(pickerTheme, strings.TrimRight(header, "\r"), cols)
}

func ChromeLine(line string, cols int) string {
	return ChromeLineWithTheme(DefaultTheme, line, cols)
}

func ChromeLineWithTheme(pickerTheme Theme, line string, cols int) string {
	if cols <= 0 {
		cols = DefaultCols
	}
	return PadStyledLineWithTheme(pickerTheme, TruncateANSI(line, cols), cols)
}

func SeparatorLine(cols int) string {
	return SeparatorLineWithTheme(DefaultTheme, cols)
}

func SeparatorLineWithTheme(pickerTheme Theme, cols int) string {
	if cols <= 0 {
		cols = DefaultCols
	}
	return themeMuted(pickerTheme) + strings.Repeat(GapLine, cols) + Reset
}

func RenderedTextLineCount(value string) int {
	value = strings.TrimSuffix(value, "\n")
	if value == "" {
		return 0
	}
	return len(strings.Split(value, "\n"))
}

func PromptLine(prompt, query string, matches, total, cols int) string {
	return PromptLineWithRenderedQuery(prompt, query, query, matches, total, cols)
}

func PromptLineWithCursor(prompt, query string, cursor, matches, total, cols int) string {
	return PromptLineWithRenderedQuery(prompt, query, QueryWithCursor(query, cursor), matches, total, cols)
}

func PromptLineWithCursorLabel(searchLabel, prompt, query string, cursor, matches, total, cols int) string {
	return PromptLineWithRenderedQueryLabel(searchLabel, prompt, query, QueryWithCursor(query, cursor), matches, total, cols)
}

func PromptLineWithRenderedQuery(prompt, query, renderedQuery string, matches, total, cols int) string {
	return PromptLineWithRenderedQueryLabel("Search", prompt, query, renderedQuery, matches, total, cols)
}

func PromptLineWithRenderedQueryLabel(searchLabel, prompt, query, renderedQuery string, matches, total, cols int) string {
	return PromptLineWithRenderedQueryLabelAndTheme(DefaultTheme, searchLabel, prompt, query, renderedQuery, matches, total, cols)
}

func PromptLineWithRenderedQueryLabelAndTheme(pickerTheme Theme, searchLabel, prompt, query, renderedQuery string, matches, total, cols int) string {
	prompt = strings.TrimRight(prompt, " ")
	input := strings.TrimRight(prompt+" "+renderedQuery, " ")
	searchLabel = strings.TrimSpace(searchLabel)
	if searchLabel == "" {
		searchLabel = "Search"
	}
	muted := themeMuted(pickerTheme)
	line := muted + searchLabel + Reset + " " + input
	info := strconv.Itoa(matches)
	if query != "" || matches != total {
		info = fmt.Sprintf("%d/%d", matches, total)
	}
	info = muted + info + Reset
	if cols <= 0 {
		cols = DefaultCols
	}
	padding := cols - VisibleLen(line) - VisibleLen(info)
	if padding < 2 {
		return PadStyledLineWithTheme(pickerTheme, line+"  "+info, cols)
	}
	return PadStyledLineWithTheme(pickerTheme, line+strings.Repeat(" ", padding)+info, cols)
}

func QueryWithCursor(query string, cursor int) string {
	return QueryWithCursorAndTheme(DefaultTheme, query, cursor)
}

func QueryWithCursorAndTheme(pickerTheme Theme, query string, cursor int) string {
	runes := []rune(query)
	cursor = clampCursor(runes, cursor)
	cursorStart := themeCursor(pickerTheme)
	if cursor == len(runes) {
		return string(runes) + cursorStart + " " + Reset
	}
	var out strings.Builder
	for i, r := range runes {
		if i == cursor {
			out.WriteString(cursorStart)
			out.WriteRune(r)
			out.WriteString(Reset)
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func RowLineCount(row Row) int {
	count := len(strings.Split(row.Label, "\n"))
	if count == 0 {
		count = 1
	}
	for _, meta := range row.MetaLines {
		if strings.TrimSpace(meta) != "" {
			count++
		}
	}
	return count
}

func RenderedListLineCount(rows []Row, start, end int, multiLine bool) int {
	if start < 0 {
		start = 0
	}
	if end > len(rows) {
		end = len(rows)
	}
	if start >= end {
		return 0
	}
	total := 0
	for i := start; i < end; i++ {
		total += RowLineCount(rows[i])
	}
	if multiLine {
		total += end - start - 1
	}
	return total
}

func InteractiveListLines(rows []Row, start, end, selected int, multiLine bool) []string {
	return InteractiveListLinesWithTheme(DefaultTheme, rows, start, end, selected, multiLine)
}

func InteractiveListLinesWithTheme(pickerTheme Theme, rows []Row, start, end, selected int, multiLine bool) []string {
	if start < 0 {
		start = 0
	}
	if end > len(rows) {
		end = len(rows)
	}
	lines := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		lines = append(lines, InteractiveRowLinesWithTheme(pickerTheme, rows[i], i == selected, multiLine)...)
		if multiLine && i < end-1 {
			lines = append(lines, gapSentinel)
		}
	}
	return lines
}

func ListLinesWithScrollbar(lines []string, total, start, end, width int) []string {
	return ListLinesWithScrollbarRows(lines, total, start, end, width, len(lines))
}

func ListLinesWithScrollbarRows(lines []string, total, start, end, width, rows int) []string {
	return ListLinesWithScrollbarRowsWithTheme(DefaultTheme, lines, total, start, end, width, rows)
}

func ListLinesWithScrollbarRowsWithTheme(pickerTheme Theme, lines []string, total, start, end, width, rows int) []string {
	visible := end - start
	if rows < len(lines) {
		rows = len(lines)
	}
	hasScrollbar := total > visible && rows > 0 && width > 1
	contentWidth, markerWidth := listContentAndMarkerWidth(width)
	if !hasScrollbar {
		rendered := make([]string, 0, max(len(lines), rows))
		for _, line := range lines {
			rendered = append(rendered, renderListLineWithMarker(pickerTheme, line, contentWidth, strings.Repeat(" ", markerWidth)))
		}
		for len(rendered) < rows {
			rendered = append(rendered, PadRight("", width))
		}
		return rendered
	}
	thumbStart, thumbEnd := scrollbarThumbRange(total, visible, start, rows)
	rendered := make([]string, 0, rows)
	for i := range rows {
		marker := " "
		if i >= thumbStart && i < thumbEnd {
			marker = Scrollbar
		}
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		rendered = append(rendered, renderListLineWithMarker(pickerTheme, line, contentWidth, marker))
	}
	return rendered
}

func listContentAndMarkerWidth(width int) (int, int) {
	if width <= 1 {
		return width, 0
	}
	return width - 1, 1
}

func renderListLineWithMarker(pickerTheme Theme, line string, contentWidth int, marker string) string {
	if contentWidth <= 0 {
		return marker
	}
	return RenderableListLineWithTheme(pickerTheme, line, contentWidth) + marker
}

func scrollbarThumbRange(total, visible, start, track int) (int, int) {
	if total <= 0 || visible <= 0 || track <= 0 {
		return 0, 0
	}
	if visible >= total {
		return 0, track
	}
	thumb := min(max((track*visible+total-1)/total, 1), track)
	maxStart := total - visible
	maxThumbStart := track - thumb
	thumbStart := 0
	if maxStart > 0 && maxThumbStart > 0 {
		thumbStart = (start*maxThumbStart + maxStart/2) / maxStart
	}
	if thumbStart < 0 {
		thumbStart = 0
	}
	if thumbStart > maxThumbStart {
		thumbStart = maxThumbStart
	}
	return thumbStart, thumbStart + thumb
}

func RenderableListLines(lines []string, width int) []string {
	return RenderableListLinesWithTheme(DefaultTheme, lines, width)
}

func RenderableListLinesWithTheme(pickerTheme Theme, lines []string, width int) []string {
	rendered := make([]string, 0, len(lines))
	for _, line := range lines {
		rendered = append(rendered, RenderableListLineWithTheme(pickerTheme, line, width))
	}
	return rendered
}

func RenderableListLine(line string, width int) string {
	return RenderableListLineWithTheme(DefaultTheme, line, width)
}

func RenderableListLineWithTheme(pickerTheme Theme, line string, width int) string {
	if line != gapSentinel {
		return PadStyledLineWithTheme(pickerTheme, TruncateANSI(line, width), width)
	}
	if width <= 0 {
		return ""
	}
	return SeparatorLineWithTheme(pickerTheme, width)
}

func PadStyledLine(line string, width int) string {
	return PadStyledLineWithTheme(DefaultTheme, line, width)
}

func PadStyledLineWithTheme(pickerTheme Theme, line string, width int) string {
	if width <= 0 {
		return closeStyledLine(line)
	}
	visible := VisibleLen(line)
	if visible > width {
		line = TruncateANSI(line, width)
		visible = VisibleLen(line)
	}
	if visible >= width {
		return closeStyledLine(line)
	}
	padding := strings.Repeat(" ", width-visible)
	if strings.HasSuffix(line, Reset) && padsInsideFinalStyleWithTheme(pickerTheme, line) {
		return strings.TrimSuffix(line, Reset) + padding + Reset
	}
	if hasActiveStyle(line) {
		if padsInsideFinalStyleWithTheme(pickerTheme, line) {
			return line + padding + Reset
		}
		return line + Reset + padding
	}
	return line + padding
}

func padsInsideFinalStyle(line string) bool {
	return padsInsideFinalStyleWithTheme(DefaultTheme, line)
}

func padsInsideFinalStyleWithTheme(pickerTheme Theme, line string) bool {
	return strings.Contains(line, themeSelected(pickerTheme)) || strings.Contains(line, themeCursor(pickerTheme))
}

func InteractiveRowLines(row Row, selected, multiLine bool) []string {
	return InteractiveRowLinesWithTheme(DefaultTheme, row, selected, multiLine)
}

func InteractiveRowLinesWithTheme(pickerTheme Theme, row Row, selected, multiLine bool) []string {
	lines := strings.Split(row.Label, "\n")
	prefix := "  "
	if selected {
		prefix = themePointer(pickerTheme)
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	rendered := make([]string, 0, len(lines)+len(row.MetaLines))
	first := fmt.Sprintf("%s%s", prefix, strings.TrimRight(lines[0], "\r"))
	if selected {
		first = SelectedLineWithTheme(pickerTheme, prefix, strings.TrimRight(lines[0], "\r"))
	}
	rendered = append(rendered, first)
	for _, line := range lines[1:] {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if selected && multiLine {
			line = SelectedLineWithTheme(pickerTheme, themeContinuation(pickerTheme), line)
		} else {
			line = "  " + line
		}
		rendered = append(rendered, line)
	}
	for _, meta := range row.MetaLines {
		if meta = strings.TrimSpace(meta); meta != "" {
			line := meta
			if selected && multiLine {
				line = SelectedLineWithTheme(pickerTheme, themeContinuation(pickerTheme), " "+meta)
			} else {
				line = "   " + line
			}
			rendered = append(rendered, line)
		}
	}
	return rendered
}

func SelectedLine(prefix, value string) string {
	return SelectedLineWithTheme(DefaultTheme, prefix, value)
}

func SelectedLineWithTheme(pickerTheme Theme, prefix, value string) string {
	selected := themeSelected(pickerTheme)
	return prefix + strings.ReplaceAll(value, Reset, Reset+selected) + Reset
}

func SelectedContent(value string) string {
	return SelectedContentWithTheme(DefaultTheme, value)
}

func SelectedContentWithTheme(pickerTheme Theme, value string) string {
	selected := themeSelected(pickerTheme)
	return selected + strings.ReplaceAll(value, Reset, Reset+selected) + Reset
}

func InverseSelectedContent(value string) string {
	return InverseSelectedContentWithTheme(DefaultTheme, value)
}

func InverseSelectedContentWithTheme(pickerTheme Theme, value string) string {
	inverse := themeCursor(pickerTheme)
	return inverse + strings.ReplaceAll(value, Reset, Reset+inverse) + Reset
}

func themeSelected(pickerTheme Theme) string {
	if pickerTheme.Selected != "" {
		return pickerTheme.Selected
	}
	return CurrentStart
}

func themeMuted(pickerTheme Theme) string {
	if pickerTheme.Muted != "" {
		return pickerTheme.Muted
	}
	return MutedStart
}

func themeAccent(pickerTheme Theme) string {
	if pickerTheme.Accent != "" {
		return pickerTheme.Accent
	}
	return themeAccentStart
}

func themeCursor(pickerTheme Theme) string {
	if pickerTheme.Cursor != "" {
		return pickerTheme.Cursor
	}
	return CursorStart
}

func themePointer(pickerTheme Theme) string {
	selected := themeSelected(pickerTheme)
	return selected + themeAccent(pickerTheme) + "▌" + selected + " "
}

func themeContinuation(pickerTheme Theme) string {
	return themePointer(pickerTheme)
}

func clampCursor(runes []rune, cursor int) int {
	if cursor < 0 {
		return 0
	}
	if cursor > len(runes) {
		return len(runes)
	}
	return cursor
}
