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
		GitBranch:   "main",
		Windows: []corepreview.Window{
			{Index: "1", Name: "shell", PaneCount: 1, Path: "~/"},
			{Index: "2", Name: "app", PaneCount: 2, Path: "~rp/app"},
		},
		Panes: []corepreview.Pane{
			{WindowIndex: "2", Index: "0", Title: "server", Command: "go", Path: "~rp/app"},
			{WindowIndex: "2", Index: "1", Title: "tests", Command: "gotest", Path: "~rp/app"},
		},
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
	}, "popup")

	want := "" +
		"dir: ~rp/app\n" +
		"session: app\n" +
		"state: existing\n" +
		"git: main\n" +
		"summary: 2w  2p  w2.p1\n" +
		"selected: w2.p1\n" +
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
	}, "popup")

	want := "" +
		"dir: /tmp/app\n" +
		"session: tmp-app\n" +
		"state: new\n" +
		"summary: 0w  0p\n" +
		"selected: none\n" +
		"windows:\n" +
		"  (none)\n" +
		"panes:\n" +
		"  (none)\n"
	if got != want {
		t.Fatalf("RenderSwitchPreview() = %q, want %q", got, want)
	}
}

func TestRenderSwitchPreviewForSidebarMatchesLegacySections(t *testing.T) {
	t.Parallel()

	got := RenderSwitchPreview(corepreview.SwitchReadModel{
		Path:        "/home/tester/source/repos/app",
		DisplayPath: "~rp/app",
		SessionName: "app",
		SessionMode: "existing",
		Windows: []corepreview.Window{
			{Index: "1", Name: "shell"},
			{Index: "2", Name: "app"},
		},
		Panes: []corepreview.Pane{
			{WindowIndex: "1", Index: "0", Title: "shell"},
			{WindowIndex: "2", Index: "0", Title: "server"},
			{WindowIndex: "2", Index: "1", Title: "tests"},
		},
	}, "sidebar")

	want := "" +
		"\x1b[1m\x1b[36mDir\x1b[0m\n" +
		"~rp/app\n\n" +
		"\x1b[1m\x1b[36mWindows\x1b[0m\n" +
		"[1] shell\n" +
		"[2] server | tests\n"
	if got != want {
		t.Fatalf("RenderSwitchPreview() = %q, want %q", got, want)
	}
}

func TestRenderSwitchPreviewForSidebarNewSessionShowsStatus(t *testing.T) {
	t.Parallel()

	got := RenderSwitchPreview(corepreview.SwitchReadModel{
		Path:        "/tmp/app",
		DisplayPath: "/tmp/app",
		SessionMode: "new",
	}, "sidebar")

	want := "" +
		"\x1b[1m\x1b[36mDir\x1b[0m\n" +
		"/tmp/app\n\n" +
		"\x1b[1m\x1b[36mStatus\x1b[0m\n" +
		"\x1b[33mnew session\x1b[0m\n"
	if got != want {
		t.Fatalf("RenderSwitchPreview() = %q, want %q", got, want)
	}
}
