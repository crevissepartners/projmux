package projmuxpicker

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/crevissepartners/projmux/internal/theme"
)

// ANSI/style tokens used by the projmux native picker surface.
const (
	CurrentStart   = theme.ANSISurfaceActiveStart
	HighlightStart = theme.ANSIAccentHighlightStart
	MutedStart     = theme.ANSITextMutedStart
	TitlebarStart  = theme.ANSISurfaceRaisedStart
	TitlebarRule   = theme.ANSISurfaceRuleStart
	Pointer        = CurrentStart + theme.ANSIAccentActionStrongStart + "▌" + CurrentStart + " "
	Continuation   = CurrentStart + theme.ANSIAccentActionStrongStart + "▌" + CurrentStart + " "
	Reset          = theme.ANSIReset
	InverseStart   = theme.ANSIInverse
	CursorStart    = theme.ANSIInverse
	Scrollbar      = "█"
	GapLine        = "─"

	// Tmux window-status tone tokens. These are the canonical 256-color
	// palette entries used both by the tmux status bar (window list) and by
	// the projmux picker frame chip primitives so the two surfaces stay
	// visually congruent without re-declaring colour codes.
	TmuxWindowInactiveBg = theme.TmuxWindowInactiveBg
	TmuxWindowInactiveFg = theme.TmuxWindowInactiveFg
	TmuxWindowActiveBg   = theme.TmuxWindowActiveBg
	TmuxWindowActiveFg   = theme.TmuxWindowActiveFg

	// Chip ANSI segments (terminal SGR escapes). They mirror the tmux
	// window-status tone above. Disabled reuses inactive bg with a dimmer
	// foreground to read as "tab present but not actionable" rather than
	// introducing a third hue.
	ChipInactiveStart = theme.ANSIChipInactiveStart
	ChipActiveStart   = theme.ANSIChipActiveStart
	ChipDisabledStart = theme.ANSIChipDisabledStart
)

// ThemeFromEffective adapts resolver-backed colors to the native picker frame.
// Fallback-sourced fields deliberately leave DefaultTheme unchanged so existing
// default render output stays byte-identical.
func ThemeFromEffective(effective theme.EffectiveTheme) Theme {
	out := DefaultTheme
	if effective.Background.Source != theme.SourceFallback {
		if bg := effective.Background.Value.TruecolorBG(); bg != "" {
			out.Background = "\x1b[" + bg + "m"
		}
	}
	if effective.Foreground.Source != theme.SourceFallback {
		if fg := effective.Foreground.Value.TruecolorFG(); fg != "" {
			out.Foreground = "\x1b[" + fg + "m"
		}
	}
	return out
}

func PadRight(value string, width int) string {
	length := VisibleLen(value)
	if length >= width {
		return value
	}
	return value + strings.Repeat(" ", width-length)
}

func closeStyledLine(line string) string {
	if hasActiveStyle(line) {
		return line + Reset
	}
	return line
}

func hasActiveStyle(value string) bool {
	active := false
	for i := 0; i < len(value); {
		if value[i] != '\x1b' {
			i++
			continue
		}
		start := i
		i++
		for i < len(value) && value[i] != 'm' {
			i++
		}
		if i >= len(value) {
			break
		}
		sequence := value[start : i+1]
		i++
		if !strings.HasPrefix(sequence, "\x1b[") {
			continue
		}
		params := strings.TrimSuffix(strings.TrimPrefix(sequence, "\x1b["), "m")
		active = sgrLeavesStyleActive(params, active)
	}
	return active
}

func sgrLeavesStyleActive(params string, active bool) bool {
	if params == "" {
		return false
	}
	for field := range strings.SplitSeq(params, ";") {
		code, err := strconv.Atoi(field)
		if err != nil {
			continue
		}
		switch code {
		case 0:
			active = false
		case 22, 23, 24, 25, 27, 28, 29, 39, 49, 59:
			// These only clear one SGR attribute. Other attributes may still be active.
		default:
			active = true
		}
	}
	return active
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
