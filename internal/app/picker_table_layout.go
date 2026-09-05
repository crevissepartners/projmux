package app

import (
	"strings"

	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// Compact field selection makes the representative Registry table fit the
// 75 usable cells of an 80-column popup. Wide deliberately retains every value:
// the fixed viewport may clip the row, and its action detail and JSON recover it.
const fixedViewportNarrowUsableCells = 75

func pickerTableWidths(table [][]string) []int {
	if len(table) == 0 {
		return nil
	}
	widths := make([]int, len(table[0]))
	for _, row := range table {
		for i, cell := range row {
			if i < len(widths) {
				widths[i] = max(widths[i], resourceCellWidth(cell))
			}
		}
	}
	return widths
}

// Compact uses one separating cell so the complete 76-cell regression row
// fits the native frame's 75 label cells. Wide retains the shared two-cell gap;
// neither profile truncates data or changes the renderer's frame and gutters.
func pickerTableLine(row []string, widths []int, profile columnProfile) string {
	if profile == columnWide {
		return resourceTableLine(row, widths)
	}
	var line strings.Builder
	for i, cell := range row {
		line.WriteString(cell)
		if i < len(row)-1 {
			line.WriteString(strings.Repeat(" ", widths[i]-resourceCellWidth(cell)+1))
		}
	}
	return strings.TrimRight(line.String(), " ")
}

// Details put each wide field on its own row so a narrow popup can recover
// values hidden to the right of the list. These inert rows keep action routing
// and the underlying resource snapshot unchanged.
func pickerColumnDetailEntries(columns, cells []string) []intpickercompat.Entry {
	entries := make([]intpickercompat.Entry, 0, len(columns))
	for i, column := range columns {
		entries = append(entries, intpickercompat.Entry{
			Label: column + ": " + cells[i], Value: settingsNoopValue,
		})
	}
	return entries
}
