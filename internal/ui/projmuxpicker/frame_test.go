package projmuxpicker

import (
	"bytes"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/theme"
)

func TestRendererRenderFrameUsesCRLFRowsForRawTTY(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	DefaultRenderer().RenderFrame(&out, "hello\nworld\n", Layout{Rows: 4, Cols: 12})

	rendered := out.String()
	if !strings.Contains(rendered, "╮\r\n│") || !strings.Contains(rendered, "│\r\n╰") {
		t.Fatalf("RenderFrame() = %q, want CRLF-delimited frame rows", rendered)
	}
	if strings.HasSuffix(rendered, "\r\n") {
		t.Fatalf("RenderFrame() = %q, want no trailing CRLF after bottom border", rendered)
	}
}

func TestRendererRenderFramePreservesExactGeometry(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	DefaultRenderer().RenderFrame(&out, "title\nrow", Layout{Rows: 5, Cols: 12})

	lines := strings.Split(out.String(), "\r\n")
	if got, want := len(lines), 5; got != want {
		t.Fatalf("frame line count = %d, want exact layout rows %d: %q", got, want, out.String())
	}
	if !strings.HasPrefix(lines[0], "╭") || !strings.HasSuffix(lines[0], "╮") {
		t.Fatalf("top frame row = %q, want visible top border", lines[0])
	}
	if !strings.HasPrefix(lines[len(lines)-1], "╰") || !strings.HasSuffix(lines[len(lines)-1], "╯") {
		t.Fatalf("bottom frame row = %q, want visible bottom border", lines[len(lines)-1])
	}
	for i, line := range lines {
		if got, want := VisibleLen(line), 12; got != want {
			t.Fatalf("frame line %d width = %d, want %d: %q", i, got, want, line)
		}
		if i > 0 && i < len(lines)-1 && (!strings.HasPrefix(line, "│") || !strings.HasSuffix(line, "│")) {
			t.Fatalf("content frame row %d = %q, want continuous vertical borders", i, line)
		}
	}
}

func TestRendererRenderFrameWithTitleKeepsDefaultWhenTitleEmpty(t *testing.T) {
	t.Parallel()

	var plain bytes.Buffer
	var titled bytes.Buffer
	DefaultRenderer().RenderFrame(&plain, "hello", Layout{Rows: 4, Cols: 20})
	DefaultRenderer().RenderFrameWithTitle(&titled, "hello", "", Layout{Rows: 4, Cols: 20})

	if got, want := titled.String(), plain.String(); got != want {
		t.Fatalf("RenderFrameWithTitle(empty) = %q, want default frame %q", got, want)
	}
}

func TestThemeFromEffectiveFallbackPaintsFrameBackground(t *testing.T) {
	t.Parallel()

	effective := theme.ResolveTheme(theme.ThemeConfig{})
	themed := NewRenderer(ThemeFromEffective(effective))
	layout := Layout{Rows: 8, Cols: 32}
	listRow := RenderableListLine(InteractiveRowLines(Row{
		Label: "\x1b[36mapi\x1b[0m",
	}, true, false)[0], 30)
	content := strings.Join([]string{
		HeaderLine("\x1b[36mPinned", 30),
		listRow,
		PromptLineWithCursor("› ", "api", 3, 2, 9, 30),
		strings.Join(FooterBlockLines("\x1b[32mEnter: open\x1b[0m", 30), "\n"),
	}, "\n")

	var defaultFrame bytes.Buffer
	var themedFrame bytes.Buffer
	DefaultRenderer().RenderFrame(&defaultFrame, content, layout)
	themed.RenderFrame(&themedFrame, content, layout)
	if got, want := themedFrame.String(), defaultFrame.String(); got == want {
		t.Fatalf("fallback themed frame = default frame, want app surface SGR")
	}

	var defaultTitle bytes.Buffer
	var themedTitle bytes.Buffer
	DefaultRenderer().RenderFrameWithTitle(&defaultTitle, content, "Projects", layout)
	themed.RenderFrameWithTitle(&themedTitle, content, "Projects", layout)
	if got, want := themedTitle.String(), defaultTitle.String(); got == want {
		t.Fatalf("fallback themed title frame = default frame, want app surface SGR")
	}

	chips := []Chip{{Label: "Projects", Active: true}, {Label: "Settings"}}
	var defaultChips bytes.Buffer
	var themedChips bytes.Buffer
	DefaultRenderer().RenderFrameWithChips(&defaultChips, content, chips, layout)
	themed.RenderFrameWithChips(&themedChips, content, chips, layout)
	if got, want := themedChips.String(), defaultChips.String(); got == want {
		t.Fatalf("fallback themed chip frame = default frame, want app surface SGR")
	}

	fallbackTheme := ThemeFromEffective(effective)
	style := fallbackTheme.Background + fallbackTheme.Foreground
	if style == "" {
		t.Fatal("fallback frame style empty, want app surface/chrome_foreground SGR")
	}
	for _, rendered := range []string{themedFrame.String(), themedTitle.String(), themedChips.String()} {
		if !strings.Contains(rendered, style) {
			t.Fatalf("fallback themed frame = %q, want fallback surface/chrome_foreground SGR %q", rendered, style)
		}
		for line := range strings.SplitSeq(rendered, "\r\n") {
			if !strings.HasPrefix(line, style) {
				t.Fatalf("fallback themed row = %q, want style prefix %q", line, style)
			}
			assertFrameResetsResumeStyleOrEnd(t, line, style)
		}
	}
}

func TestThemeFromEffectiveAppliesGlobalSurfaceForeground(t *testing.T) {
	t.Parallel()

	effective := theme.ResolveTheme(theme.ThemeConfig{
		Background: "#010203",
		Surface:    "#040506",
		Foreground: "#aabbcc",
	})
	renderer := NewRenderer(ThemeFromEffective(effective))
	var out bytes.Buffer
	renderer.RenderFrameWithTitle(&out, "api", "Projects", Layout{Rows: 5, Cols: 18})
	rendered := out.String()

	for _, want := range []string{"\x1b[48;2;4;5;6m", "\x1b[38;2;170;187;204m"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered frame = %q, want global SGR %q", rendered, want)
		}
	}
	if banned := "\x1b[48;2;1;2;3m"; strings.Contains(rendered, banned) {
		t.Fatalf("rendered frame = %q, must not use pane background SGR %q", rendered, banned)
	}
	for _, banned := range []string{"\x1b[48;2;17;34;51m", "\x1b[38;2;68;85;102m"} {
		if strings.Contains(rendered, banned) {
			t.Fatalf("rendered frame = %q, must not contain unrelated SGR %q", rendered, banned)
		}
	}
}

func TestThemeFromEffectiveBackgroundOnlyDoesNotRepaintPickerSurface(t *testing.T) {
	t.Parallel()

	effective := theme.ResolveTheme(theme.ThemeConfig{Background: "#010203"})
	renderer := NewRenderer(ThemeFromEffective(effective))
	var out bytes.Buffer
	renderer.RenderFrameWithTitle(&out, "api", "Projects", Layout{Rows: 5, Cols: 18})
	rendered := out.String()

	if banned := "\x1b[48;2;1;2;3m"; strings.Contains(rendered, banned) {
		t.Fatalf("rendered frame = %q, must not use pane background SGR %q", rendered, banned)
	}
}

func TestThemeFromEffectiveMapsSemanticRoles(t *testing.T) {
	t.Parallel()

	effective := theme.ResolveTheme(theme.ThemeConfig{
		SurfaceActive: "#010203",
		Foreground:    "#111213",
		Muted:         "#212223",
		Accent:        "#313233",
		Warning:       "#414243",
		Critical:      "#515253",
	})
	pickerTheme := ThemeFromEffective(effective)

	for name, values := range map[string][2]string{
		"selected": {pickerTheme.Selected, "\x1b[48;2;1;2;3m\x1b[38;2;17;18;19m"},
		"muted":    {pickerTheme.Muted, "\x1b[38;2;33;34;35m"},
		"accent":   {pickerTheme.Accent, "\x1b[38;2;49;50;51m"},
		"warning":  {pickerTheme.Warning, "\x1b[38;2;65;66;67m"},
		"critical": {pickerTheme.Critical, "\x1b[38;2;81;82;83m"},
	} {
		if values[0] != values[1] {
			t.Fatalf("%s role = %q, want %q", name, values[0], values[1])
		}
	}

	selected := RenderableListLineWithTheme(pickerTheme, InteractiveRowLinesWithTheme(pickerTheme, Row{Label: "\x1b[36mapi\x1b[0m"}, true, false)[0], 16)
	if !strings.Contains(selected, pickerTheme.Selected) || !strings.Contains(selected, pickerTheme.Accent+"▌"+pickerTheme.Selected) {
		t.Fatalf("selected row = %q, want themed selected/accent roles", selected)
	}
}

func TestRendererRenderFrameWithTitleUsesTitlebarRow(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	DefaultRenderer().RenderFrameWithTitle(&out, "hello", "Projects", Layout{Rows: 5, Cols: 24})

	lines := strings.Split(out.String(), "\r\n")
	if got, want := len(lines), 5; got != want {
		t.Fatalf("frame line count = %d, want exact layout rows %d: %q", got, want, out.String())
	}
	if strings.Contains(lines[0], "Projects") {
		t.Fatalf("top frame row = %q, want plain border without title text", lines[0])
	}
	if !strings.Contains(lines[1], " Projects ") {
		t.Fatalf("titlebar row = %q, want title inside picker-owned titlebar", lines[1])
	}
	if strings.Contains(lines[1], TitlebarStart) || strings.Contains(lines[1], TitlebarRule) {
		t.Fatalf("titlebar row = %q, want frame styling without titlebar overlay ANSI", lines[1])
	}
	if strings.Contains(lines[1], "▌") {
		t.Fatalf("titlebar row = %q, want no red accent marker next to title", lines[1])
	}
	if strings.Contains(lines[1], TitlebarRule+"─") || strings.Contains(lines[1], strings.Repeat("─", 2)) {
		t.Fatalf("titlebar row = %q, want no rule fill after title", lines[1])
	}
	if got, want := VisibleLen(lines[1]), 24; got != want {
		t.Fatalf("titlebar row width = %d, want %d: %q", got, want, lines[1])
	}
	if !strings.HasPrefix(lines[2], "├") || !strings.HasSuffix(lines[2], "┤") {
		t.Fatalf("titlebar divider row = %q, want full-width divider below titlebar", lines[2])
	}
	if !strings.Contains(lines[3], "hello") {
		t.Fatalf("content row = %q, want content below titlebar divider", lines[3])
	}
}

func TestFrameTitlebarLineResetsAroundBordersAndPadsBody(t *testing.T) {
	t.Parallel()

	const innerWidth = 18
	line := frameTitlebarLine(DefaultTheme, innerWidth, "Projects")

	if got, want := line, "│ Projects "+strings.Repeat(" ", 8)+"│"; got != want {
		t.Fatalf("frameTitlebarLine() = %q, want %q", got, want)
	}
	if got, want := VisibleLen(line), innerWidth+2; got != want {
		t.Fatalf("VisibleLen(titlebar line) = %d, want %d: %q", got, want, line)
	}
	if hasActiveStyle(line) {
		t.Fatalf("frameTitlebarLine() = %q, want reset after right border", line)
	}
}

func TestFrameTitlebarLineUsesFrameBackgroundForeground(t *testing.T) {
	t.Parallel()

	theme := DefaultTheme
	theme.Background = "\x1b[48;2;1;2;3m"
	theme.Foreground = "\x1b[38;2;170;187;204m"
	line := frameTitlebarLine(theme, 18, "\x1b[31mProjects\x1b[0m")
	style := theme.Background + theme.Foreground

	if !strings.HasPrefix(line, style+"│ ") {
		t.Fatalf("frameTitlebarLine() = %q, want frame style before titlebar content", line)
	}
	if !strings.Contains(line, "\x1b[31mProjects"+Reset+style+strings.Repeat(" ", 9)) {
		t.Fatalf("frameTitlebarLine() = %q, want embedded reset to resume frame style for padding", line)
	}
	if !strings.HasSuffix(line, "│"+Reset) {
		t.Fatalf("frameTitlebarLine() = %q, want final reset after right border", line)
	}
	if strings.Contains(line, TitlebarStart) || strings.Contains(line, TitlebarRule) {
		t.Fatalf("frameTitlebarLine() = %q, want no titlebar overlay ANSI", line)
	}
}

func TestRendererFrameBackgroundResumesAfterContentResetBeforePadding(t *testing.T) {
	t.Parallel()

	theme := DefaultTheme
	theme.Background = "\x1b[48;2;1;2;3m"
	theme.Foreground = "\x1b[38;2;170;187;204m"
	style := theme.Background + theme.Foreground
	content := "\x1b[31mapi\x1b[0m"
	var out bytes.Buffer
	NewRenderer(theme).RenderFrame(&out, content, Layout{Rows: 4, Cols: 12})

	lines := strings.Split(out.String(), "\r\n")
	if got, want := len(lines), 4; got != want {
		t.Fatalf("frame rows = %d, want %d: %q", got, want, out.String())
	}
	contentRow := lines[1]
	if !strings.Contains(contentRow, "\x1b[31mapi"+Reset+style+strings.Repeat(" ", 7)+"│") {
		t.Fatalf("content row = %q, want background style resumed for padding and right border", contentRow)
	}
	assertFrameResetsResumeStyleOrEnd(t, contentRow, style)
	if got, want := VisibleLen(contentRow), 12; got != want {
		t.Fatalf("content row width = %d, want %d: %q", got, want, contentRow)
	}
}

func assertFrameResetsResumeStyleOrEnd(t *testing.T, line, style string) {
	t.Helper()
	for start := 0; ; {
		idx := strings.Index(line[start:], Reset)
		if idx < 0 {
			return
		}
		after := start + idx + len(Reset)
		if after == len(line) || strings.HasPrefix(line[after:], style) {
			start = after
			continue
		}
		t.Fatalf("line = %q, want reset followed by frame style %q or row end", line, style)
	}
}

func TestFrameTitlebarChipsLineResetsAroundBordersAndPadsBody(t *testing.T) {
	t.Parallel()

	const innerWidth = 18
	line := frameTitlebarChipsLine(DefaultTheme, innerWidth, []Chip{
		{Label: "A", Active: true},
		{Label: "B"},
	})

	wantBody := " " +
		ChipActiveStart + " A " + Reset +
		" " +
		ChipInactiveStart + " B " + Reset +
		strings.Repeat(" ", 10)
	if got, want := line, "│"+wantBody+"│"; got != want {
		t.Fatalf("frameTitlebarChipsLine() = %q, want %q", got, want)
	}
	if got, want := VisibleLen(line), innerWidth+2; got != want {
		t.Fatalf("VisibleLen(chip titlebar line) = %d, want %d: %q", got, want, line)
	}
	if hasActiveStyle(line) {
		t.Fatalf("frameTitlebarChipsLine() = %q, want reset after right border", line)
	}
}

func TestFrameTitlebarChipsLineUsesFrameStyleForGaps(t *testing.T) {
	t.Parallel()

	theme := DefaultTheme
	theme.Background = "\x1b[48;2;1;2;3m"
	theme.Foreground = "\x1b[38;2;170;187;204m"
	line := frameTitlebarChipsLine(theme, 18, []Chip{
		{Label: "A", Active: true},
		{Label: "B"},
	})
	style := theme.Background + theme.Foreground

	if !strings.Contains(line, ChipActiveStart+" A "+Reset+style+" "+ChipInactiveStart+" B "+Reset+style) {
		t.Fatalf("frameTitlebarChipsLine() = %q, want chip gap and right pad to resume frame style", line)
	}
	if strings.Contains(line, TitlebarStart) || strings.Contains(line, TitlebarRule) {
		t.Fatalf("frameTitlebarChipsLine() = %q, want no titlebar overlay ANSI", line)
	}
}

func TestFrameTitlebarChipsLineKeepsRightPadStyledWhenStripFills(t *testing.T) {
	t.Parallel()

	const innerWidth = 10
	line := frameTitlebarChipsLine(DefaultTheme, innerWidth, []Chip{
		{Label: "Project Settings", Active: true},
	})

	if got, want := VisibleLen(line), innerWidth+2; got != want {
		t.Fatalf("VisibleLen(chip titlebar line) = %d, want %d: %q", got, want, line)
	}
	if !strings.Contains(line, ChipActiveStart+" Projec "+Reset) {
		t.Fatalf("frameTitlebarChipsLine() = %q, want truncated chip before right pad", line)
	}
	if !strings.HasSuffix(line, Reset+" │") {
		t.Fatalf("frameTitlebarChipsLine() = %q, want inherited right padding cell before right border", line)
	}
	if strings.Contains(line, TitlebarStart) || strings.Contains(line, TitlebarRule) {
		t.Fatalf("frameTitlebarChipsLine() = %q, want no titlebar overlay ANSI", line)
	}
}

func TestRendererContentLayoutWithTitleReservesTitlebarRow(t *testing.T) {
	t.Parallel()

	layout := DefaultRenderer().ContentLayoutWithTitle(Layout{Rows: 10, Cols: 40}, "Projects")
	if got, want := layout.Rows, 6; got != want {
		t.Fatalf("ContentLayoutWithTitle().Rows = %d, want frame inner height minus titlebar %d", got, want)
	}
	if got, want := layout.Cols, 38; got != want {
		t.Fatalf("ContentLayoutWithTitle().Cols = %d, want frame inner width %d", got, want)
	}
}

func TestRendererContentLayoutUsesFrameInnerWidth(t *testing.T) {
	t.Parallel()

	layout := DefaultRenderer().ContentLayout(Layout{Rows: 10, Cols: 40})
	if got, want := layout.Cols, 38; got != want {
		t.Fatalf("ContentLayout().Cols = %d, want frame inner width %d", got, want)
	}
}

func TestFrameUpdateRendererDiffsAfterFirstFrame(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderer := FrameUpdateRenderer{}
	renderer.Render(&out, "top\r\none\r\nbottom")
	renderer.Render(&out, "top\r\ntwo\r\nbottom")

	rendered := out.String()
	if got, want := strings.Count(rendered, SyncUpdateEnter), 2; got != want {
		t.Fatalf("synchronized update enter count = %d, want %d: %q", got, want, rendered)
	}
	if got, want := strings.Count(rendered, SyncUpdateLeave), 2; got != want {
		t.Fatalf("synchronized update leave count = %d, want %d: %q", got, want, rendered)
	}
	if got := strings.Count(rendered, "top"); got != 1 {
		t.Fatalf("top row render count = %d, want initial full frame only: %q", got, rendered)
	}
	if !strings.Contains(rendered, "\x1b[2;1Htwo") {
		t.Fatalf("rendered = %q, want cursor-addressed changed row", rendered)
	}
	if !strings.Contains(rendered, "bottom\r"+SyncUpdateLeave) {
		t.Fatalf("rendered = %q, want carriage return before synchronized update leave", rendered)
	}
}

func TestFrameUpdateRendererSkipsUnchangedFrame(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	renderer := FrameUpdateRenderer{}
	renderer.Render(&out, "top\r\none\r\nbottom")
	first := out.String()
	renderer.Render(&out, "top\r\none\r\nbottom")

	if got := out.String(); got != first {
		t.Fatalf("unchanged frame emitted output: before %q after %q", first, got)
	}
	if got, want := strings.Count(out.String(), SyncUpdateEnter), 1; got != want {
		t.Fatalf("synchronized update enter count = %d, want %d for unchanged second frame: %q", got, want, out.String())
	}
}

func TestFrameUpdateRendererCoalescesEachFrameUpdate(t *testing.T) {
	t.Parallel()

	out := &countingWriter{}
	renderer := FrameUpdateRenderer{}
	renderer.Render(out, "top\r\none\r\nbottom")
	renderer.Render(out, "top\r\ntwo\r\nbottom")

	if got, want := out.writeCount, 2; got != want {
		t.Fatalf("write count = %d, want %d one-write rendered frame updates; output = %q", got, want, out.String())
	}
}

func TestRenderFullFrameUpdateAlwaysHomesAndWritesFrame(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	RenderFullFrameUpdate(&out, "frame")

	rendered := out.String()
	if !strings.HasPrefix(rendered, SyncUpdateEnter+"\x1b[Hframe") {
		t.Fatalf("RenderFullFrameUpdate() = %q, want sync wrapper and home cursor before frame", rendered)
	}
	if !strings.HasSuffix(rendered, "\r"+SyncUpdateLeave) {
		t.Fatalf("RenderFullFrameUpdate() = %q, want sync update leave suffix", rendered)
	}
}

func TestTruncateANSIClosesStyleWhenCutBeforeReset(t *testing.T) {
	t.Parallel()

	got := TruncateANSI("\x1b[90mProject Root is a long hint\x1b[0m", 12)
	if !strings.HasSuffix(got, Reset) {
		t.Fatalf("TruncateANSI() = %q, want trailing reset", got)
	}
	if gotLen := VisibleLen(got); gotLen != 12 {
		t.Fatalf("VisibleLen(truncated) = %d, want 12; value = %q", gotLen, got)
	}
}

func TestVisibleLenUsesTerminalCellWidth(t *testing.T) {
	t.Parallel()

	if got, want := VisibleLen("프로젝트"), 8; got != want {
		t.Fatalf("VisibleLen(korean) = %d, want terminal cell width %d", got, want)
	}
	if got, want := VisibleLen("api⏳✅"), 7; got != want {
		t.Fatalf("VisibleLen(lower emoji) = %d, want terminal cell width %d", got, want)
	}
	if got, want := VisibleLen("api🔔"), 5; got != want {
		t.Fatalf("VisibleLen(emoji) = %d, want terminal cell width %d", got, want)
	}
	if got, want := VisibleLen("☑️"), 1; got != want {
		t.Fatalf("VisibleLen(variation selector) = %d, want terminal cell width %d", got, want)
	}
	if got, want := VisibleLen("e\u0301"), 1; got != want {
		t.Fatalf("VisibleLen(combining) = %d, want terminal cell width %d", got, want)
	}
}

func TestVisibleLenStripsTitlebarANSISequences(t *testing.T) {
	t.Parallel()

	value := TitlebarStart + " Usage " + Reset + TitlebarStart + strings.Repeat(" ", 4) + Reset
	if got, want := VisibleLen(value), 11; got != want {
		t.Fatalf("VisibleLen(titlebar ANSI) = %d, want %d: %q", got, want, value)
	}
}

func TestTruncateANSIRespectsWideRuneCells(t *testing.T) {
	t.Parallel()

	got := TruncateANSI("프로젝트 api", 7)
	if got != "프로젝" {
		t.Fatalf("TruncateANSI(wide) = %q, want not to split past cell budget", got)
	}
	if gotLen := VisibleLen(got); gotLen != 6 {
		t.Fatalf("VisibleLen(truncated wide) = %d, want 6", gotLen)
	}
}

type countingWriter struct {
	bytes.Buffer
	writeCount int
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.writeCount++
	return w.Buffer.Write(p)
}

func TestRendererRenderFrameWithChipsUsesTmuxToneTokens(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	DefaultRenderer().RenderFrameWithChips(
		&out,
		"hello",
		[]Chip{
			{Label: "Global", Active: true},
			{Label: "Project"},
		},
		Layout{Rows: 5, Cols: 40},
	)

	lines := strings.Split(out.String(), "\r\n")
	if got, want := len(lines), 5; got != want {
		t.Fatalf("frame line count = %d, want exact layout rows %d: %q", got, want, out.String())
	}
	if strings.Contains(lines[0], "Global") || strings.Contains(lines[0], "Project") {
		t.Fatalf("top frame row = %q, want plain border without chip labels", lines[0])
	}
	chipRow := lines[1]
	if !strings.Contains(chipRow, " Global ") || !strings.Contains(chipRow, " Project ") {
		t.Fatalf("chip row = %q, want both chip labels padded with single cells", chipRow)
	}
	if !strings.Contains(chipRow, ChipActiveStart) {
		t.Fatalf("chip row = %q, want active chip ANSI tone", chipRow)
	}
	if !strings.Contains(chipRow, ChipInactiveStart) {
		t.Fatalf("chip row = %q, want inactive chip ANSI tone", chipRow)
	}
	// Active chip should occur before inactive chip in the strip.
	if strings.Index(chipRow, ChipActiveStart) > strings.LastIndex(chipRow, ChipInactiveStart) {
		t.Fatalf("chip row = %q, want active chip rendered before inactive chip", chipRow)
	}
	if got, want := VisibleLen(chipRow), 40; got != want {
		t.Fatalf("chip row width = %d, want %d: %q", got, want, chipRow)
	}
	if !strings.HasPrefix(lines[2], "├") || !strings.HasSuffix(lines[2], "┤") {
		t.Fatalf("titlebar divider = %q, want full-width divider below chip row", lines[2])
	}
	if !strings.Contains(lines[3], "hello") {
		t.Fatalf("content row = %q, want body content below chip row", lines[3])
	}
}

func TestRendererRenderFrameWithChipsDisabledChipReadsDim(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	DefaultRenderer().RenderFrameWithChips(
		&out,
		"hello",
		[]Chip{
			{Label: "Global", Active: true},
			{Label: "Project", Disabled: true},
		},
		Layout{Rows: 5, Cols: 40},
	)

	lines := strings.Split(out.String(), "\r\n")
	chipRow := lines[1]
	if !strings.Contains(chipRow, ChipDisabledStart) {
		t.Fatalf("chip row = %q, want disabled chip ANSI tone", chipRow)
	}
	// Disabled chip still occupies the same geometry — both labels visible.
	if !strings.Contains(chipRow, " Project ") {
		t.Fatalf("chip row = %q, want Project chip label visible even when disabled", chipRow)
	}
}

func TestRendererRenderFrameWithEmptyChipsHasNoTitlebar(t *testing.T) {
	t.Parallel()

	var plain bytes.Buffer
	var chipped bytes.Buffer
	DefaultRenderer().RenderFrame(&plain, "hello", Layout{Rows: 4, Cols: 20})
	DefaultRenderer().RenderFrameWithChips(&chipped, "hello", nil, Layout{Rows: 4, Cols: 20})

	if got, want := chipped.String(), plain.String(); got != want {
		t.Fatalf("RenderFrameWithChips(nil) = %q, want default frame %q", got, want)
	}
}

func TestChipsHitRegionsReportsClickColumnRangesPerChip(t *testing.T) {
	t.Parallel()

	// Outer width 40 → innerWidth 38. Chip strip layout starts at outer
	// column 3 (border + leading cell), each chip body is " Label ", and
	// chips are separated by one gap cell. " Global " spans 8 cells, then
	// gap, then " Project " spans 9 cells.
	hits := ChipsHitRegions([]Chip{
		{Label: "Global", Active: true, ClickValue: "tab:global"},
		{Label: "Project", Disabled: true, ClickValue: "tab:project"},
	}, 38)

	want := []ChipHit{
		{Index: 0, Disabled: false, ColStart: 3, ColEnd: 10, Value: "tab:global"},
		{Index: 1, Disabled: true, ColStart: 12, ColEnd: 20, Value: "tab:project"},
	}
	if len(hits) != len(want) {
		t.Fatalf("ChipsHitRegions() length = %d, want %d (hits=%#v)", len(hits), len(want), hits)
	}
	for i := range want {
		if hits[i] != want[i] {
			t.Fatalf("ChipsHitRegions()[%d] = %#v, want %#v", i, hits[i], want[i])
		}
	}
}

func TestChipsHitRegionsSkipsBlankChips(t *testing.T) {
	t.Parallel()

	hits := ChipsHitRegions([]Chip{
		{Label: "", ClickValue: ""},
		{Label: "Project", ClickValue: "tab:project"},
	}, 38)
	if len(hits) != 1 {
		t.Fatalf("ChipsHitRegions() = %#v, want one hit skipping blank chip", hits)
	}
	if hits[0].ColStart != 3 {
		t.Fatalf("ChipsHitRegions()[0].ColStart = %d, want 3 (no leading gap before first visible chip)", hits[0].ColStart)
	}
}

func TestChipsHitRegionsReturnsNilWhenInnerWidthTooSmall(t *testing.T) {
	t.Parallel()

	hits := ChipsHitRegions([]Chip{{Label: "Global"}}, 3)
	if hits != nil {
		t.Fatalf("ChipsHitRegions(small width) = %#v, want nil", hits)
	}
}

func TestChipsTitlebarRowIsRowTwo(t *testing.T) {
	t.Parallel()

	if got, want := ChipsTitlebarRow(), 2; got != want {
		t.Fatalf("ChipsTitlebarRow() = %d, want %d (top border on row 1, chip strip on row 2)", got, want)
	}
}

func TestChipANSIGoldenMatchesTmuxWindowStatusPalette(t *testing.T) {
	t.Parallel()

	// Chips reuse the tmux window-status palette so the popup tab strip
	// stays visually congruent with the tmux status row. Pin the ANSI
	// escape sequences and palette tokens together so future changes to
	// either side surface as a single golden diff.
	if got, want := TmuxWindowInactiveBg, "colour235"; got != want {
		t.Fatalf("inactive bg palette = %q, want %q", got, want)
	}
	if got, want := TmuxWindowInactiveFg, "colour245"; got != want {
		t.Fatalf("inactive fg palette = %q, want %q", got, want)
	}
	if got, want := TmuxWindowActiveBg, "colour240"; got != want {
		t.Fatalf("active bg palette = %q, want %q", got, want)
	}
	if got, want := TmuxWindowActiveFg, "colour231"; got != want {
		t.Fatalf("active fg palette = %q, want %q", got, want)
	}
	if got, want := ChipInactiveStart, "\x1b[48;5;235m\x1b[38;5;245m"; got != want {
		t.Fatalf("inactive chip ANSI = %q, want %q", got, want)
	}
	if got, want := ChipActiveStart, "\x1b[1m\x1b[48;5;240m\x1b[38;5;231m"; got != want {
		t.Fatalf("active chip ANSI = %q, want %q", got, want)
	}
	if got, want := ChipDisabledStart, "\x1b[2m\x1b[48;5;235m\x1b[38;5;245m"; got != want {
		t.Fatalf("disabled chip ANSI = %q, want %q", got, want)
	}
}
