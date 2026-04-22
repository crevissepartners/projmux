package render

import (
	"strings"

	"github.com/es5h/projmux/internal/core/preview"
)

// RenderPopupPreview renders a concise textual popup preview from the derived
// preview read-model.
func RenderPopupPreview(model preview.PopupReadModel) string {
	var builder strings.Builder

	builder.WriteString("session: ")
	builder.WriteString(sanitizeCell(model.SessionName))
	builder.WriteString("\n")

	builder.WriteString("selected: ")
	builder.WriteString(formatSelectedSummary(model))
	builder.WriteString("\n")

	builder.WriteString("windows:\n")
	writeWindows(&builder, model)

	builder.WriteString("panes:\n")
	writePanes(&builder, model)

	return builder.String()
}

func formatSelectedSummary(model preview.PopupReadModel) string {
	if !model.HasSelection {
		return "none"
	}

	var builder strings.Builder
	builder.WriteString("window=")
	builder.WriteString(sanitizeCell(model.SelectedWindowIndex))
	builder.WriteString(" pane=")
	if strings.TrimSpace(model.SelectedPaneIndex) == "" {
		builder.WriteString("-")
		return builder.String()
	}

	builder.WriteString(sanitizeCell(model.SelectedPaneIndex))
	return builder.String()
}

func writeWindows(builder *strings.Builder, model preview.PopupReadModel) {
	if len(model.Windows) == 0 {
		builder.WriteString("  (none)\n")
		return
	}

	selectedWindow := strings.TrimSpace(model.SelectedWindowIndex)
	for _, window := range model.Windows {
		builder.WriteString("  ")
		builder.WriteString(selectionMarker(window.Index == selectedWindow))
		builder.WriteString(" ")
		builder.WriteString(sanitizeCell(window.Index))
		builder.WriteString("\n")
	}
}

func writePanes(builder *strings.Builder, model preview.PopupReadModel) {
	if len(model.Panes) == 0 {
		builder.WriteString("  (none)\n")
		return
	}

	selectedPane := strings.TrimSpace(model.SelectedPaneIndex)
	for _, pane := range model.Panes {
		builder.WriteString("  ")
		builder.WriteString(selectionMarker(pane.Index == selectedPane && selectedPane != ""))
		builder.WriteString(" ")
		builder.WriteString(sanitizeCell(pane.Index))
		builder.WriteString("\n")
	}
}

func selectionMarker(selected bool) string {
	if selected {
		return "*"
	}
	return " "
}
