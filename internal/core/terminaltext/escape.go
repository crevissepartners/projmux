// Package terminaltext makes untrusted text safe to render in a terminal.
package terminaltext

import (
	"fmt"
	"strings"
	"unicode"
)

// EscapeControls preserves ordinary Unicode while rendering control and
// formatting runes visibly. In particular, ESC/C1 CSI/C1 OSC cannot reach a
// terminal as active control sequences.
func EscapeControls(value string) string {
	var out strings.Builder
	for _, r := range value {
		switch r {
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		default:
			switch {
			case r == 0x1b:
				out.WriteString(`\x1b`)
			case r <= 0xff && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r)):
				fmt.Fprintf(&out, `\x%02x`, r)
			case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
				fmt.Fprintf(&out, `\u%04x`, r)
			default:
				out.WriteRune(r)
			}
		}
	}
	return out.String()
}
