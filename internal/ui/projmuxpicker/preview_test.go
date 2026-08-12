package projmuxpicker

import (
	"bytes"
	"strings"
	"testing"
)

func TestPreviewWidthUsesWindowPercent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		contentCols int
		want        int
	}{
		{contentCols: 76, want: 42},
		{contentCols: 96, want: 54},
		{contentCols: 116, want: 66},
	}

	for _, tt := range tests {
		if got := PreviewWidth(tt.contentCols, "right,60%,border-left"); got != tt.want {
			t.Fatalf("PreviewWidth(%d) = %d, want reference content width %d", tt.contentCols, got, tt.want)
		}
	}
}

func TestPreviewHeightUsesWindowPercent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		contentRows int
		want        int
	}{
		{contentRows: 18, want: 3},
		{contentRows: 28, want: 6},
		{contentRows: 38, want: 8},
	}

	for _, tt := range tests {
		if got := PreviewHeight(tt.contentRows, "down,25%,border-top"); got != tt.want {
			t.Fatalf("PreviewHeight(%d) = %d, want reference content height %d", tt.contentRows, got, tt.want)
		}
	}
}

func TestRenderSplitPreviewRowsExtendsSeparator(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	RenderSplitPreviewRows(&out, []string{"api"}, []string{"preview"}, Layout{Rows: 10, Cols: 80}, "right,60%,border-left", 1, 0, 1, 5)

	separatorRows := 0
	for line := range strings.SplitSeq(strings.TrimRight(out.String(), "\n"), "\n") {
		if strings.Contains(line, "│") {
			separatorRows++
		}
	}
	if separatorRows != 5 {
		t.Fatalf("separator rows = %d, want 5 in output %q", separatorRows, out.String())
	}
}

func TestRenderSplitPreviewRowsPadsBothPanes(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	RenderSplitPreviewRows(&out, []string{"api"}, []string{"preview"}, Layout{Rows: 10, Cols: 80}, "right,60%,border-left", 1, 0, 1, 3)

	for i, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		if got, want := VisibleLen(line), 80; got != want {
			t.Fatalf("split preview row %d width = %d, want %d: %q", i, got, want, line)
		}
	}
}

func TestRenderSplitPreviewRowsNormalizesPreviewTabsBeforeTruncating(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	RenderSplitPreviewRows(&out,
		[]string{"api"},
		[]string{"one\ttwo three four five six seven eight nine ten eleven twelve"},
		Layout{Rows: 10, Cols: 40},
		"right,60%,border-left",
		1,
		0,
		1,
		1,
	)

	line := strings.TrimRight(out.String(), "\n")
	if strings.Contains(line, "\t") {
		t.Fatalf("split preview row contains raw tab and can wrap unexpectedly: %q", line)
	}
	if got, want := VisibleLen(line), 40; got != want {
		t.Fatalf("split preview row width = %d, want %d: %q", got, want, line)
	}
}

func TestRenderSplitPreviewRowsKeepsRequestedViewport(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	RenderSplitPreviewRows(&out,
		[]string{"api", "detail"},
		[]string{"one", "two", "three", "four", "five"},
		Layout{Rows: 10, Cols: 80},
		"right,60%,border-left",
		10,
		3,
		4,
		3,
	)

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("split preview rows = %d, want fixed viewport of 3: %q", len(lines), out.String())
	}
	if !strings.Contains(out.String(), Scrollbar) {
		t.Fatalf("split preview output = %q, want scrollbar on fixed viewport", out.String())
	}
	if strings.Contains(out.String(), "four") || strings.Contains(out.String(), "five") {
		t.Fatalf("split preview output = %q, want preview clipped to viewport", out.String())
	}
}

func TestRenderDownPreviewPadsPreviewRows(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	RenderDownPreview(&out, []string{"preview"}, Layout{Rows: 10, Cols: 24})

	for i, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		if got, want := VisibleLen(line), 24; got != want {
			t.Fatalf("down preview row %d width = %d, want %d: %q", i, got, want, line)
		}
	}
}

func TestRenderInlinePreviewRowsTruncatesToLayoutWidth(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	RenderInlinePreviewRows(&out, []string{"one\ttwo three four five"}, Layout{Cols: 12})

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("inline preview lines = %d, want 3: %q", len(lines), out.String())
	}
	if strings.Contains(lines[2], "\t") {
		t.Fatalf("inline preview row contains raw tab and can wrap unexpectedly: %q", lines[2])
	}
	if got, want := VisibleLen(lines[2]), 12; got != want {
		t.Fatalf("inline preview row width = %d, want %d: %q", got, want, lines[2])
	}
}
