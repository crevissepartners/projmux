package render

import (
	"testing"

	"github.com/es5h/projmux/internal/core/preview"
)

func TestRenderPopupPreviewWithSelectedWindowAndPane(t *testing.T) {
	t.Parallel()

	got := RenderPopupPreview(preview.PopupReadModel{
		SessionName:         "app",
		HasSelection:        true,
		SelectedWindowIndex: "2",
		SelectedPaneIndex:   "4",
		Windows: []preview.Window{
			{Index: "1"},
			{Index: "2"},
		},
		Panes: []preview.Pane{
			{WindowIndex: "2", Index: "3"},
			{WindowIndex: "2", Index: "4"},
		},
	})

	want := "" +
		"session: app\n" +
		"selected: window=2 pane=4\n" +
		"windows:\n" +
		"    1\n" +
		"  * 2\n" +
		"panes:\n" +
		"    3\n" +
		"  * 4\n"
	if got != want {
		t.Fatalf("RenderPopupPreview() = %q, want %q", got, want)
	}
}

func TestRenderPopupPreviewWithWindowOnlySelection(t *testing.T) {
	t.Parallel()

	got := RenderPopupPreview(preview.PopupReadModel{
		SessionName:         "app",
		HasSelection:        true,
		SelectedWindowIndex: "5",
		Windows: []preview.Window{
			{Index: "5"},
		},
	})

	want := "" +
		"session: app\n" +
		"selected: window=5 pane=-\n" +
		"windows:\n" +
		"  * 5\n" +
		"panes:\n" +
		"  (none)\n"
	if got != want {
		t.Fatalf("RenderPopupPreview() = %q, want %q", got, want)
	}
}

func TestRenderPopupPreviewWithoutSelectionSanitizesOutput(t *testing.T) {
	t.Parallel()

	got := RenderPopupPreview(preview.PopupReadModel{
		SessionName: "app\tone\npreview",
		Windows: []preview.Window{
			{Index: "1\t2"},
		},
		Panes: []preview.Pane{
			{WindowIndex: "1\t2", Index: "3\n4"},
		},
	})

	want := "" +
		"session: app one preview\n" +
		"selected: none\n" +
		"windows:\n" +
		"    1 2\n" +
		"panes:\n" +
		"    3 4\n"
	if got != want {
		t.Fatalf("RenderPopupPreview() = %q, want %q", got, want)
	}
}
