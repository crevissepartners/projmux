package render

import "strings"

type SwitchRow struct {
	Label string
	Value string
}

type SwitchCandidate struct {
	Path        string
	SessionName string
	ModeLabel   string
}

func BuildSwitchRows(candidates []SwitchCandidate) []SwitchRow {
	rows := make([]SwitchRow, 0, len(candidates))
	for _, candidate := range candidates {
		rows = append(rows, SwitchRow{
			Label: formatSwitchLabel(candidate),
			Value: candidate.Path,
		})
	}
	return rows
}

func formatSwitchLabel(candidate SwitchCandidate) string {
	parts := make([]string, 0, 3)

	sessionName := sanitizeCell(candidate.SessionName)
	if sessionName != "" {
		parts = append(parts, sessionName)
	}

	modeLabel := sanitizeCell(candidate.ModeLabel)
	if modeLabel != "" {
		parts = append(parts, "["+modeLabel+"]")
	}

	path := sanitizeCell(candidate.Path)
	if path != "" {
		parts = append(parts, path)
	}

	return strings.Join(parts, "  ")
}

func sanitizeCell(value string) string {
	value = strings.ReplaceAll(value, "\t", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.TrimSpace(value)
}
