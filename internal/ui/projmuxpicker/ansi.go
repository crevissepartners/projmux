package projmuxpicker

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ANSI/style tokens used by the projmux native picker surface.
const (
	CurrentStart   = "\x1b[48;2;38;50;56m\x1b[38;2;255;255;255m"
	HighlightStart = "\x1b[38;2;255;204;102m"
	Pointer        = CurrentStart + "\x1b[38;2;225;38;114m▌" + CurrentStart + " "
	Continuation   = CurrentStart + "\x1b[38;2;225;38;114m┃" + CurrentStart + " "
	Reset          = "\x1b[0m"
	InverseStart   = "\x1b[7m"
	CursorStart    = "\x1b[7m"
	Scrollbar      = "█"
	GapLine        = "─"
)

func PadRight(value string, width int) string {
	length := VisibleLen(value)
	if length >= width {
		return value
	}
	return value + strings.Repeat(" ", width-length)
}

func TruncateANSI(value string, width int) string {
	if width <= 0 || VisibleLen(value) <= width {
		return value
	}
	var out strings.Builder
	visible := 0
	sawANSI := false
	for i := 0; i < len(value) && visible < width; {
		if value[i] == '\x1b' {
			end := i + 1
			for end < len(value) && value[end] != 'm' {
				end++
			}
			if end < len(value) {
				out.WriteString(value[i : end+1])
				sawANSI = true
				i = end + 1
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(value[i:])
		if r == utf8.RuneError && size == 0 {
			break
		}
		runeWidth := RuneWidth(r)
		if visible+runeWidth > width {
			break
		}
		out.WriteRune(r)
		visible += runeWidth
		i += size
	}
	if sawANSI && !strings.HasSuffix(out.String(), Reset) {
		out.WriteString(Reset)
	}
	return out.String()
}

func VisibleLen(value string) int {
	length := 0
	for i := 0; i < len(value); {
		if value[i] == '\x1b' {
			i++
			for i < len(value) && value[i] != 'm' {
				i++
			}
			if i < len(value) {
				i++
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(value[i:])
		if size <= 0 {
			break
		}
		length += RuneWidth(r)
		i += size
	}
	return length
}

func RuneWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case r < 0x20 || (r >= 0x7f && r < 0xa0):
		return 0
	case isCombiningRune(r):
		return 0
	case isWideRune(r):
		return 2
	default:
		return 1
	}
}

func isCombiningRune(r rune) bool {
	return unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r)
}

func isWideRune(r rune) bool {
	return r >= 0x1100 && (r <= 0x115f ||
		r == 0x2329 || r == 0x232a ||
		(r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) ||
		(r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) ||
		(r >= 0xfe10 && r <= 0xfe19) ||
		(r >= 0xfe30 && r <= 0xfe6f) ||
		(r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) ||
		(r >= 0x1f300 && r <= 0x1faff) ||
		(r >= 0x20000 && r <= 0x3fffd))
}
