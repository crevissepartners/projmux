package preview

import "testing"

func TestBuildSwitchReadModelKeepsNewCandidatesWithoutPopupState(t *testing.T) {
	t.Parallel()

	model := BuildSwitchReadModel(SwitchReadModelInputs{
		Path:        "/home/tester/source/repos/app",
		DisplayPath: "~rp/app",
		SessionName: "app",
	})

	if got, want := model.SessionMode, "new"; got != want {
		t.Fatalf("SessionMode = %q, want %q", got, want)
	}
	if model.Popup.HasSelection {
		t.Fatalf("Popup.HasSelection = true, want false")
	}
}

func TestBuildSwitchReadModelUsesPopupSelectionForExistingSessions(t *testing.T) {
	t.Parallel()

	model := BuildSwitchReadModel(SwitchReadModelInputs{
		Path:               "/home/tester/source/repos/app",
		DisplayPath:        "~rp/app",
		SessionName:        "app",
		SessionExists:      true,
		StoredSelection:    Selection{SessionName: "app", WindowIndex: "2", PaneIndex: "1"},
		HasStoredSelection: true,
		Windows: []Window{
			{Index: "1"},
			{Index: "2", Active: true},
		},
		Panes: []Pane{
			{WindowIndex: "2", Index: "0"},
			{WindowIndex: "2", Index: "1", Active: true},
		},
	})

	if got, want := model.SessionMode, "existing"; got != want {
		t.Fatalf("SessionMode = %q, want %q", got, want)
	}
	if got, want := model.Popup.SelectedWindowIndex, "2"; got != want {
		t.Fatalf("SelectedWindowIndex = %q, want %q", got, want)
	}
	if got, want := model.Popup.SelectedPaneIndex, "1"; got != want {
		t.Fatalf("SelectedPaneIndex = %q, want %q", got, want)
	}
}
