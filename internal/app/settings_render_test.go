package app

import (
	"strings"
	"testing"
)

func TestSettingsLabelPadsNameAndAppliesColor(t *testing.T) {
	t.Parallel()

	label := settingsLabel(settingsGlyphAdd, settingsColorAdd, "Add Project...", "scan filesystem roots")

	if !strings.HasPrefix(label, settingsGlyphAdd+"  ") {
		t.Fatalf("label = %q, want glyph+two spaces prefix", label)
	}
	// Name column is colored and right-padded with spaces; the reset comes
	// after the padding so width is consistent regardless of name length.
	wantNameRun := settingsColorAdd + "Add Project..." + strings.Repeat(" ", settingsLabelNameWidth-len("Add Project...")) + settingsColorReset
	if !strings.Contains(label, wantNameRun) {
		t.Fatalf("label = %q, want padded colored name run %q", label, wantNameRun)
	}
	wantDescRun := settingsColorDim + "scan filesystem roots" + settingsColorReset
	if !strings.Contains(label, wantDescRun) {
		t.Fatalf("label = %q, want dim description run %q", label, wantDescRun)
	}
}

func TestSettingsLabelEmptyGlyphAlignsWithSingleCellGlyphRows(t *testing.T) {
	t.Parallel()

	// Among rows with single-cell glyphs (and rows with no glyph), the name
	// column starts at the same display column: glyph(1 rune) + 2 spaces, or
	// 2 spaces + 2 spaces. Compare by rune count after stripping ANSI to
	// avoid being fooled by multi-byte encodings of the glyph.
	withGlyph := settingsLabel(settingsGlyphRemove, settingsColorRemove, "Name", "")
	withoutGlyph := settingsLabel("", "", "Name", "")

	if got, want := visibleRuneColumn(withoutGlyph, "Name"), visibleRuneColumn(withGlyph, "Name"); got != want {
		t.Fatalf("name rune offset without glyph = %d, with single-cell glyph = %d", got, want)
	}
}

func TestSettingsLabelDimWrapsRowInDimColor(t *testing.T) {
	t.Parallel()

	label := settingsLabelDim("(no saved workdirs)", "")

	if !strings.Contains(label, settingsColorDim+"(no saved workdirs)") {
		t.Fatalf("label = %q, want dim-wrapped name", label)
	}
	if !strings.HasPrefix(label, settingsGlyphInfo+"  ") {
		t.Fatalf("label = %q, want info-glyph prefix", label)
	}
}

func TestSettingsLabelInfoEmitsValueAndSource(t *testing.T) {
	t.Parallel()

	label := settingsLabelInfo("Project Root", "/home/me/code", "PROJMUX_PROJDIR env")

	if !strings.Contains(label, "Project Root") {
		t.Fatalf("label = %q, want name", label)
	}
	if !strings.Contains(label, settingsColorActive+"/home/me/code"+settingsColorReset) {
		t.Fatalf("label = %q, want bold value run", label)
	}
	if !strings.Contains(label, "(PROJMUX_PROJDIR env)") {
		t.Fatalf("label = %q, want parenthesized source", label)
	}
}

func TestSettingsLabelInfoOmitsEmptyValueAndSource(t *testing.T) {
	t.Parallel()

	label := settingsLabelInfo("Standalone", "", "")
	if strings.Contains(label, "()") {
		t.Fatalf("label = %q, must not emit empty source parens", label)
	}
	if strings.Contains(label, settingsColorActive+settingsColorReset) {
		t.Fatalf("label = %q, must not emit empty bold value run", label)
	}
}

// visibleRuneColumn strips ANSI escape sequences from s and returns the rune
// offset at which marker first appears. Returns -1 if not present.
func visibleRuneColumn(s, marker string) int {
	stripped := stripANSI(s)
	before, _, ok := strings.Cut(stripped, marker)
	if !ok {
		return -1
	}
	return len([]rune(before))
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			// Skip until 'm' (the only terminator we use here).
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			i = j + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
