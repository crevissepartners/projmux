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
			{Index: "1", Name: "shell", PaneCount: 1, Path: "~/"},
			{Index: "2", Name: "app", PaneCount: 2, Path: "~rp/app"},
		},
		Panes: []preview.Pane{
			{WindowIndex: "2", Index: "3", Title: "server", Command: "go", Path: "~rp/app"},
			{WindowIndex: "2", Index: "4", Title: "tests", Command: "gotest", Path: "~rp/app"},
		},
	})

	want := "" +
		"session: app\n" +
		"selected: window=2 pane=4\n" +
		"windows:\n" +
		"    1 | shell | 1 panes | ~/\n" +
		"  * 2 | app | 2 panes | ~rp/app\n" +
		"panes:\n" +
		"    3 | server | go | ~rp/app\n" +
		"  * 4 | tests | gotest | ~rp/app\n"
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
			{Index: "5", Name: "build", PaneCount: 1, Path: "~rp/build"},
		},
	})

	want := "" +
		"session: app\n" +
		"selected: window=5 pane=-\n" +
		"windows:\n" +
		"  * 5 | build | 1 panes | ~rp/build\n" +
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
			{Index: "1\t2", Name: "main\tpane", PaneCount: 2, Path: "/tmp/app\tone"},
		},
		Panes: []preview.Pane{
			{WindowIndex: "1\t2", Index: "3\n4", Title: "srv\tone", Command: "go\ntest", Path: "/tmp/app\none"},
		},
	})

	want := "" +
		"session: app one preview\n" +
		"selected: none\n" +
		"windows:\n" +
		"    1 2 | main pane | 2 panes | /tmp/app one\n" +
		"panes:\n" +
		"    3 4 | srv one | go test | /tmp/app one\n"
	if got != want {
		t.Fatalf("RenderPopupPreview() = %q, want %q", got, want)
	}
}
