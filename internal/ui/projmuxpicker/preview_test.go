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
			t.Fatalf("PreviewWidth(%d) = %d, want fzf-measured content width %d", tt.contentCols, got, tt.want)
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
			t.Fatalf("PreviewHeight(%d) = %d, want fzf-measured content height %d", tt.contentRows, got, tt.want)
		}
	}
}

func TestRenderSplitPreviewRowsExtendsSeparator(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	RenderSplitPreviewRows(&out, []string{"api"}, []string{"preview"}, Layout{Rows: 10, Cols: 80}, "right,60%,border-left", 1, 0, 1, 5)

	separatorRows := 0
	for _, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
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
