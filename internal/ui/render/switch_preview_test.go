package render

import (
	"testing"

	corepreview "github.com/es5h/projmux/internal/core/preview"
)

func TestRenderSwitchPreviewForExistingSession(t *testing.T) {
	t.Parallel()

	got := RenderSwitchPreview(corepreview.SwitchReadModel{
		Path:          "/home/tester/source/repos/app",
		DisplayPath:   "~rp/app",
		SessionName:   "app",
		SessionMode:   "existing",
		GitBranch:     "main",
		KubeContext:   "kind-dev",
		KubeNamespace: "apps",
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
			PaneSnapshot: "npm test\npass",
		},
	}, "popup")

	want := "" +
		"\x1b[1m\x1b[36mTarget\x1b[0m\n" +
		"  \x1b[2mdir\x1b[0m  ~rp/app\n" +
		"  \x1b[2msession\x1b[0m  app\n" +
		"  \x1b[2mmode\x1b[0m  \x1b[32mexisting\x1b[0m\n" +
		"  \x1b[2mgit\x1b[0m  main\n" +
		"  \x1b[2mk8s\x1b[0m  \x1b[31mkind-dev\x1b[0m/\x1b[34mapps\x1b[0m\n\n" +
		"\x1b[1m\x1b[36mWindows\x1b[0m\n" +
		"[1] shell               1p\n" +
		"\x1b[1m\x1b[32m[2] app                 2p\x1b[0m\n\n" +
		"\x1b[1m\x1b[36mPanes\x1b[0m\n" +
		"[2.0] server             go\n" +
		"\x1b[1m\x1b[32m[2.1] tests              gotest\x1b[0m\n\n" +
		"\x1b[1m\x1b[36mPane Snapshot\x1b[0m\n" +
		"\x1b[2m────────────────────────────────────────────────────────────────\x1b[0m\n" +
		"npm test\npass\n"
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
		"\x1b[1m\x1b[36mTarget\x1b[0m\n" +
		"  \x1b[2mdir\x1b[0m  /tmp/app\n" +
		"  \x1b[2msession\x1b[0m  tmp-app\n" +
		"  \x1b[2mmode\x1b[0m  \x1b[33mnew session\x1b[0m\n\n" +
		"\x1b[1m\x1b[36mAction\x1b[0m\n" +
		"  \x1b[2menter\x1b[0m  switch/create this session\n" +
		"  \x1b[2mresult\x1b[0m  tmux new-session -d -s <name> -c <dir>\n"
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
		KubeContext: "kind-dev",
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
		"k8s:\x1b[31mkind-dev\x1b[0m/\x1b[34mdefault\x1b[0m\n\n" +
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
