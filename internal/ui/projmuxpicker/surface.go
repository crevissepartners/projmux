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
	for i := 0; i < remaining; i++ {
		fmt.Fprintln(&screen)
	}
	for _, line := range footerLines {
		fmt.Fprintln(&screen, line)
	}
	fmt.Fprint(w, screen.String())
}

func FooterBlockLines(footer string, cols int) []string {
	footer = strings.TrimSpace(footer)
	if footer == "" {
		return nil
	}
	if cols <= 0 {
		cols = DefaultCols
	}
	lines := []string{TruncateANSI(strings.Repeat(GapLine, cols), cols)}
	for _, line := range strings.Split(footer, "\n") {
		lines = append(lines, TruncateANSI(strings.TrimRight(line, "\r"), cols))
	}
	return lines
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

func PromptLineWithRenderedQuery(prompt, query, renderedQuery string, matches, total, cols int) string {
	prompt = strings.TrimRight(prompt, " ")
	line := strings.TrimRight(prompt+" "+renderedQuery, " ")
	info := strconv.Itoa(matches)
	if query != "" || matches != total {
		info = fmt.Sprintf("%d/%d", matches, total)
	}
	if cols <= 0 {
		cols = DefaultCols
	}
	padding := cols - VisibleLen(line) - len(info)
	if padding < 2 {
		return line + "  " + info
	}
	return line + strings.Repeat(" ", padding) + info
}

func QueryWithCursor(query string, cursor int) string {
	runes := []rune(query)
	cursor = clampCursor(runes, cursor)
	if cursor == len(runes) {
		return string(runes) + CursorStart + " " + Reset
	}
	var out strings.Builder
	for i, r := range runes {
		if i == cursor {
			out.WriteString(CursorStart)
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
	if start < 0 {
		start = 0
	}
	if end > len(rows) {
		end = len(rows)
	}
	lines := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		lines = append(lines, InteractiveRowLines(rows[i], i == selected, multiLine)...)
		if multiLine && i < end-1 {
			lines = append(lines, gapSentinel)
		}
	}
	return lines
}

func ListLinesWithScrollbar(lines []string, total, start, end, width int) []string {
	visible := end - start
	hasScrollbar := total > visible && len(lines) > 0 && width > 1
	if !hasScrollbar {
		return RenderableListLines(lines, width)
	}
	scrollbarIndex := 0
	if maxStart := total - visible; maxStart > 0 && len(lines) > 1 {
		scrollbarIndex = start * (len(lines) - 1) / maxStart
	}
	rendered := make([]string, 0, len(lines))
	for i, line := range lines {
		marker := " "
		if i == scrollbarIndex {
			marker = Scrollbar
		}
		line = RenderableListLine(line, width-1)
		rendered = append(rendered, PadRight(TruncateANSI(line, width-1), width-1)+marker)
	}
	return rendered
}

func RenderableListLines(lines []string, width int) []string {
	rendered := make([]string, 0, len(lines))
	for _, line := range lines {
		rendered = append(rendered, RenderableListLine(line, width))
	}
	return rendered
}

func RenderableListLine(line string, width int) string {
	if line != gapSentinel {
		return PadStyledLine(line, width)
	}
	if width <= 4 {
		return GapLine
	}
	return "  " + strings.Repeat(GapLine, width-2)
}

func PadStyledLine(line string, width int) string {
	if width <= 0 || VisibleLen(line) >= width {
		return line
	}
	padding := strings.Repeat(" ", width-VisibleLen(line))
	if strings.HasSuffix(line, Reset) && padsInsideFinalStyle(line) {
		return strings.TrimSuffix(line, Reset) + padding + Reset
	}
	return line + padding
}

func padsInsideFinalStyle(line string) bool {
	return strings.Contains(line, CurrentStart) || strings.Contains(line, InverseStart)
}

func InteractiveRowLines(row Row, selected, multiLine bool) []string {
	lines := strings.Split(row.Label, "\n")
	prefix := "  "
	if selected {
		prefix = Pointer
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	rendered := make([]string, 0, len(lines)+len(row.MetaLines))
	first := fmt.Sprintf("%s%s", prefix, strings.TrimRight(lines[0], "\r"))
	if selected {
		first = SelectedLine(prefix, strings.TrimRight(lines[0], "\r"))
	}
	rendered = append(rendered, first)
	for _, line := range lines[1:] {
		line = fmt.Sprintf("    %s", strings.TrimRight(line, "\r"))
		if selected && multiLine {
			line = SelectedLine(Continuation, strings.TrimSpace(line))
		}
		rendered = append(rendered, line)
	}
	for _, meta := range row.MetaLines {
		if meta = strings.TrimSpace(meta); meta != "" {
			line := fmt.Sprintf("    %s", meta)
			if selected && multiLine {
				line = SelectedLine(Continuation, meta)
			}
			rendered = append(rendered, line)
		}
	}
	return rendered
}

func SelectedLine(prefix, value string) string {
	return prefix + strings.ReplaceAll(value, Reset, Reset+CurrentStart) + Reset
}

func SelectedContent(value string) string {
	return CurrentStart + strings.ReplaceAll(value, Reset, Reset+CurrentStart) + Reset
}

func InverseSelectedContent(value string) string {
	return InverseStart + strings.ReplaceAll(value, Reset, Reset+InverseStart) + Reset
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
