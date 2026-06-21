package picker

import (
	"bytes"
	"strings"
	"testing"
)

func TestNativeColorGridFrameRendersSwatchesAndCursorMarker(t *testing.T) {
	t.Parallel()

	// Cursor on cube index 45 -> RGB (0,215,255), luma >= 128 so the marker
	// uses the dark foreground 16.
	frame := nativeColorGridFrame(Options{Title: "Color grid"}, 45, nativeLayout{Rows: 24, Cols: 120})

	if !strings.Contains(frame, "\x1b[48;5;45m") {
		t.Fatalf("frame missing swatch escape for color 45:\n%q", frame)
	}
	if !strings.Contains(frame, "\x1b[48;5;45m\x1b[38;5;16m[]\x1b[0m") {
		t.Fatalf("frame missing cursor marker at color 45:\n%q", frame)
	}
	if !strings.Contains(frame, "#00d7ff") {
		t.Fatalf("frame missing preview hex for color 45:\n%q", frame)
	}
	if !strings.Contains(frame, "colour45") {
		t.Fatalf("frame missing preview label for color 45:\n%q", frame)
	}
}

func TestNativeColorGridFrameMarkerLightForegroundOnDarkCell(t *testing.T) {
	t.Parallel()

	// Index 16 is the darkest cube cell (0,0,0); marker should use light fg 231.
	frame := nativeColorGridFrame(Options{}, 16, nativeLayout{Rows: 24, Cols: 120})
	if !strings.Contains(frame, "\x1b[48;5;16m\x1b[38;5;231m[]\x1b[0m") {
		t.Fatalf("frame missing light cursor marker at color 16:\n%q", frame)
	}
}

func TestNativeColorGridNavigationReturnsCursorHex(t *testing.T) {
	t.Parallel()

	const (
		left  = "\x1b[D"
		right = "\x1b[C"
		up    = "\x1b[A"
		down  = "\x1b[B"
		enter = "\r"
	)

	cases := []struct {
		name      string
		initial   int
		keys      string
		wantValue string
	}{
		{
			name:      "left at col 0 stays",
			initial:   0,
			keys:      left + enter,
			wantValue: "#000000", // index 0
		},
		{
			name:      "right at base row end stays",
			initial:   15,
			keys:      right + enter,
			wantValue: "#ffffff", // index 15
		},
		{
			name:      "up from cube row 0 lands in base",
			initial:   16, // cube row 0, col 0
			keys:      up + enter,
			wantValue: "#000000", // base index 0
		},
		{
			name:      "down from cube row 5 lands in grayscale",
			initial:   196, // cube row 5, col 0 (16 + 5*36)
			keys:      down + enter,
			wantValue: "#080808", // grayscale index 232 = 8,8,8
		},
		{
			name:      "right then left returns to start",
			initial:   100,
			keys:      right + left + enter,
			wantValue: colorGridHex(100),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			result, err := runNativeInteractive(strings.NewReader(tc.keys), &out, Options{
				UI:           "settings-theme-color-grid",
				ColorGrid:    true,
				InitialIndex: tc.initial,
			})
			if err != nil {
				t.Fatalf("runNativeInteractive() error = %v", err)
			}
			if result.Key != "enter" || result.Value != tc.wantValue {
				t.Fatalf("result = %#v, want enter with %q", result, tc.wantValue)
			}
		})
	}
}

func TestNativeColorGridHKeyReturnsHex(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	result, err := runNativeInteractive(strings.NewReader("h"), &out, Options{
		UI:        "settings-theme-color-grid",
		ColorGrid: true,
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if result.Key != "hex" {
		t.Fatalf("result = %#v, want Key==hex", result)
	}
}

func TestNativeColorGridEscClosesWithoutSelection(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	result, err := runNativeInteractive(strings.NewReader("\x1b"), &out, Options{
		ColorGrid: true,
	})
	if err != nil {
		t.Fatalf("runNativeInteractive() error = %v", err)
	}
	if !result.Closed {
		t.Fatalf("result = %#v, want Closed", result)
	}
}

func TestColorGridRowColRoundTrip(t *testing.T) {
	t.Parallel()

	for idx := 0; idx <= 255; idx++ {
		row, col := colorGridRowCol(idx)
		if got := colorGridIndex(row, col); got != idx {
			t.Fatalf("colorGridIndex(colorGridRowCol(%d)) = %d, want round trip", idx, got)
		}
	}
}
