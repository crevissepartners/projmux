package render

import (
	"fmt"
	"slices"
	"strings"

	corepreview "github.com/crevissepartners/projmux/internal/core/preview"
)

// RenderSwitchPreview renders sidebar/popup preview context for a switch
// candidate.
func RenderSwitchPreview(model corepreview.SwitchReadModel, ui string) string {
	if strings.TrimSpace(ui) == "sidebar" {
		return renderSidebarSwitchPreview(model)
	}

	var builder strings.Builder

	writePopupSection(&builder, "Target")
	writePopupKV(&builder, "session", sanitizeCell(model.SessionName))
	writePopupKV(&builder, "mode", formatPopupSwitchPreviewMode(model.SessionMode))

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

		title := formatSidebarPaneTitle(pane)
		if title == "" || containsString(unique, title) {
			continue
		}

		if len(unique) < 3 {
			unique = append(unique, title)
			continue
		}
		extraCount++
	}

	if extraCount > 0 {
		unique = append(unique, fmt.Sprintf("+%d", extraCount))
	}

	return unique
}

func formatSidebarPaneTitle(pane corepreview.Pane) string {
	title := sidebarPaneLabel(pane)
	if title == "" {
		return ""
	}

	if badge := sidebarPaneBadge(pane); badge != "" {
		return badge + " " + trimSidebarPaneTitleMarker(title)
	}

	switch {
	case strings.HasPrefix(title, "✳"), strings.HasPrefix(title, "✔"):
		return ansiGreen + "●" + ansiReset + " " + trimSidebarPaneTitleMarker(title)
	case hasBraillePrefix(title):
		return ansiYellow + "●" + ansiReset + " " + trimSidebarPaneTitleMarker(title)
	default:
		return title
	}
}

func sidebarPaneLabel(pane corepreview.Pane) string {
	if strings.TrimSpace(pane.AIAgent) != "" {
		if topic := sanitizeCell(pane.AITopic); topic != "" {
			return topic
		}
	}
	return sanitizeCell(pane.Title)
}

func sidebarPaneBadge(pane corepreview.Pane) string {
	switch paneAttentionRank(pane) {
	case 2:
		return ansiYellow + "●" + ansiReset
	case 1:
		return ansiGreen + "●" + ansiReset
	default:
		return ""
	}
}

func trimSidebarPaneTitleMarker(title string) string {
	switch {
	case strings.HasPrefix(title, "✳"), strings.HasPrefix(title, "✔"):
		return strings.TrimSpace(strings.TrimLeft(title, "✳✔"))
	case hasBraillePrefix(title):
		runes := []rune(title)
		if len(runes) <= 1 {
			return ""
		}
		return strings.TrimSpace(string(runes[1:]))
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
	return slices.Contains(values, target)
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
