package render

// Tmux status-line text measurement helpers shared by the usage HUD and the
// notify segment. Both render `#[...]`-styled segments into a fixed cell
// budget, so the escape stripper and the visual-length counter live here as
// the neutral home for every command-layer consumer.

import "strings"

// VisualLen returns the rune count of s after stripping tmux `#[...]`
// escape sequences. The HUD format strings only contain single-cell runes
// (ASCII + `█` + `░` + `·`), so rune count matches display width 1:1.
func VisualLen(s string) int {
	return len([]rune(StripTmuxEscapes(s)))
}

// StripTmuxEscapes removes `#[...]` escape sequences from s. A literal `#`
// followed by a non-`[` is preserved untouched so user content with `#` is
// not mangled.
func StripTmuxEscapes(s string) string {
	if !strings.Contains(s, "#[") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '#' && i+1 < len(s) && s[i+1] == '[' {
			end := strings.IndexByte(s[i+2:], ']')
			if end < 0 {
				// Unterminated escape — emit verbatim and stop scanning.
				b.WriteString(s[i:])
				break
			}
			i += 2 + end + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
