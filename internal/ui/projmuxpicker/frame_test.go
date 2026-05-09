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

func TestRendererContentLayoutUsesFrameInnerWidth(t *testing.T) {
	t.Parallel()

	layout := DefaultRenderer().ContentLayout(Layout{Rows: 10, Cols: 40})
	if got, want := layout.Cols, 38; got != want {
		t.Fatalf("ContentLayout().Cols = %d, want frame inner width %d", got, want)
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
