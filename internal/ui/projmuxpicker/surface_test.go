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

func TestFrameChromeANSIGoldenLines(t *testing.T) {
	t.Parallel()

	footerLines := FooterBlockLines("\x1b[32mEnter: open\x1b[0m", 18)
	if len(footerLines) != 2 {
		t.Fatalf("FooterBlockLines() len = %d, want 2: %#v", len(footerLines), footerLines)
	}
	row := RenderableListLine(InteractiveRowLines(Row{
		Label: "\x1b[36mapi\x1b[0m",
	}, true, false)[0], 18)
	cases := []struct {
		name      string
		line      string
		want      string
		wantWidth int
	}{
		{
			name:      "title",
			line:      frameTitlebarLine(DefaultTheme, 18, "\x1b[31mProjects\x1b[0m"),
			want:      TitlebarRule + "│" + TitlebarStart + " \x1b[31mProjects" + Reset + TitlebarStart + strings.Repeat(" ", 9) + TitlebarRule + "│" + Reset,
			wantWidth: 20,
		},
		{
			name:      "header",
			line:      HeaderLine("\x1b[36mPinned", 18),
			want:      "\x1b[36mPinned" + Reset + strings.Repeat(" ", 12),
			wantWidth: 18,
		},
		{
			name:      "search prompt",
			line:      PromptLineWithCursor("› ", "api", 3, 2, 9, 18),
			want:      MutedStart + "Search" + Reset + " › api" + CursorStart + " " + Reset + "  " + MutedStart + "2/9" + Reset,
			wantWidth: 18,
		},
		{
			name:      "row",
			line:      row,
			want:      Pointer + "\x1b[36mapi" + Reset + CurrentStart + strings.Repeat(" ", 13) + Reset,
			wantWidth: 18,
		},
		{
			name:      "footer separator",
			line:      footerLines[0],
			want:      MutedStart + strings.Repeat(GapLine, 18) + Reset,
			wantWidth: 18,
		},
		{
			name:      "footer text",
			line:      footerLines[1],
			want:      "\x1b[32mEnter: open" + Reset + strings.Repeat(" ", 7),
			wantWidth: 18,
		},
	}
	for _, tt := range cases {
		if tt.line != tt.want {
			t.Fatalf("%s line = %q, want %q", tt.name, tt.line, tt.want)
		}
		if hasActiveStyle(tt.line) {
			t.Fatalf("%s line = %q, want no active style at line end", tt.name, tt.line)
		}
		if got := VisibleLen(tt.line); got != tt.wantWidth {
			t.Fatalf("%s width = %d, want %d: %q", tt.name, got, tt.wantWidth, tt.line)
		}
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

func TestRenderableListLineRendersGapAtFullListWidth(t *testing.T) {
	t.Parallel()

	rendered := RenderableListLine(gapSentinel, 12)

	if strings.HasPrefix(rendered, " ") {
		t.Fatalf("RenderableListLine(gap) = %q, want no leading indent", rendered)
	}
	if got := VisibleLen(rendered); got != 12 {
		t.Fatalf("VisibleLen(RenderableListLine(gap)) = %d, want 12", got)
	}
	if !strings.Contains(rendered, strings.Repeat(GapLine, 12)) {
		t.Fatalf("RenderableListLine(gap) = %q, want full-width gap line", rendered)
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
		if strings.Contains(line, "┃┃┃") || strings.Contains(line, "|||") || !strings.Contains(line, "▌") {
			t.Fatalf("selected continuation line = %q, want single pointer-width continuation bar", line)
		}
		if !strings.Contains(line, CurrentStart) {
			t.Fatalf("selected continuation line = %q, want current-row style", line)
		}
	}
}

func TestInteractiveRowLinesIndentsMetadataOneExtraColumn(t *testing.T) {
	t.Parallel()

	lines := InteractiveRowLines(Row{
		Label:     "api",
		MetaLines: []string{"~rp/api main"},
	}, false, true)

	if got, want := lines[1], "   ~rp/api main"; got != want {
		t.Fatalf("metadata line = %q, want %q", got, want)
	}
}

func TestInteractiveRowLinesIndentsSelectedMetadataAfterContinuation(t *testing.T) {
	t.Parallel()

	lines := InteractiveRowLines(Row{
		Label:     "api",
		MetaLines: []string{"~rp/api main"},
	}, true, true)

	plain := stripANSIForSurfaceTest(lines[1])
	if got, want := plain, "▌  ~rp/api main"; got != want {
		t.Fatalf("selected metadata line = %q, want %q", plain, want)
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
		if !strings.HasPrefix(plain, "▌ ") {
			t.Fatalf("selected continuation line = %q, want compact pointer-width indent", line)
		}
	}
}

func TestInteractiveRowLinesAlignsUnselectedMetaWithProjectName(t *testing.T) {
	t.Parallel()

	lines := InteractiveRowLines(Row{
		Label: "api\n  ~rp/api\n  shell main",
	}, false, true)

	if len(lines) != 3 {
		t.Fatalf("InteractiveRowLines() len = %d, want 3: %#v", len(lines), lines)
	}
	if got, want := lines[1], "  ~rp/api"; got != want {
		t.Fatalf("pwd line = %q, want %q", got, want)
	}
	if got, want := lines[2], "  shell main"; got != want {
		t.Fatalf("tabs line = %q, want %q", got, want)
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

func TestListLinesWithScrollbarRowsKeepsViewportTrack(t *testing.T) {
	t.Parallel()

	lines := []string{"item 3", "detail"}
	rendered := ListLinesWithScrollbarRows(lines, 10, 3, 4, 12, 6)

	if len(rendered) != 6 {
		t.Fatalf("rendered rows = %d, want viewport-sized rows: %#v", len(rendered), rendered)
	}
	if got := scrollbarCount(rendered); got == 0 {
		t.Fatalf("scrollbar count = %d, want scrollbar on viewport track: %#v", got, rendered)
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
