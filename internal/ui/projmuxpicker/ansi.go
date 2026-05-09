package projmuxpicker

import (
	"strings"
	"unicode/utf8"
)

// ANSI/style tokens used by the projmux native picker surface.
const (
	CurrentStart   = "\x1b[48;2;38;50;56m\x1b[38;2;255;255;255m"
	HighlightStart = "\x1b[38;2;255;204;102m"
	Pointer        = "\x1b[38;2;225;38;114m▌\x1b[0m "
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
		out.WriteRune(r)
		visible++
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
		_, size := utf8.DecodeRuneInString(value[i:])
		if size <= 0 {
			break
		}
		length++
		i += size
	}
	return length
}
