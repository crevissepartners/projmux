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

	writePopupSection(&builder, "Target")
	writePopupKV(&builder, "dir", sanitizeSwitchPreviewPath(model))
	writePopupKV(&builder, "session", sanitizeCell(model.SessionName))
	writePopupKV(&builder, "mode", formatPopupSwitchPreviewMode(model.SessionMode))

	if branch := sanitizeCell(model.GitBranch); branch != "" {
		writePopupKV(&builder, "git", branch)
	}
	if kube := formatKubeSummary(model); kube != "" {
		writePopupKV(&builder, "k8s", kube)
	}

	builder.WriteString("\n")

	if model.SessionMode != "existing" {
		writePopupSection(&builder, "Action")
		writePopupKV(&builder, "enter", "switch/create this session")
		writePopupKV(&builder, "result", "tmux new-session -d -s <name> -c <dir>")
		return builder.String()
	}

	writePopupSection(&builder, "Windows")
	writeWindows(&builder, model.Popup)

	builder.WriteString("\n")
	writePopupSection(&builder, "Panes")
	writePanes(&builder, model.Popup)

	if snapshot := strings.TrimRight(model.Popup.PaneSnapshot, "\r\n"); snapshot != "" {
		builder.WriteString("\n")
		writePopupSection(&builder, "Pane Snapshot")
		writePopupRule(&builder)
		builder.WriteString(snapshot)
		builder.WriteString("\n")
	}

	return builder.String()
}

func formatPopupSwitchPreviewMode(mode string) string {
	switch sanitizeCell(mode) {
	case "existing":
		return ansiGreen + "existing" + ansiReset
	case "new":
		return ansiYellow + "new session" + ansiReset
	default:
		return sanitizeCell(mode)
	}
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

	if kube := formatKubeSummary(model); kube != "" {
		builder.WriteString("k8s:")
		builder.WriteString(kube)
		builder.WriteString("\n\n")
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

func formatKubeSummary(model corepreview.SwitchReadModel) string {
	context := sanitizeCell(model.KubeContext)
	if context == "" {
		return ""
	}
	namespace := sanitizeCell(model.KubeNamespace)
	if namespace == "" {
		namespace = "default"
	}
	return ansiRed + context + ansiReset + "/" + ansiBlue + namespace + ansiReset
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
