package render

import (
	"strings"
	"testing"

	corepreview "github.com/crevissepartners/projmux/internal/core/preview"
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
		"  \x1b[2msession\x1b[0m  app\n" +
		"  \x1b[2mmode\x1b[0m  \x1b[32mexisting\x1b[0m\n" +
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
			{WindowIndex: "2", Index: "1", Title: "tests", AttentionState: "busy"},
			{WindowIndex: "2", Index: "2", Title: "review", AIState: "waiting", AIAgent: "codex", AITopic: "projmux-2"},
		},
	}, "sidebar")

	want := "" +
		"k8s:\x1b[31mkind-dev\x1b[0m/\x1b[34mdefault\x1b[0m\n\n" +
		"\x1b[1m\x1b[36mWindows\x1b[0m\n" +
		"[1] shell\n" +
		"[2] server | \x1b[38;2;255;204;102m●\x1b[0m tests | \x1b[32m●\x1b[0m projmux-2\n"
	if got != want {
		t.Fatalf("RenderSwitchPreview() = %q, want %q", got, want)
	}
}

func TestRenderSwitchPreviewForSidebarUsesSemanticPromptBadge(t *testing.T) {
	t.Parallel()

	got := RenderSwitchPreview(corepreview.SwitchReadModel{
		SessionMode: "existing",
		Windows: []corepreview.Window{
			{Index: "1", Name: "app"},
		},
		Panes: []corepreview.Pane{
			{WindowIndex: "1", Index: "0", Title: "agent", AIState: "waiting", AIAgent: "codex", AITopic: "approval needed", AIBadgeKind: "approval_required"},
		},
	}, "sidebar")

	if !strings.Contains(got, "\x1b[38;5;214m●\x1b[0m approval needed") {
		t.Fatalf("RenderSwitchPreview() = %q, want semantic prompt warning badge", got)
	}
}

func TestRenderSwitchPreviewStylesLeadTopicPrefixOnlyInRenderer(t *testing.T) {
	t.Parallel()

	got := RenderSwitchPreview(corepreview.SwitchReadModel{
		SessionMode: "existing",
		Windows: []corepreview.Window{
			{Index: "1", Name: "app"},
		},
		Panes: []corepreview.Pane{
			{WindowIndex: "1", Index: "0", Title: "agent", AIAgent: "codex", AITopic: "[Lead:QA] review topic"},
			{WindowIndex: "1", Index: "1", Title: "agent", AIAgent: "codex", AITopic: "[lead:poc] measure"},
		},
	}, "sidebar")

	for _, want := range []string{
		"\x1b[1m\x1b[38;2;255;204;102m[Lead:QA]\x1b[0m review topic",
		"\x1b[1m\x1b[38;2;255;204;102m[lead:poc]\x1b[0m measure",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("RenderSwitchPreview() = %q, want styled lead prefix %q", got, want)
		}
	}
	if strings.Contains(got, "@projmux_ai_topic") || strings.Contains(got, "#[") {
		t.Fatalf("RenderSwitchPreview() = %q, want ANSI renderer styling only", got)
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
		"\x1b[1m\x1b[36mStatus\x1b[0m\n" +
		"\x1b[33mnew session\x1b[0m\n"
	if got != want {
		t.Fatalf("RenderSwitchPreview() = %q, want %q", got, want)
	}
}
