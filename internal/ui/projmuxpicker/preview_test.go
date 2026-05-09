package projmuxpicker

import "testing"

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
