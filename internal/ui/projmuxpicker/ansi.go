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
	Pointer        = CurrentStart + themeAccentStart + "▌" + CurrentStart + " "
	Continuation   = CurrentStart + themeAccentStart + "▌" + CurrentStart + " "
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

const (
	themeAccentStart   = theme.ANSIAccentActionStrongStart
	themeWarningStart  = theme.ANSIStateProgressStart
	themeCriticalStart = theme.ANSIStateDangerStart
)

// ThemeFromEffective adapts resolver-backed colors to the native picker frame.
// Even fallback-sourced fields are concrete app theme tokens, so native popup
// rows paint the picker background instead of inheriting the terminal default.
func ThemeFromEffective(effective theme.EffectiveTheme) Theme {
	out := DefaultTheme
	if bg := effective.Background.Value.TruecolorBG(); bg != "" {
		out.Background = "\x1b[" + bg + "m"
	}
	if fg := effective.ChromeForeground.Value.TruecolorFG(); fg != "" {
		out.Foreground = "\x1b[" + fg + "m"
	}
	if effective.SurfaceActive.Source != theme.SourceFallback || effective.ChromeForeground.Source != theme.SourceFallback {
		out.Selected = ansiBG(effective.SurfaceActive) + ansiFG(effective.ChromeForeground)
	}
	if effective.Muted.Source != theme.SourceFallback {
		out.Muted = ansiFG(effective.Muted)
	}
	if effective.Accent.Source != theme.SourceFallback {
		out.Accent = ansiFG(effective.Accent)
		out.Highlight = out.Accent
	}
	if effective.Warning.Source != theme.SourceFallback {
		out.Warning = ansiFG(effective.Warning)
	}
	if effective.Critical.Source != theme.SourceFallback {
		out.Critical = ansiFG(effective.Critical)
	}
	return out
}

func ansiFG(field theme.ColorField) string {
	if fg := field.Value.TruecolorFG(); fg != "" {
		return "\x1b[" + fg + "m"
	}
	return ""
}

func ansiBG(field theme.ColorField) string {
	if bg := field.Value.TruecolorBG(); bg != "" {
		return "\x1b[" + bg + "m"
	}
	return ""
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
	return unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r)
}

func isWideRune(r rune) bool {
	return r >= 0x1100 && (r <= 0x115f ||
		(r >= 0x231a && r <= 0x231b) ||
		r == 0x2329 || r == 0x232a ||
		(r >= 0x23e9 && r <= 0x23ec) ||
		r == 0x23f0 ||
		r == 0x23f3 ||
		(r >= 0x25fd && r <= 0x25fe) ||
		(r >= 0x2614 && r <= 0x2615) ||
		(r >= 0x2648 && r <= 0x2653) ||
		r == 0x267f ||
		r == 0x2693 ||
		r == 0x26a1 ||
		(r >= 0x26aa && r <= 0x26ab) ||
		(r >= 0x26bd && r <= 0x26be) ||
		(r >= 0x26c4 && r <= 0x26c5) ||
		r == 0x26ce ||
		r == 0x26d4 ||
		r == 0x26ea ||
		(r >= 0x26f2 && r <= 0x26f3) ||
		r == 0x26f5 ||
		r == 0x26fa ||
		r == 0x26fd ||
		r == 0x2705 ||
		(r >= 0x270a && r <= 0x270b) ||
		r == 0x2728 ||
		r == 0x274c ||
		r == 0x274e ||
		(r >= 0x2753 && r <= 0x2755) ||
		r == 0x2757 ||
		(r >= 0x2795 && r <= 0x2797) ||
		r == 0x27b0 ||
		r == 0x27bf ||
		(r >= 0x2b1b && r <= 0x2b1c) ||
		r == 0x2b50 ||
		r == 0x2b55 ||
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
