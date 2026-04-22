package preview

import "testing"

func TestBuildPopupReadModelFallsBackToActiveSelectionWhenStoreMissing(t *testing.T) {
	t.Parallel()

	model := BuildPopupReadModel(PopupReadModelInputs{
		SessionName: "app",
		Windows: []Window{
			{Index: "1"},
			{Index: "2", Active: true},
		},
		Panes: []Pane{
			{WindowIndex: "1", Index: "0"},
			{WindowIndex: "2", Index: "3"},
			{WindowIndex: "2", Index: "4", Active: true},
		},
	})

	if model.SessionName != "app" {
		t.Fatalf("SessionName = %q, want app", model.SessionName)
	}
	if !model.HasSelection {
		t.Fatal("HasSelection = false, want true")
	}
	if model.SelectedWindowIndex != "2" {
		t.Fatalf("SelectedWindowIndex = %q, want 2", model.SelectedWindowIndex)
	}
	if model.SelectedPaneIndex != "4" {
		t.Fatalf("SelectedPaneIndex = %q, want 4", model.SelectedPaneIndex)
	}
	if len(model.Panes) != 2 {
		t.Fatalf("len(Panes) = %d, want 2", len(model.Panes))
	}
}

func TestBuildPopupReadModelUsesStoredSelectionWhenAvailable(t *testing.T) {
	t.Parallel()

	model := BuildPopupReadModel(PopupReadModelInputs{
		SessionName: "app",
		StoredSelection: Selection{
			SessionName: "app",
			WindowIndex: "3",
			PaneIndex:   "8",
		},
		HasStoredSelection: true,
		Windows: []Window{
			{Index: "2", Active: true},
			{Index: "3"},
		},
		Panes: []Pane{
			{WindowIndex: "2", Index: "4", Active: true},
			{WindowIndex: "3", Index: "7"},
			{WindowIndex: "3", Index: "8"},
		},
	})

	if !model.HasSelection {
		t.Fatal("HasSelection = false, want true")
	}
	if model.SelectedWindowIndex != "3" {
		t.Fatalf("SelectedWindowIndex = %q, want 3", model.SelectedWindowIndex)
	}
	if model.SelectedPaneIndex != "8" {
		t.Fatalf("SelectedPaneIndex = %q, want 8", model.SelectedPaneIndex)
	}
	if len(model.Panes) != 2 {
		t.Fatalf("len(Panes) = %d, want 2", len(model.Panes))
	}
	if got := model.Panes[0].WindowIndex; got != "3" {
		t.Fatalf("Panes[0].WindowIndex = %q, want 3", got)
	}
}

func TestBuildPopupReadModelFallsBackToActivePaneWhenStoredPaneIsMissing(t *testing.T) {
	t.Parallel()

	model := BuildPopupReadModel(PopupReadModelInputs{
		SessionName: "app",
		StoredSelection: Selection{
			SessionName: "app",
			WindowIndex: "3",
			PaneIndex:   "99",
		},
		HasStoredSelection: true,
		Windows: []Window{
			{Index: "2"},
			{Index: "3"},
		},
		Panes: []Pane{
			{WindowIndex: "3", Index: "7"},
			{WindowIndex: "3", Index: "8", Active: true},
		},
	})

	if !model.HasSelection {
		t.Fatal("HasSelection = false, want true")
	}
	if model.SelectedWindowIndex != "3" {
		t.Fatalf("SelectedWindowIndex = %q, want 3", model.SelectedWindowIndex)
	}
	if model.SelectedPaneIndex != "8" {
		t.Fatalf("SelectedPaneIndex = %q, want 8", model.SelectedPaneIndex)
	}
}

func TestBuildPopupReadModelKeepsWindowSelectionWhenNoPanesExist(t *testing.T) {
	t.Parallel()

	model := BuildPopupReadModel(PopupReadModelInputs{
		SessionName: "app",
		StoredSelection: Selection{
			SessionName: "app",
			WindowIndex: "5",
		},
		HasStoredSelection: true,
		Windows: []Window{
			{Index: "5"},
		},
	})

	if !model.HasSelection {
		t.Fatal("HasSelection = false, want true")
	}
	if model.SelectedWindowIndex != "5" {
		t.Fatalf("SelectedWindowIndex = %q, want 5", model.SelectedWindowIndex)
	}
	if model.SelectedPaneIndex != "" {
		t.Fatalf("SelectedPaneIndex = %q, want empty", model.SelectedPaneIndex)
	}
	if len(model.Panes) != 0 {
		t.Fatalf("len(Panes) = %d, want 0", len(model.Panes))
	}
}

func TestBuildPopupReadModelReturnsNoSelectionWhenNoWindowsExist(t *testing.T) {
	t.Parallel()

	model := BuildPopupReadModel(PopupReadModelInputs{
		SessionName: "app",
		Panes: []Pane{
			{WindowIndex: "1", Index: "0", Active: true},
		},
	})

	if model.HasSelection {
		t.Fatal("HasSelection = true, want false")
	}
	if model.SelectedWindowIndex != "" {
		t.Fatalf("SelectedWindowIndex = %q, want empty", model.SelectedWindowIndex)
	}
	if model.SelectedPaneIndex != "" {
		t.Fatalf("SelectedPaneIndex = %q, want empty", model.SelectedPaneIndex)
	}
	if len(model.Windows) != 0 {
		t.Fatalf("len(Windows) = %d, want 0", len(model.Windows))
	}
	if len(model.Panes) != 0 {
		t.Fatalf("len(Panes) = %d, want 0", len(model.Panes))
	}
}
