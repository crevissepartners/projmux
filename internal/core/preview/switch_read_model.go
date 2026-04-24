package preview

import "strings"

// SwitchReadModel captures the preview context for a switch candidate path.
type SwitchReadModel struct {
	Path        string
	DisplayPath string
	SessionName string
	SessionMode string
	GitBranch   string
	Windows     []Window
	Panes       []Pane
	Popup       PopupReadModel
}

// SwitchReadModelInputs captures the pure inputs needed to derive switch
// preview state.
type SwitchReadModelInputs struct {
	Path               string
	DisplayPath        string
	SessionName        string
	SessionExists      bool
	GitBranch          string
	StoredSelection    Selection
	HasStoredSelection bool
	Windows            []Window
	Panes              []Pane
}

// BuildSwitchReadModel derives switch preview state from candidate metadata and
// optional tmux inventory.
func BuildSwitchReadModel(inputs SwitchReadModelInputs) SwitchReadModel {
	model := SwitchReadModel{
		Path:        strings.TrimSpace(inputs.Path),
		DisplayPath: strings.TrimSpace(inputs.DisplayPath),
		SessionName: strings.TrimSpace(inputs.SessionName),
		SessionMode: "new",
		GitBranch:   strings.TrimSpace(inputs.GitBranch),
		Windows:     normalizedWindows(inputs.Windows),
		Panes:       normalizedPanes(inputs.Panes),
	}

	if !inputs.SessionExists {
		return model
	}

	model.SessionMode = "existing"
	model.Popup = BuildPopupReadModel(PopupReadModelInputs{
		SessionName:        model.SessionName,
		StoredSelection:    inputs.StoredSelection,
		HasStoredSelection: inputs.HasStoredSelection,
		Windows:            inputs.Windows,
		Panes:              inputs.Panes,
	})

	return model
}
