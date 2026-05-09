package projmuxpicker

import (
	"strings"
	"testing"
)

func TestPromptLineWithCursorRendersInlineCount(t *testing.T) {
	t.Parallel()

	line := PromptLineWithCursor("› ", "abcd", 2, 1, 1, 20)
	if !strings.Contains(line, "ab"+CursorStart+"c"+Reset+"d") {
		t.Fatalf("PromptLineWithCursor() = %q, want styled cursor at query index", line)
	}
	if got := VisibleLen(line); got != 20 {
		t.Fatalf("VisibleLen(line) = %d, want 20", got)
	}
}

func TestRenderableListLinePadsSelectedStyleBeforeReset(t *testing.T) {
	t.Parallel()

	rendered := RenderableListLine(CurrentStart+"api"+Reset, 12)
	if !strings.Contains(rendered, CurrentStart+"api         "+Reset) {
		t.Fatalf("RenderableListLine() = %q, want padding inside style", rendered)
	}
}

func TestRenderableListLinePadsUnselectedStyleAfterReset(t *testing.T) {
	t.Parallel()

	line := "\x1b[90m~rp/web\x1b[0m \x1b[90mmaster\x1b[0m"
	padding := strings.Repeat(" ", 24-VisibleLen(line))
	rendered := RenderableListLine(line, 24)

	if !strings.HasSuffix(rendered, Reset+padding) {
		t.Fatalf("RenderableListLine() = %q, want inactive style reset before padding", rendered)
	}
	if strings.Contains(rendered, "master"+padding+Reset) {
		t.Fatalf("RenderableListLine() = %q, want inactive branch style not stretched", rendered)
	}
}

func TestInteractiveRowLinesUsesCurrentStyleForSimpleSelection(t *testing.T) {
	t.Parallel()

	line := InteractiveRowLines(Row{
		Label: "\x1b[36mAI Settings\x1b[0m  \x1b[90mdefault split mode\x1b[0m",
	}, true, false)[0]
	rendered := RenderableListLine(line, 48)

	if !strings.Contains(rendered, Pointer) || !strings.Contains(rendered, CurrentStart) {
		t.Fatalf("rendered selected line = %q, want projmux pointer and current-row style", rendered)
	}
	if strings.Contains(rendered, InverseStart) {
		t.Fatalf("rendered selected line = %q, want no terminal inverse selection style", rendered)
	}
	if !strings.Contains(rendered, "default split mode"+Reset+CurrentStart) {
		t.Fatalf("rendered selected line = %q, want current style restored after final label reset", rendered)
	}
	if !strings.HasSuffix(rendered, Reset) {
		t.Fatalf("rendered selected line = %q, want final reset", rendered)
	}
}

func TestSelectedContentKeepsCurrentStyleAfterReset(t *testing.T) {
	t.Parallel()

	rendered := SelectedContent("\x1b[1mapi\x1b[0m branch")
	if !strings.Contains(rendered, Reset+CurrentStart+" branch") {
		t.Fatalf("SelectedContent() = %q, want current style restored after reset", rendered)
	}
}
