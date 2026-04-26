package render

import (
	"strconv"
	"strings"
	"time"
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
	Activity     int64
}

func BuildSessionRows(summaries []SessionSummary) []SessionRow {
	rows := make([]SessionRow, 0, len(summaries))
	for _, summary := range summaries {
		parts := make([]string, 0, 5)
		parts = append(parts, formatTagSlot(false))

		status := ansiYellow + "[Detached]" + ansiReset
		if summary.Attached {
			status = ansiGreen + "[Attached]" + ansiReset
		}
		parts = append(parts, status)

		if summary.WindowCount >= 2 {
			parts = append(parts, ansiBlue+sanitizeCell(strconv.Itoa(summary.WindowCount))+" Windows"+ansiReset)
		}

		if name := sanitizeCell(summary.Name); name != "" {
			parts = append(parts, name)
		}

		if activity := formatSessionActivity(summary.Activity); activity != "" {
			parts = append(parts, activity)
		}

		label := strings.Join(parts, "  ")

		rows = append(rows, SessionRow{
			Label: label,
			Value: sanitizeCell(summary.Name),
		})
	}
	return rows
}

func formatSessionActivity(activity int64) string {
	if activity <= 0 {
		return ""
	}
	return time.Unix(activity, 0).Format("2006-01-02 15:04")
}
