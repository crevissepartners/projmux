package preview

import (
	"errors"
	"strings"
)

// ErrInvalidDirection is returned when a cycle helper is called with an
// unknown direction.
var ErrInvalidDirection = errors.New("invalid preview cycle direction")

// Direction controls whether cycling moves forward or backward.
type Direction string

const (
	DirectionNext Direction = "next"
	DirectionPrev Direction = "prev"
)

// Cursor captures the selected preview target that a caller may later persist.
type Cursor struct {
	WindowIndex string
	PaneIndex   string
}

// Window models a previewable tmux window with an optional active hint.
type Window struct {
	Index     string
	Name      string
	PaneCount int
	Path      string
	Active    bool
}

// Pane models a previewable tmux pane with its parent window and active hint.
type Pane struct {
	WindowIndex string
	Index       string
	Title       string
	Command     string
	Path        string
	Active      bool
}

// CycleInputs captures the pure state needed to move the preview cursor.
type CycleInputs struct {
	StoredWindowIndex string
	StoredPaneIndex   string
	Windows           []Window
	Panes             []Pane
}

// NextPane cycles to the next pane in the current preview window.
func NextPane(inputs CycleInputs) (Cursor, bool, error) {
	return cyclePane(inputs, DirectionNext)
}

// PreviousPane cycles to the previous pane in the current preview window.
func PreviousPane(inputs CycleInputs) (Cursor, bool, error) {
	return cyclePane(inputs, DirectionPrev)
}

// NextWindow cycles to the next preview window and picks that window's active
// pane when possible.
func NextWindow(inputs CycleInputs) (Cursor, bool, error) {
	return cycleWindow(inputs, DirectionNext)
}

// PreviousWindow cycles to the previous preview window and picks that window's
// active pane when possible.
func PreviousWindow(inputs CycleInputs) (Cursor, bool, error) {
	return cycleWindow(inputs, DirectionPrev)
}

func cyclePane(inputs CycleInputs, direction Direction) (Cursor, bool, error) {
	if err := validateDirection(direction); err != nil {
		return Cursor{}, false, err
	}

	panes := normalizedPanes(inputs.Panes)
	if len(panes) == 0 {
		return Cursor{}, false, nil
	}

	currentWindow := strings.TrimSpace(inputs.StoredWindowIndex)
	currentPane := strings.TrimSpace(inputs.StoredPaneIndex)
	if currentWindow == "" || currentPane == "" {
		if active, ok := activePane(panes); ok {
			currentWindow = active.WindowIndex
			currentPane = active.Index
		}
	}

	filtered := panesForWindow(panes, currentWindow)
	if len(filtered) == 0 {
		return Cursor{}, false, nil
	}

	index := 0
	found := false
	for i, pane := range filtered {
		if pane.Index == currentPane {
			index = i
			found = true
			break
		}
	}

	if found {
		index = stepIndex(index, len(filtered), direction)
	}

	target := filtered[index]
	return Cursor{
		WindowIndex: target.WindowIndex,
		PaneIndex:   target.Index,
	}, true, nil
}

func cycleWindow(inputs CycleInputs, direction Direction) (Cursor, bool, error) {
	if err := validateDirection(direction); err != nil {
		return Cursor{}, false, err
	}

	windows := normalizedWindows(inputs.Windows)
	if len(windows) == 0 {
		return Cursor{}, false, nil
	}

	currentWindow := strings.TrimSpace(inputs.StoredWindowIndex)
	if currentWindow == "" {
		if active, ok := activeWindow(windows); ok {
			currentWindow = active.Index
		}
	}

	index := 0
	found := false
	for i, window := range windows {
		if window.Index == currentWindow {
			index = i
			found = true
			break
		}
	}

	if found {
		index = stepIndex(index, len(windows), direction)
	}

	targetWindow := windows[index].Index
	targetPane := firstActivePaneIndex(inputs.Panes, targetWindow)
	if targetPane == "" {
		targetPane = firstPaneIndex(inputs.Panes, targetWindow)
	}

	return Cursor{
		WindowIndex: targetWindow,
		PaneIndex:   targetPane,
	}, true, nil
}

func validateDirection(direction Direction) error {
	switch direction {
	case DirectionNext, DirectionPrev:
		return nil
	default:
		return ErrInvalidDirection
	}
}

func normalizedWindows(windows []Window) []Window {
	normalized := make([]Window, 0, len(windows))
	for _, window := range windows {
		index := strings.TrimSpace(window.Index)
		if index == "" {
			continue
		}
		normalized = append(normalized, Window{
			Index:     index,
			Name:      strings.TrimSpace(window.Name),
			PaneCount: window.PaneCount,
			Path:      strings.TrimSpace(window.Path),
			Active:    window.Active,
		})
	}
	return normalized
}

func normalizedPanes(panes []Pane) []Pane {
	normalized := make([]Pane, 0, len(panes))
	for _, pane := range panes {
		windowIndex := strings.TrimSpace(pane.WindowIndex)
		index := strings.TrimSpace(pane.Index)
		if windowIndex == "" || index == "" {
			continue
		}
		normalized = append(normalized, Pane{
			WindowIndex: windowIndex,
			Index:       index,
			Title:       strings.TrimSpace(pane.Title),
			Command:     strings.TrimSpace(pane.Command),
			Path:        strings.TrimSpace(pane.Path),
			Active:      pane.Active,
		})
	}
	return normalized
}

func activeWindow(windows []Window) (Window, bool) {
	for _, window := range windows {
		if window.Active {
			return window, true
		}
	}
	return Window{}, false
}

func activePane(panes []Pane) (Pane, bool) {
	for _, pane := range panes {
		if pane.Active {
			return pane, true
		}
	}
	return Pane{}, false
}

func panesForWindow(panes []Pane, windowIndex string) []Pane {
	filtered := make([]Pane, 0, len(panes))
	for _, pane := range panes {
		if pane.WindowIndex == windowIndex {
			filtered = append(filtered, pane)
		}
	}
	return filtered
}

func firstActivePaneIndex(panes []Pane, windowIndex string) string {
	for _, pane := range panes {
		if strings.TrimSpace(pane.WindowIndex) == windowIndex && pane.Active {
			return strings.TrimSpace(pane.Index)
		}
	}
	return ""
}

func firstPaneIndex(panes []Pane, windowIndex string) string {
	for _, pane := range panes {
		if strings.TrimSpace(pane.WindowIndex) == windowIndex {
			return strings.TrimSpace(pane.Index)
		}
	}
	return ""
}

func stepIndex(index int, count int, direction Direction) int {
	if count == 0 {
		return 0
	}
	if direction == DirectionPrev {
		return (index - 1 + count) % count
	}
	return (index + 1) % count
}
