package projmuxpicker

import (
	"bytes"
	"strings"
	"testing"
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

func TestRendererRenderFrameWithTitleUsesTopBorderTitle(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	DefaultRenderer().RenderFrameWithTitle(&out, "hello", "Projects", Layout{Rows: 4, Cols: 24})

	lines := strings.Split(out.String(), "\r\n")
	if !strings.Contains(lines[0], " Projects ") {
		t.Fatalf("top frame row = %q, want title in top border", lines[0])
	}
	if got, want := VisibleLen(lines[0]), 24; got != want {
		t.Fatalf("top frame width = %d, want %d: %q", got, want, lines[0])
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
	if got, want := VisibleLen("api🔔"), 5; got != want {
		t.Fatalf("VisibleLen(emoji) = %d, want terminal cell width %d", got, want)
	}
	if got, want := VisibleLen("e\u0301"), 1; got != want {
		t.Fatalf("VisibleLen(combining) = %d, want terminal cell width %d", got, want)
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
