package render

import (
	"strconv"
	"strings"
)

type SessionRow struct {
	Label string
	Value string
}

type SessionSummary struct {
	Name         string
	Attached     bool
	WindowCount  int
	PaneCount    int
	Path         string
	StoredTarget string
}

func BuildSessionRows(summaries []SessionSummary) []SessionRow {
	rows := make([]SessionRow, 0, len(summaries))
	for _, summary := range summaries {
		label := sanitizeCell(summary.Name)
		status := "detached"
		if summary.Attached {
			status = "attached"
		}
		label += "  [" + status + "]"
		if summary.WindowCount > 0 {
			label += "  " + sanitizeCell(strconv.Itoa(summary.WindowCount)) + "w"
		}
		if summary.PaneCount > 0 {
			label += "  " + sanitizeCell(strconv.Itoa(summary.PaneCount)) + "p"
		}
		if target := sanitizeCell(strings.TrimSpace(summary.StoredTarget)); target != "" {
			label += "  " + target
		}
		if path := sanitizeCell(strings.TrimSpace(summary.Path)); path != "" {
			label += "  " + path
		}

		rows = append(rows, SessionRow{
			Label: label,
			Value: sanitizeCell(summary.Name),
		})
	}
	return rows
}
