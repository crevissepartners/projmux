package render

import (
	"fmt"
	"strings"

	corepreview "github.com/es5h/projmux/internal/core/preview"
)

// RenderSwitchPreview renders sidebar/popup preview context for a switch
// candidate.
func RenderSwitchPreview(model corepreview.SwitchReadModel, ui string) string {
	if strings.TrimSpace(ui) == "sidebar" {
		return renderSidebarSwitchPreview(model)
	}

	var builder strings.Builder

	builder.WriteString("dir: ")
	builder.WriteString(sanitizeSwitchPreviewPath(model))
	builder.WriteString("\n")

	builder.WriteString("session: ")
	builder.WriteString(sanitizeCell(model.SessionName))
	builder.WriteString("\n")

	builder.WriteString("state: ")
	builder.WriteString(sanitizeCell(model.SessionMode))
	builder.WriteString("\n")

	if branch := sanitizeCell(model.GitBranch); branch != "" {
		builder.WriteString("git: ")
		builder.WriteString(branch)
		builder.WriteString("\n")
	}

	builder.WriteString("summary: ")
	builder.WriteString(formatPopupSummary(model.Popup))
	builder.WriteString("\n")

	builder.WriteString("selected: ")
	builder.WriteString(formatSelectedSummary(model.Popup))
	builder.WriteString("\n")

	builder.WriteString("windows:\n")
	writeWindows(&builder, model.Popup)

	builder.WriteString("panes:\n")
	writePanes(&builder, model.Popup)

	return builder.String()
}

func renderSidebarSwitchPreview(model corepreview.SwitchReadModel) string {
	var builder strings.Builder

	writeSidebarSection(&builder, "Dir")
	builder.WriteString(sanitizeSwitchPreviewPath(model))
	builder.WriteString("\n\n")

	if model.SessionMode != "existing" {
		writeSidebarSection(&builder, "Status")
		builder.WriteString(ansiYellow)
		builder.WriteString("new session")
		builder.WriteString(ansiReset)
		builder.WriteString("\n")
		return builder.String()
	}

	writeSidebarSection(&builder, "Windows")
	if len(model.Windows) == 0 {
		builder.WriteString("(none)\n")
		return builder.String()
	}

	for _, window := range model.Windows {
		builder.WriteString(formatSidebarWindowSummary(window, model.Panes))
		builder.WriteString("\n")
	}

	return builder.String()
}

func writeSidebarSection(builder *strings.Builder, title string) {
	builder.WriteString(ansiBold)
	builder.WriteString(ansiCyan)
	builder.WriteString(title)
	builder.WriteString(ansiReset)
	builder.WriteString("\n")
}

func formatSidebarWindowSummary(window corepreview.Window, panes []corepreview.Pane) string {
	titles := sidebarWindowTitles(window.Index, panes)
	if len(titles) == 0 {
		name := sanitizeCell(window.Name)
		if name != "" {
			titles = append(titles, name)
		}
	}
	if len(titles) == 0 {
		titles = append(titles, window.Index)
	}

	return fmt.Sprintf("[%s] %s", sanitizeCell(window.Index), strings.Join(titles, " | "))
}

func sidebarWindowTitles(windowIndex string, panes []corepreview.Pane) []string {
	unique := make([]string, 0, 3)
	extraCount := 0

	for _, pane := range panes {
		if pane.WindowIndex != windowIndex {
			continue
		}

		title := sanitizeCell(pane.Title)
		if title == "" || containsString(unique, title) {
			continue
		}

		if len(unique) < 3 {
			unique = append(unique, formatSidebarPaneTitle(title))
			continue
		}
		extraCount++
	}

	if extraCount > 0 {
		unique = append(unique, fmt.Sprintf("+%d", extraCount))
	}

	return unique
}

func formatSidebarPaneTitle(title string) string {
	switch {
	case strings.HasPrefix(title, "✳"), strings.HasPrefix(title, "✔"):
		display := strings.TrimSpace(strings.TrimLeft(title, "✳✔"))
		return ansiGreen + "●" + ansiReset + " " + display
	case hasBraillePrefix(title):
		runes := []rune(title)
		display := ""
		if len(runes) > 1 {
			display = strings.TrimSpace(string(runes[1:]))
		}
		return ansiYellow + "●" + ansiReset + " " + display
	default:
		return title
	}
}

func hasBraillePrefix(value string) bool {
	if value == "" {
		return false
	}

	r := []rune(value)[0]
	return r >= 0x2800 && r <= 0x28FF
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func sanitizeSwitchPreviewPath(model corepreview.SwitchReadModel) string {
	path := sanitizeCell(model.DisplayPath)
	if path != "" {
		return path
	}

	path = sanitizeCell(model.Path)
	if path != "" {
		return path
	}

	return "-"
}
