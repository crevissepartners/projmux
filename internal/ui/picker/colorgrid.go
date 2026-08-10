package picker

import (
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

// xterm-256 grid band layout. The three bands stack as one continuous vertical
// space of 8 logical rows: 1 base row (16 cells), 6 cube rows (36 cells each),
// and 1 grayscale row (24 cells).
const (
	colorGridBaseStart = 0
	colorGridBaseCount = 16
	colorGridCubeStart = 16
	colorGridCubeCount = 216
	colorGridCubeWidth = 36
	colorGridCubeRows  = 6
	colorGridGrayStart = 232
	colorGridGrayCount = 24
	colorGridTotalRows = 1 + colorGridCubeRows + 1
)

// colorGridRowCol maps a 0..255 color index to its (row, col) in the continuous
// 8-row band space.
func colorGridRowCol(index int) (row, col int) {
	switch {
	case index < colorGridCubeStart:
		return 0, index
	case index < colorGridGrayStart:
		n := index - colorGridCubeStart
		return 1 + n/colorGridCubeWidth, n % colorGridCubeWidth
	default:
		return colorGridTotalRows - 1, index - colorGridGrayStart
	}
}

// colorGridRowWidth returns the number of cells in a given logical band row.
func colorGridRowWidth(row int) int {
	switch {
	case row <= 0:
		return colorGridBaseCount
	case row < colorGridTotalRows-1:
		return colorGridCubeWidth
	default:
		return colorGridGrayCount
	}
}

// colorGridIndex maps a (row, col) back to a 0..255 color index. Col is clamped
// into the target row's width so vertical moves carry the column over safely.
func colorGridIndex(row, col int) int {
	if row < 0 {
		row = 0
	}
	if row > colorGridTotalRows-1 {
		row = colorGridTotalRows - 1
	}
	width := colorGridRowWidth(row)
	if col < 0 {
		col = 0
	}
	if col > width-1 {
		col = width - 1
	}
	switch {
	case row == 0:
		return colorGridBaseStart + col
	case row < colorGridTotalRows-1:
		return colorGridCubeStart + (row-1)*colorGridCubeWidth + col
	default:
		return colorGridGrayStart + col
	}
}

// colorGridMoveHorizontal moves the cursor within its own band row, clamping at
// the row ends (no wrap, no band crossing).
func colorGridMoveHorizontal(cur, delta int) int {
	row, col := colorGridRowCol(cur)
	return colorGridIndex(row, col+delta)
}

// colorGridMoveVertical moves the cursor between band rows, carrying the column
// over (clamped to the target row width) and clamping at the top/bottom.
func colorGridMoveVertical(cur, delta int) int {
	row, col := colorGridRowCol(cur)
	return colorGridIndex(row+delta, col)
}

// xterm256RGB returns the 24-bit RGB of an xterm-256 palette index using the
// standard mapping: 0-15 base table, 16-231 cube with steps
// {0,95,135,175,215,255}, 232-255 gray ramp 8+(i-232)*10.
func xterm256RGB(index int) (r, g, b int) {
	base := [16][3]int{
		{0, 0, 0}, {128, 0, 0}, {0, 128, 0}, {128, 128, 0},
		{0, 0, 128}, {128, 0, 128}, {0, 128, 128}, {192, 192, 192},
		{128, 128, 128}, {255, 0, 0}, {0, 255, 0}, {255, 255, 0},
		{0, 0, 255}, {255, 0, 255}, {0, 255, 255}, {255, 255, 255},
	}
	switch {
	case index < 16:
		c := base[index]
		return c[0], c[1], c[2]
	case index < 232:
		n := index - 16
		steps := [6]int{0, 95, 135, 175, 215, 255}
		return steps[n/36], steps[(n/6)%6], steps[n%6]
	default:
		gray := 8 + (index-232)*10
		return gray, gray, gray
	}
}

// colorGridHex formats an xterm-256 index as #rrggbb.
func colorGridHex(index int) string {
	r, g, b := xterm256RGB(index)
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

// colorGridMarkerFg picks a contrasting foreground (dark 16 on light cells,
// light 231 on dark) by Rec.601 luma of the cell's RGB so the cursor marker is
// locatable on any background color.
func colorGridMarkerFg(index int) int {
	r, g, b := xterm256RGB(index)
	luma := (299*r + 587*g + 114*b) / 1000
	if luma >= 128 {
		return 16
	}
	return 231
}

// runNativeColorGrid is the ColorGrid-mode interactive loop. It reuses the same
// raw-mode/alt-screen/key-reader/renderer plumbing as the list loop but renders
// the swatch grid and interprets keys for grid navigation only (search and the
// list focus/preview machinery are disabled).
func runNativeColorGrid(in io.Reader, out io.Writer, options Options) (Result, error) {
	cur := options.InitialIndex
	if cur < 0 || cur > 255 {
		cur = 0
	}
	layout := detectNativeLayout(in)
	renderer := projmuxpicker.FrameUpdateRenderer{}
	nativeDebugLogf("color-grid ui=%q start cur=%d layout=%dx%d", options.UI, cur, layout.Cols, layout.Rows)
	fmt.Fprint(out, nativeScreenEnter)
	defer leaveNativeInteractiveScreen(out)

	for {
		renderer.Render(out, nativeColorGridFrame(options, cur, layout))
		key, err := readNativeKey(in)
		if err != nil {
			if err == io.EOF {
				nativeDebugLogf("color-grid ui=%q result=closed reason=eof", options.UI)
				return Result{Closed: true}, nil
			}
			return Result{}, fmt.Errorf("read native picker key: %w", err)
		}
		if key.Name == "" && key.Text == "" {
			continue
		}
		if key.HasMouse {
			// Mouse is not wired in grid mode; ignore so it does not corrupt state.
			continue
		}
		nativeDebugLogf("color-grid ui=%q key name=%q text=%q cur=%d", options.UI, key.Name, key.Text, cur)
		switch key.Name {
		case "enter":
			hex := colorGridHex(cur)
			nativeDebugLogf("color-grid ui=%q result=enter cur=%d value=%q", options.UI, cur, hex)
			return Result{Key: "enter", Value: hex}, nil
		case "esc", "ctrl-c":
			nativeDebugLogf("color-grid ui=%q result=closed key=%q", options.UI, key.Name)
			return Result{Key: key.Name, Closed: true}, nil
		case "left":
			cur = colorGridMoveHorizontal(cur, -1)
		case "right":
			cur = colorGridMoveHorizontal(cur, 1)
		case "up", "ctrl-p", "ctrl-k":
			cur = colorGridMoveVertical(cur, -1)
		case "down", "ctrl-n", "ctrl-j":
			cur = colorGridMoveVertical(cur, 1)
		case "home":
			row, _ := colorGridRowCol(cur)
			cur = colorGridIndex(row, 0)
		case "end":
			row, _ := colorGridRowCol(cur)
			cur = colorGridIndex(row, colorGridRowWidth(row)-1)
		default:
			if key.Text == "h" {
				nativeDebugLogf("color-grid ui=%q result=hex cur=%d", options.UI, cur)
				return Result{Key: "hex"}, nil
			}
			// Ignore all other keys (search/query are disabled in grid mode).
		}
	}
}

// nativeColorGridFrame renders the full grid-mode frame: title chrome, three
// labeled swatch bands with a cursor marker on the current cell, a wide preview
// line, and the footer. Band rows wider than the content area are clipped (the
// same non-panic behavior the list frame relies on for wide content).
func nativeColorGridFrame(options Options, cur int, layout nativeLayout) string {
	contentLayout := nativeContentLayoutForOptions(layout, options)
	pickerTheme := nativeTheme(options)

	var top strings.Builder
	if header := strings.TrimSpace(options.Header); header != "" {
		fmt.Fprintln(&top, nativeHeaderLineWithTheme(pickerTheme, header, contentLayout.Cols))
	}

	var main strings.Builder
	cols := contentLayout.Cols

	// Base table band (indices 0..15).
	fmt.Fprintln(&main, "  Base (0-15)")
	fmt.Fprintln(&main, colorGridBandLine(colorGridBaseStart, colorGridBaseCount, cur, cols))

	// Cube band (indices 16..231), six rows of 36.
	fmt.Fprintln(&main, "  Cube (16-231)")
	for r := range colorGridCubeRows {
		start := colorGridCubeStart + r*colorGridCubeWidth
		fmt.Fprintln(&main, colorGridBandLine(start, colorGridCubeWidth, cur, cols))
	}

	// Grayscale band (indices 232..255).
	fmt.Fprintln(&main, "  Grayscale (232-255)")
	fmt.Fprintln(&main, colorGridBandLine(colorGridGrayStart, colorGridGrayCount, cur, cols))

	// Live preview line.
	fmt.Fprintln(&main, "")
	fmt.Fprintln(&main, colorGridPreviewLine(cur))

	var body strings.Builder
	writeNativeContentWithFooterWithTheme(&body, pickerTheme, top.String(), main.String(), options.Footer, contentLayout)

	var frame strings.Builder
	if len(options.TitleChips) > 0 {
		renderNativeFrameWithChips(&frame, body.String(), options.TitleChips, layout, options)
	} else {
		renderNativeFrameWithTitle(&frame, body.String(), options.Title, layout, options)
	}
	return frame.String()
}

// colorGridBandLine renders one band row: count swatches starting at start, each
// a two-cell colored block, with the cursor cell showing a contrasting "[]"
// marker. The line is clipped to cols so a narrow popup does not corrupt the
// frame.
func colorGridBandLine(start, count, cur, cols int) string {
	var b strings.Builder
	b.WriteString("  ")
	used := 2
	for i := range count {
		idx := start + i
		// Each swatch occupies two columns; clip when it would overflow.
		if cols > 0 && used+2 > cols {
			break
		}
		if idx == cur {
			fg := colorGridMarkerFg(idx)
			fmt.Fprintf(&b, "\x1b[48;5;%dm\x1b[38;5;%dm[]\x1b[0m", idx, fg)
		} else {
			fmt.Fprintf(&b, "\x1b[48;5;%dm  \x1b[0m", idx)
		}
		used += 2
	}
	return b.String()
}

// colorGridPreviewLine renders a wider block of the cursor color followed by the
// palette index label and its hex.
func colorGridPreviewLine(cur int) string {
	hex := colorGridHex(cur)
	return fmt.Sprintf("  \x1b[48;5;%dm        \x1b[0m  colour%d  %s", cur, cur, hex)
}
