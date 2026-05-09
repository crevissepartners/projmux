package projmuxpicker

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

func PreviewPlacement(window string) string {
	window = strings.ToLower(strings.TrimSpace(window))
	switch {
	case strings.HasPrefix(window, "down"):
		return "down"
	case strings.HasPrefix(window, "right"), window == "":
		return "right"
	default:
		return "inline"
	}
}

func PreviewHeight(rows int, window string) int {
	if rows <= 0 {
		rows = DefaultRows - 2
	}
	percent := previewPercent(window)
	if percent <= 0 {
		percent = 25
	}
	height := previewRoundedPercent(rows+2, percent) - 2
	if height < 1 {
		return 1
	}
	if height > rows-2 {
		return maxInt(1, rows-2)
	}
	return height
}

func PreviewWidth(cols int, window string) int {
	if cols <= 0 {
		cols = DefaultCols - 4
	}
	percent := previewPercent(window)
	if percent <= 0 {
		percent = 50
	}
	width := previewRoundedPercent(cols+4, percent) - 6
	if width < 1 {
		return 1
	}
	if width > cols-1 {
		return maxInt(1, cols-1)
	}
	return width
}

func RenderSplitPreview(w io.Writer, listLines, previewLines []string, layout Layout, window string, total, start, end int) {
	RenderSplitPreviewRows(w, listLines, previewLines, layout, window, total, start, end, 0)
}

func RenderSplitPreviewRows(w io.Writer, listLines, previewLines []string, layout Layout, window string, total, start, end, rowCount int) {
	previewWidth := PreviewWidth(layout.Cols, window)
	listWidth := layout.Cols - previewWidth - 1
	if listWidth < 32 {
		listWidth = 32
		previewWidth = layout.Cols - listWidth - 1
	}
	listLines = ListLinesWithScrollbar(listLines, total, start, end, listWidth)
	rows := max(rowCount, maxInt(len(listLines), len(previewLines)))
	for i := 0; i < rows; i++ {
		left := ""
		if i < len(listLines) {
			left = listLines[i]
		}
		right := ""
		if i < len(previewLines) {
			right = previewLines[i]
		}
		right = PadRight(TruncateANSI(right, previewWidth), previewWidth)
		fmt.Fprintf(w, "%s│%s\n", PadRight(TruncateANSI(left, listWidth), listWidth), right)
	}
}

func RenderDownPreview(w io.Writer, previewLines []string, layout Layout) {
	width := layout.Cols
	if width <= 0 {
		width = DefaultCols
	}
	fmt.Fprintln(w, SeparatorLine(width))
	for _, line := range previewLines {
		fmt.Fprintln(w, PadRight(TruncateANSI(line, width), width))
	}
}

func RenderInlinePreview(w io.Writer, previewLines []string) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "--- preview ---")
	for _, line := range previewLines {
		fmt.Fprintln(w, line)
	}
}

func previewPercent(window string) int {
	for part := range strings.SplitSeq(window, ",") {
		part = strings.TrimSpace(part)
		if !strings.HasSuffix(part, "%") {
			continue
		}
		value, err := strconv.Atoi(strings.TrimSuffix(part, "%"))
		if err == nil && value > 0 {
			return value
		}
	}
	return 0
}

func previewRoundedPercent(size, percent int) int {
	return (size*percent + 50) / 100
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
