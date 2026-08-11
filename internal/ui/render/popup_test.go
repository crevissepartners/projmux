package render

import (
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/preview"
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
		"  \x1b[2mtitle\x1b[0m  approval needed\n" +
		"  \x1b[2mstatus\x1b[0m  badge=needs-reply state=waiting-for-you assistant=codex topic=approval needed clears-on-focus=yes\n" +
		"  \x1b[2mpath\x1b[0m  ~rp/app\n\n" +
		"\x1b[1m\x1b[36mWindows\x1b[0m\n" +
		"[1] shell               1p\n" +
		"\x1b[1m\x1b[32m[2] app                 2p\x1b[0m\n\n" +
		"\x1b[1m\x1b[36mPanes\x1b[0m\n" +
		"[2.3] server             go\n" +
		"\x1b[1m\x1b[32m[2.4] approval needed    codex  \x1b[2mbadge=needs-reply state=waiting-for-you assistant=codex topic=approval needed clears-on-focus=yes\x1b[0m\x1b[0m\n\n" +
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

func TestPopupVisiblePaneIdentityUsesLabelTopicShellTitleOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		pane preview.Pane
		want string
	}{
		{name: "label", pane: preview.Pane{Label: "[lead:ship] user label", AIAgent: "codex", AITopic: "AI topic", Command: "zsh", Title: "raw title"}, want: "[lead:ship] user label"},
		{name: "topic", pane: preview.Pane{AIAgent: "codex", AITopic: "AI topic", Command: "zsh", Title: "raw title"}, want: "AI topic"},
		{name: "shell", pane: preview.Pane{Command: "zsh", Title: "raw title"}, want: "zsh"},
		{name: "title", pane: preview.Pane{Command: "nvim", Title: "raw title"}, want: "raw title"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := displayPaneTitlePlain(tt.pane); got != tt.want {
				t.Fatalf("displayPaneTitlePlain(%#v) = %q, want %q", tt.pane, got, tt.want)
			}
		})
	}
	if got := displayPaneTitle(tests[0].pane); strings.Contains(got, "\x1b[") {
		t.Fatalf("user label = %q, must not receive AI-topic lead styling", got)
	}
}

func TestRenderPopupPreviewStylesProgressAndLeadPrefixOnlyInRenderer(t *testing.T) {
	t.Parallel()

	got := RenderPopupPreview(preview.PopupReadModel{
		SessionName:         "app",
		HasSelection:        true,
		SelectedWindowIndex: "1",
		SelectedPaneIndex:   "0",
		Windows: []preview.Window{
			{Index: "1", Name: "app", PaneCount: 1},
		},
		Panes: []preview.Pane{
			{WindowIndex: "1", Index: "0", Title: "agent", AttentionState: "busy", AIState: "thinking", AIAgent: "codex", AITopic: "[Lead:Ship] release", Command: "node", Path: "~rp/app"},
		},
	})

	for _, want := range []string{
		"  \x1b[2mtitle\x1b[0m  \x1b[1m\x1b[38;2;255;204;102m[Lead:Ship]\x1b[0m release\n",
		"badge=\x1b[38;2;255;204;102mworking\x1b[0m state=\x1b[38;2;255;204;102mworking\x1b[0m assistant=codex topic=\x1b[1m\x1b[38;2;255;204;102m[Lead:Ship]\x1b[0m release",
		"\x1b[1m\x1b[32m[1.0] \x1b[1m\x1b[38;2;255;204;102m[Lead:Ship]\x1b[0m relea",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("RenderPopupPreview() = %q, want substring %q", got, want)
		}
	}
	if strings.Contains(got, "@projmux_ai_topic") || strings.Contains(got, "#[") {
		t.Fatalf("RenderPopupPreview() = %q, want ANSI renderer styling only", got)
	}
}

func TestRenderPopupPreviewShowsSemanticBadgeKind(t *testing.T) {
	t.Parallel()

	got := RenderPopupPreview(preview.PopupReadModel{
		SessionName:         "dev",
		HasSelection:        true,
		SelectedWindowIndex: "1",
		SelectedPaneIndex:   "0",
		Windows: []preview.Window{
			{Index: "1", Name: "app", PaneCount: 1},
		},
		Panes: []preview.Pane{
			{WindowIndex: "1", Index: "0", Title: "agent", AIState: "waiting", AIBadgeKind: "input_required", AIAgent: "codex", AITopic: "needs target", Command: "node"},
		},
	})

	if !strings.Contains(got, "badge=\x1b[38;5;214minput-required\x1b[0m") {
		t.Fatalf("RenderPopupPreview() = %q, want semantic input badge", got)
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
