package render

import (
	"testing"

	"github.com/crevissepartners/projmux/internal/theme"
)

func TestVisualLenIgnoresTmuxEscapes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"#[fg=red]abc#[default]", 3},
		{"#[fg=" + theme.TmuxAccentAIFg + ",bold]Claude#[default] 5h", 9},
		{"a#[fg=red]b#[default]c", 3},
		{"abc#[broken", 11},
		{"hash#tag", 8},
	}
	for _, tc := range cases {
		if got := VisualLen(tc.in); got != tc.want {
			t.Fatalf("VisualLen(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
