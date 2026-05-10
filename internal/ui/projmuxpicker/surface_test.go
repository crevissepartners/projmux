package projmuxpicker

import (
	"strings"
	"testing"
)

func TestPromptLineWithCursorRendersInlineCount(t *testing.T) {
	t.Parallel()

	line := PromptLineWithCursor("› ", "abcd", 2, 1, 1, 20)
	if !strings.Contains(line, "Search") {
		t.Fatalf("PromptLineWithCursor() = %q, want explicit search label", line)
	}
	if !strings.Contains(line, "ab"+CursorStart+"c"+Reset+"d") {
		t.Fatalf("PromptLineWithCursor() = %q, want styled cursor at query index", line)
	}
	if got := VisibleLen(line); got != 20 {
		t.Fatalf("VisibleLen(line) = %d, want 20", got)
	}
}

func TestSeparatorLineUsesMutedChromeAtFullWidth(t *testing.T) {
	t.Parallel()

	line := SeparatorLine(12)
	if !strings.HasPrefix(line, MutedStart) || !strings.HasSuffix(line, Reset) {
		t.Fatalf("SeparatorLine() = %q, want muted styled separator", line)
	}
	if got := VisibleLen(line); got != 12 {
		t.Fatalf("VisibleLen(SeparatorLine()) = %d, want 12", got)
	}
	if !strings.Contains(line, strings.Repeat(GapLine, 12)) {
		t.Fatalf("SeparatorLine() = %q, want gap characters", line)
	}
}

func TestFooterBlockLinesUsesMutedSeparator(t *testing.T) {
	t.Parallel()

	lines := FooterBlockLines("Enter: open", 16)
	if len(lines) != 2 {
		t.Fatalf("FooterBlockLines() len = %d, want 2: %#v", len(lines), lines)
	}
	if !strings.HasPrefix(lines[0], MutedStart) || VisibleLen(lines[0]) != 16 {
		t.Fatalf("footer separator = %q, want muted full-width separator", lines[0])
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
	if !strings.HasPrefix(rendered, Pointer) {
		t.Fatalf("rendered selected line = %q, want pointer in current-row gutter", rendered)
	}
	if strings.HasPrefix(rendered, Pointer+CurrentStart) {
		t.Fatalf("rendered selected line = %q, want selected content to reuse pointer gutter style", rendered)
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

func TestSelectedLineReusesStyledGutter(t *testing.T) {
	t.Parallel()

	rendered := SelectedLine(Pointer, "\x1b[36mAI\x1b[0m Settings")

	if !strings.HasPrefix(rendered, Pointer) {
		t.Fatalf("SelectedLine() = %q, want pointer prefix", rendered)
	}
	if strings.HasPrefix(rendered, Pointer+CurrentStart) {
		t.Fatalf("SelectedLine() = %q, want no duplicated current style after pointer", rendered)
	}
	if !strings.Contains(rendered, Reset+CurrentStart+" Settings") {
		t.Fatalf("SelectedLine() = %q, want current style restored after embedded reset", rendered)
	}
}

func TestInteractiveRowLinesUsesContinuationMarkerForSelectedMultiline(t *testing.T) {
	t.Parallel()

	lines := InteractiveRowLines(Row{
		Label:     "api",
		MetaLines: []string{"~rp/api", "master"},
	}, true, true)

	if len(lines) != 3 {
		t.Fatalf("InteractiveRowLines() len = %d, want 3: %#v", len(lines), lines)
	}
	if !strings.HasPrefix(lines[0], Pointer) {
		t.Fatalf("first selected line = %q, want pointer", lines[0])
	}
	for _, line := range lines[1:] {
		if !strings.HasPrefix(line, Continuation) {
			t.Fatalf("selected continuation line = %q, want continuation marker", line)
		}
		if strings.Contains(line, "┃┃┃") || !strings.Contains(line, "|") {
			t.Fatalf("selected continuation line = %q, want single continuation bar", line)
		}
		if !strings.Contains(line, CurrentStart) {
			t.Fatalf("selected continuation line = %q, want current-row style", line)
		}
	}
}

func TestInteractiveRowLinesUsesCompactSelectedMetaIndent(t *testing.T) {
	t.Parallel()

	lines := InteractiveRowLines(Row{
		Label: "api\n  ~rp/api\n  shell main",
	}, true, true)

	if len(lines) != 3 {
		t.Fatalf("InteractiveRowLines() len = %d, want 3: %#v", len(lines), lines)
	}
	for _, line := range lines[1:] {
		plain := stripANSIForSurfaceTest(line)
		if strings.HasPrefix(plain, "|||") || strings.HasPrefix(plain, "┃┃┃") {
			t.Fatalf("selected continuation line = %q, want no repeated bar marker", line)
		}
		if !strings.HasPrefix(plain, "| ") {
			t.Fatalf("selected continuation line = %q, want compact single-bar indent", line)
		}
	}
}

func TestListLinesWithScrollbarUsesProportionalThumb(t *testing.T) {
	t.Parallel()

	lines := []string{"item 0", "item 1", "item 2", "item 3", "item 4"}
	rendered := ListLinesWithScrollbar(lines, 10, 0, 5, 12)

	if got := scrollbarCount(rendered); got != 3 {
		t.Fatalf("scrollbar count = %d, want proportional thumb of 3 rows: %#v", got, rendered)
	}
	if !strings.HasSuffix(rendered[0], Scrollbar) || !strings.HasSuffix(rendered[2], Scrollbar) {
		t.Fatalf("scrollbar = %#v, want thumb anchored at top", rendered)
	}
}

func TestListLinesWithScrollbarMovesThumbGradually(t *testing.T) {
	t.Parallel()

	lines := []string{"item 2", "item 3", "item 4", "item 5", "item 6"}
	rendered := ListLinesWithScrollbar(lines, 10, 2, 7, 12)

	if got := scrollbarCount(rendered); got != 3 {
		t.Fatalf("scrollbar count = %d, want stable proportional thumb length: %#v", got, rendered)
	}
	if !strings.HasSuffix(rendered[1], Scrollbar) || !strings.HasSuffix(rendered[3], Scrollbar) {
		t.Fatalf("scrollbar = %#v, want thumb moved by one row near the middle", rendered)
	}
}

func scrollbarCount(lines []string) int {
	count := 0
	for _, line := range lines {
		if strings.HasSuffix(line, Scrollbar) {
			count++
		}
	}
	return count
}

func stripANSIForSurfaceTest(value string) string {
	var out strings.Builder
	for i := 0; i < len(value); {
		if value[i] != '\x1b' {
			out.WriteByte(value[i])
			i++
			continue
		}
		i++
		for i < len(value) && value[i] != 'm' {
			i++
		}
		if i < len(value) {
			i++
		}
	}
	return out.String()
}
