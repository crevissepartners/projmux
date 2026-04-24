package render

import (
	"path/filepath"
	"strings"
)

const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiDim    = "\x1b[2m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiCyan   = "\x1b[36m"
)

type SwitchRow struct {
	Label string
	Value string
}

type SwitchCandidate struct {
	Path        string
	DisplayPath string
	SessionName string
	ModeLabel   string
	UI          string
	Pinned      bool
	Tagged      bool
}

func PrettyPath(path, homeDir, repoRoot string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}

	path = filepath.Clean(path)
	homeDir = cleanPrettyRoot(homeDir)
	repoRoot = cleanPrettyRoot(repoRoot)

	if repoRoot != "" {
		if path == repoRoot {
			return "~rp"
		}
		if strings.HasPrefix(path, repoRoot+string(filepath.Separator)) {
			return "~rp" + strings.TrimPrefix(path, repoRoot)
		}
	}

	if homeDir != "" {
		if path == homeDir {
			return "~"
		}
		if strings.HasPrefix(path, homeDir+string(filepath.Separator)) {
			return "~" + strings.TrimPrefix(path, homeDir)
		}
	}

	return path
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
	if candidate.Path == "__projmux_settings__" {
		return formatSettingsLabel(candidate)
	}
	if candidate.UI == "sidebar" {
		return formatSidebarSwitchLabel(candidate)
	}

	parts := make([]string, 0, 3)

	sessionName := sanitizeCell(candidate.SessionName)
	if sessionName != "" {
		parts = append(parts, sessionName)
	}

	modeLabel := sanitizeCell(candidate.ModeLabel)
	if modeLabel != "" {
		parts = append(parts, "["+modeLabel+"]")
	}

	path := sanitizeCell(candidate.DisplayPath)
	if path == "" {
		path = sanitizeCell(candidate.Path)
	}
	if path != "" {
		parts = append(parts, path)
	}

	return strings.Join(parts, "  ")
}

func formatSettingsLabel(candidate SwitchCandidate) string {
	label := sanitizeCell(candidate.DisplayPath)
	if label == "" {
		label = "Settings"
	}
	if candidate.UI != "sidebar" {
		return label
	}
	description := "manage pinned directories"
	return "  " + ansiBold + ansiCyan + label + ansiReset + "  " + ansiDim + description + ansiReset
}

func formatSidebarSwitchLabel(candidate SwitchCandidate) string {
	parts := make([]string, 0, 4)
	parts = append(parts, formatTagBadge(candidate.Tagged))
	parts = append(parts, formatPinBadge(candidate.Pinned))

	sessionName := sanitizeCell(candidate.SessionName)
	if sessionName != "" {
		parts = append(parts, formatSidebarSessionName(sessionName, candidate.ModeLabel))
	}

	path := sanitizeCell(candidate.DisplayPath)
	if path == "" {
		path = sanitizeCell(candidate.Path)
	}
	if path != "" {
		parts = append(parts, ansiDim+path+ansiReset)
	}

	return strings.Join(parts, " ")
}

func formatSidebarSessionName(sessionName, mode string) string {
	mode = sanitizeCell(mode)
	switch mode {
	case "existing":
		return ansiBold + ansiGreen + sessionName + ansiReset
	case "new":
		return sessionName
	default:
		return sessionName
	}
}

func formatTagBadge(tagged bool) string {
	if tagged {
		return ansiRed + "x" + ansiReset
	}
	return " "
}

func formatPinBadge(pinned bool) string {
	if pinned {
		return ansiYellow + "*" + ansiReset
	}
	return " "
}

func sanitizeCell(value string) string {
	value = strings.ReplaceAll(value, "\t", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.TrimSpace(value)
}

func cleanPrettyRoot(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}

	return filepath.Clean(path)
}
