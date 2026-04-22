package preview

import "strings"

// PopupReadModel captures the derived preview state for a selected session.
type PopupReadModel struct {
	SessionName         string
	HasSelection        bool
	SelectedWindowIndex string
	SelectedPaneIndex   string
	Windows             []Window
	Panes               []Pane
}

// PopupReadModelInputs captures the pure inputs needed to derive popup state.
type PopupReadModelInputs struct {
	SessionName        string
	StoredSelection    Selection
	HasStoredSelection bool
	Windows            []Window
	Panes              []Pane
}

// BuildPopupReadModel derives the current popup preview state from stored
// selection and available window/pane inventory.
func BuildPopupReadModel(inputs PopupReadModelInputs) PopupReadModel {
	windows := normalizedWindows(inputs.Windows)
	allPanes := normalizedPanes(inputs.Panes)

	model := PopupReadModel{
		SessionName: strings.TrimSpace(inputs.SessionName),
		Windows:     windows,
	}
	if len(windows) == 0 {
		return model
	}

	selectedWindow := resolveSelectedWindow(windows, inputs.StoredSelection, inputs.HasStoredSelection)
	windowPanes := panesForWindow(allPanes, selectedWindow)
	selectedPane := resolveSelectedPane(windowPanes, selectedWindow, inputs.StoredSelection, inputs.HasStoredSelection)

	model.HasSelection = true
	model.SelectedWindowIndex = selectedWindow
	model.SelectedPaneIndex = selectedPane
	model.Panes = windowPanes
	return model
}

func resolveSelectedWindow(windows []Window, stored Selection, hasStored bool) string {
	storedWindow := strings.TrimSpace(stored.WindowIndex)
	if hasStored && storedWindow != "" && hasWindow(windows, storedWindow) {
		return storedWindow
	}

	if active, ok := activeWindow(windows); ok {
		return active.Index
	}

	return windows[0].Index
}

func resolveSelectedPane(panes []Pane, selectedWindow string, stored Selection, hasStored bool) string {
	storedWindow := strings.TrimSpace(stored.WindowIndex)
	storedPane := strings.TrimSpace(stored.PaneIndex)
	if hasStored && storedWindow == selectedWindow && storedPane != "" && hasPane(panes, storedPane) {
		return storedPane
	}

	if active, ok := activePane(panes); ok {
		return active.Index
	}

	if len(panes) > 0 {
		return panes[0].Index
	}

	return ""
}

func hasWindow(windows []Window, index string) bool {
	for _, window := range windows {
		if window.Index == index {
			return true
		}
	}
	return false
}

func hasPane(panes []Pane, index string) bool {
	for _, pane := range panes {
		if pane.Index == index {
			return true
		}
	}
	return false
}
