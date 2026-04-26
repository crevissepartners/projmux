package render

import (
	"strings"
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
			{WindowIndex: "2", Index: "4", Title: "tests", AttentionState: "reply", AIState: "waiting", AIAgent: "codex", AITopic: "approval needed", AttentionFocusArmed: "1", Command: "node", Path: "~rp/app"},
		},
		PaneSnapshot: "go test ./...\nok",
	})

	want := "" +
		"\x1b[1m\x1b[36mSession\x1b[0m\n" +
		"  \x1b[2mname\x1b[0m  app\n" +
		"  \x1b[2mwindows\x1b[0m  2\n" +
		"  \x1b[2mpane\x1b[0m  4 (window 2)\n" +
		"  \x1b[2mcmd\x1b[0m  codex\n" +
		"  \x1b[2mtitle\x1b[0m  tests\n" +
		"  \x1b[2mstatus\x1b[0m  badge=needs-reply state=waiting-for-you assistant=codex topic=approval needed clears-on-focus=yes\n" +
		"  \x1b[2mpath\x1b[0m  ~rp/app\n\n" +
		"\x1b[1m\x1b[36mWindows\x1b[0m\n" +
		"[1] shell               1p\n" +
		"\x1b[1m\x1b[32m[2] app                 2p\x1b[0m\n\n" +
		"\x1b[1m\x1b[36mPanes\x1b[0m\n" +
		"[2.3] server             go\n" +
		"\x1b[1m\x1b[32m[2.4] tests              codex  \x1b[2mbadge=needs-reply state=waiting-for-you assistant=codex topic=approval needed clears-on-focus=yes\x1b[0m\x1b[0m\n\n" +
		"\x1b[1m\x1b[36mPane Snapshot\x1b[0m\n" +
		"\x1b[2m────────────────────────────────────────────────────────────────\x1b[0m\n" +
		"go test ./...\nok\n"
	if got != want {
		t.Fatalf("RenderPopupPreview() = %q, want %q", got, want)
	}
}

func TestRenderPopupPreviewShowsShellCommandAsShellPaneTitle(t *testing.T) {
	t.Parallel()

	got := RenderPopupPreview(preview.PopupReadModel{
		SessionName:         "app",
		HasSelection:        true,
		SelectedWindowIndex: "1",
		SelectedPaneIndex:   "0",
		Windows: []preview.Window{
			{Index: "1", Name: "main", PaneCount: 1},
		},
		Panes: []preview.Pane{
			{WindowIndex: "1", Index: "0", Title: "main", Command: "zsh", Path: "~rp/app"},
		},
	})

	for _, want := range []string{
		"  \x1b[2mcmd\x1b[0m  zsh\n",
		"  \x1b[2mtitle\x1b[0m  zsh\n",
		"\x1b[1m\x1b[32m[1.0] zsh                zsh\x1b[0m\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("RenderPopupPreview() = %q, want substring %q", got, want)
		}
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
		"\x1b[1m\x1b[36mSession\x1b[0m\n" +
		"  \x1b[2mname\x1b[0m  app\n" +
		"  \x1b[2mwindows\x1b[0m  1\n" +
		"  \x1b[2mpane\x1b[0m  ? (window 5)\n\n" +
		"\x1b[1m\x1b[36mWindows\x1b[0m\n" +
		"\x1b[1m\x1b[32m[5] build               1p\x1b[0m\n\n" +
		"\x1b[1m\x1b[36mPanes\x1b[0m\n" +
		"(none)\n"
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
		"\x1b[1m\x1b[36mSession\x1b[0m\n" +
		"  \x1b[2mname\x1b[0m  app one preview\n" +
		"  \x1b[2mwindows\x1b[0m  1\n" +
		"  \x1b[2mpane\x1b[0m  ? (window ?)\n\n" +
		"\x1b[1m\x1b[36mWindows\x1b[0m\n" +
		"[1 2] main pane           2p\n\n" +
		"\x1b[1m\x1b[36mPanes\x1b[0m\n" +
		"[1 2.3 4] srv one            go test\n"
	if got != want {
		t.Fatalf("RenderPopupPreview() = %q, want %q", got, want)
	}
}
