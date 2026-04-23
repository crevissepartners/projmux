package render

import (
	"testing"

	corepreview "github.com/es5h/projmux/internal/core/preview"
)

func TestRenderSwitchPreviewForExistingSession(t *testing.T) {
	t.Parallel()

	got := RenderSwitchPreview(corepreview.SwitchReadModel{
		Path:        "/home/tester/source/repos/app",
		DisplayPath: "~rp/app",
		SessionName: "app",
		SessionMode: "existing",
		Popup: corepreview.PopupReadModel{
			HasSelection:        true,
			SelectedWindowIndex: "2",
			SelectedPaneIndex:   "1",
			Windows: []corepreview.Window{
				{Index: "1", Name: "shell", PaneCount: 1, Path: "~/"},
				{Index: "2", Name: "app", PaneCount: 2, Path: "~rp/app"},
			},
			Panes: []corepreview.Pane{
				{WindowIndex: "2", Index: "0", Title: "server", Command: "go", Path: "~rp/app"},
				{WindowIndex: "2", Index: "1", Title: "tests", Command: "gotest", Path: "~rp/app"},
			},
		},
	})

	want := "" +
		"dir: ~rp/app\n" +
		"session: app\n" +
		"state: existing\n" +
		"selected: window=2 pane=1\n" +
		"windows:\n" +
		"    1 | shell | 1 panes | ~/\n" +
		"  * 2 | app | 2 panes | ~rp/app\n" +
		"panes:\n" +
		"    0 | server | go | ~rp/app\n" +
		"  * 1 | tests | gotest | ~rp/app\n"
	if got != want {
		t.Fatalf("RenderSwitchPreview() = %q, want %q", got, want)
	}
}

func TestRenderSwitchPreviewForNewSessionShowsEmptyInventory(t *testing.T) {
	t.Parallel()

	got := RenderSwitchPreview(corepreview.SwitchReadModel{
		Path:        "/tmp/app",
		SessionName: "tmp-app",
		SessionMode: "new",
	})

	want := "" +
		"dir: /tmp/app\n" +
		"session: tmp-app\n" +
		"state: new\n" +
		"selected: none\n" +
		"windows:\n" +
		"  (none)\n" +
		"panes:\n" +
		"  (none)\n"
	if got != want {
		t.Fatalf("RenderSwitchPreview() = %q, want %q", got, want)
	}
}
