package render

import (
	"strconv"
	"strings"

	"github.com/es5h/projmux/internal/core/preview"
)

// RenderPopupPreview renders a concise textual popup preview from the derived
// preview read-model.
func RenderPopupPreview(model preview.PopupReadModel) string {
	var builder strings.Builder

	writePopupSection(&builder, "Session")
	writePopupKV(&builder, "name", sanitizeCell(model.SessionName))
	writePopupKV(&builder, "windows", sanitizeCell(strconv.Itoa(effectiveWindowCount(model))))
	writePopupKV(&builder, "pane", formatLegacyPaneSummary(model))
	if pane, ok := selectedPreviewPane(model); ok {
		writePopupKV(&builder, "cmd", fallbackUnknown(displayPaneCommand(pane)))
		writePopupKV(&builder, "title", fallbackUnknown(displayPaneTitle(pane)))
		if status := formatPaneStatus(pane); status != "" {
			writePopupKV(&builder, "status", status)
		}
		writePopupKV(&builder, "path", fallbackUnknown(sanitizeCell(pane.Path)))
	}
	builder.WriteString("\n")

	writePopupSection(&builder, "Windows")
	writeWindows(&builder, model)

	builder.WriteString("\n")
	writePopupSection(&builder, "Panes")
	writePanes(&builder, model)

	if snapshot := strings.TrimRight(model.PaneSnapshot, "\r\n"); snapshot != "" {
		builder.WriteString("\n")
		writePopupSection(&builder, "Pane Snapshot")
		writePopupRule(&builder)
		builder.WriteString(snapshot)
		builder.WriteString("\n")
	}

	return builder.String()
}

func formatPopupSummary(model preview.PopupReadModel) string {
	var parts []string
	parts = append(parts, sanitizeCell(strconv.Itoa(effectiveWindowCount(model)))+"w")
	parts = append(parts, sanitizeCell(strconv.Itoa(effectivePaneCount(model)))+"p")
	if target := formatTargetSummary(model.SelectedWindowIndex, model.SelectedPaneIndex); target != "" {
		parts = append(parts, target)
	}
	return strings.Join(parts, "  ")
}

func effectiveWindowCount(model preview.PopupReadModel) int {
	if model.WindowCount > 0 {
		return model.WindowCount
	}
	return len(model.Windows)
}

func effectivePaneCount(model preview.PopupReadModel) int {
	if model.TotalPaneCount > 0 {
		return model.TotalPaneCount
	}
	return len(model.Panes)
}

func formatSelectedSummary(model preview.PopupReadModel) string {
	if !model.HasSelection {
		return "none"
	}

	return formatTargetSummary(model.SelectedWindowIndex, model.SelectedPaneIndex)
}

func formatTargetSummary(windowIndex, paneIndex string) string {
	windowIndex = strings.TrimSpace(windowIndex)
	paneIndex = strings.TrimSpace(paneIndex)
	if windowIndex == "" {
		return ""
	}
	if paneIndex == "" {
		return "w" + sanitizeCell(windowIndex)
	}
	return "w" + sanitizeCell(windowIndex) + ".p" + sanitizeCell(paneIndex)
}

func writeWindows(builder *strings.Builder, model preview.PopupReadModel) {
	if len(model.Windows) == 0 {
		builder.WriteString("(none)\n")
		return
	}

	selectedWindow := strings.TrimSpace(model.SelectedWindowIndex)
	aggregationPanes := model.AllPanes
	if len(aggregationPanes) == 0 {
		aggregationPanes = model.Panes
	}
	for _, window := range model.Windows {
		line := formatWindowSummary(window, aggregationPanes)
		if window.Index == selectedWindow {
			builder.WriteString(highlightPreviewLine(line))
			builder.WriteString("\n")
			continue
		}
		builder.WriteString(line)
		builder.WriteString("\n")
	}
}

func writePanes(builder *strings.Builder, model preview.PopupReadModel) {
	if len(model.Panes) == 0 {
		builder.WriteString("(none)\n")
		return
	}

	selectedPane := strings.TrimSpace(model.SelectedPaneIndex)
	for _, pane := range model.Panes {
		line := formatPaneSummary(pane)
		if pane.Index == selectedPane && selectedPane != "" {
			builder.WriteString(highlightPreviewLine(line))
			builder.WriteString("\n")
			continue
		}
		builder.WriteString(line)
		builder.WriteString("\n")
	}
}

func selectionMarker(selected bool) string {
	if selected {
		return "*"
	}
	return " "
}

func formatWindowSummary(window preview.Window, panes []preview.Pane) string {
	name := sanitizeCell(window.Name)
	if name == "" {
		name = "-"
	}
	return "[" + sanitizeCell(window.Index) + "] " + padRight(truncateText(name, 18), 18) + " " + padLeft(strconv.Itoa(window.PaneCount), 2) + "p"
}

func formatPaneSummary(pane preview.Pane) string {
	title := displayPaneTitle(pane)
	if title == "" {
		title = "-"
	}
	command := displayPaneCommand(pane)
	if command == "" {
		command = "-"
	}
	line := "[" + sanitizeCell(pane.WindowIndex) + "." + sanitizeCell(pane.Index) + "] " + padRight(truncateText(title, 18), 18) + " " + truncateText(command, 10)
	if status := formatPaneStatus(pane); status != "" {
		line += "  " + ansiDim + status + ansiReset
	}
	return line
}

func displayPaneTitle(pane preview.Pane) string {
	title := sanitizeCell(pane.Title)
	command := displayPaneCommand(pane)
	if title == "" {
		return command
	}
	if strings.TrimSpace(pane.AIAgent) == "" && isShellCommand(pane.Command) {
		return command
	}
	return title
}

func displayPaneCommand(pane preview.Pane) string {
	if agent := sanitizeCell(pane.AIAgent); agent != "" {
		return agent
	}
	return sanitizeCell(pane.Command)
}

func isShellCommand(command string) bool {
	switch strings.TrimSpace(command) {
	case "sh", "bash", "zsh", "fish", "nu", "xonsh":
		return true
	default:
		return false
	}
}

func formatPaneStatus(pane preview.Pane) string {
	parts := make([]string, 0, 6)
	if state := sanitizeCell(pane.AttentionState); state != "" {
		parts = append(parts, "badge="+humanAttentionState(state))
	}
	if state := sanitizeCell(pane.AIState); state != "" {
		parts = append(parts, "state="+humanAIState(state))
	}
	if agent := sanitizeCell(pane.AIAgent); agent != "" {
		parts = append(parts, "assistant="+agent)
	}
	if topic := truncateText(pane.AITopic, 24); topic != "" {
		parts = append(parts, "topic="+topic)
	}
	if ack := sanitizeCell(pane.AttentionAck); ack != "" {
		parts = append(parts, "seen="+humanBoolOption(ack))
	}
	if armed := sanitizeCell(pane.AttentionFocusArmed); armed != "" {
		parts = append(parts, "clears-on-focus="+humanBoolOption(armed))
	}
	return strings.Join(parts, " ")
}

func windowAttentionRank(windowIndex string, panes []preview.Pane) int {
	rank := 0
	for _, pane := range panes {
		if strings.TrimSpace(pane.WindowIndex) != windowIndex {
			continue
		}
		if paneAttentionRank(pane) == 2 {
			return 2
		}
		if paneAttentionRank(pane) == 1 {
			rank = 1
		}
	}
	return rank
}

func formatWindowAttentionPrefix(rank int) string {
	switch rank {
	case 2:
		return ansiYellow + "●" + ansiReset
	case 1:
		return ansiGreen + "●" + ansiReset
	default:
		return ""
	}
}

func paneAttentionRank(pane preview.Pane) int {
	if strings.TrimSpace(pane.AttentionState) == "busy" || strings.TrimSpace(pane.AIState) == "thinking" || hasBraillePrefix(pane.Title) {
		return 2
	}
	if strings.TrimSpace(pane.AttentionState) == "reply" || strings.TrimSpace(pane.AIState) == "waiting" || hasAttentionTitlePrefix(pane.Title) {
		return 1
	}
	return 0
}

func hasAttentionTitlePrefix(title string) bool {
	title = strings.TrimLeft(title, " \t")
	return strings.HasPrefix(title, "✳") || strings.HasPrefix(title, "✔")
}

func humanAttentionState(state string) string {
	switch state {
	case "busy":
		return "working"
	case "reply":
		return "needs-reply"
	default:
		return state
	}
}

func humanAIState(state string) string {
	switch state {
	case "thinking":
		return "working"
	case "waiting":
		return "waiting-for-you"
	default:
		return state
	}
}

func humanBoolOption(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return "yes"
	case "0", "false", "no", "off":
		return "no"
	default:
		return value
	}
}

func writePopupSection(builder *strings.Builder, title string) {
	builder.WriteString(ansiBold)
	builder.WriteString(ansiCyan)
	builder.WriteString(title)
	builder.WriteString(ansiReset)
	builder.WriteString("\n")
}

func writePopupKV(builder *strings.Builder, key, value string) {
	builder.WriteString("  ")
	builder.WriteString(ansiDim)
	builder.WriteString(key)
	builder.WriteString(ansiReset)
	builder.WriteString("  ")
	builder.WriteString(value)
	builder.WriteString("\n")
}

func writePopupRule(builder *strings.Builder) {
	builder.WriteString(ansiDim)
	builder.WriteString(strings.Repeat("─", 64))
	builder.WriteString(ansiReset)
	builder.WriteString("\n")
}

func highlightPreviewLine(line string) string {
	return ansiBold + ansiGreen + line + ansiReset
}

func formatLegacyPaneSummary(model preview.PopupReadModel) string {
	pane := sanitizeCell(model.SelectedPaneIndex)
	if pane == "" {
		pane = "?"
	}
	window := sanitizeCell(model.SelectedWindowIndex)
	if window == "" {
		window = "?"
	}
	return pane + " (window " + window + ")"
}

func selectedPreviewPane(model preview.PopupReadModel) (preview.Pane, bool) {
	selectedPane := strings.TrimSpace(model.SelectedPaneIndex)
	for _, pane := range model.Panes {
		if selectedPane != "" && pane.Index == selectedPane {
			return pane, true
		}
	}
	return preview.Pane{}, false
}

func fallbackUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func truncateText(value string, limit int) string {
	value = sanitizeCell(value)
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit == 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}

func padRight(value string, width int) string {
	runes := []rune(value)
	if len(runes) >= width {
		return value
	}
	return value + strings.Repeat(" ", width-len(runes))
}

func padLeft(value string, width int) string {
	runes := []rune(value)
	if len(runes) >= width {
		return value
	}
	return strings.Repeat(" ", width-len(runes)) + value
}
