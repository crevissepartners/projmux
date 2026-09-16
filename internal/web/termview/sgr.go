package termview

import (
	"fmt"
	"strconv"
	"strings"
)

// atoiOr parses a tmux format field, which is empty when tmux has no value.
func atoiOr(text string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		return fallback
	}
	return value
}

// parseSGR splits one captured line into attribute runs.
//
// Only SGR (`ESC [ … m`) is interpreted. capture-pane emits nothing else for a
// static grid, and anything unrecognized is dropped rather than printed, so a
// stray sequence cannot leak control characters into the page.
func parseSGR(line string) []Run {
	var (
		runs    []Run
		current = Run{}
		text    strings.Builder
	)
	flush := func() {
		if text.Len() == 0 {
			return
		}
		run := current
		run.Text = text.String()
		runs = append(runs, run)
		text.Reset()
	}

	for i := 0; i < len(line); {
		if line[i] != 0x1b {
			// Drop other control characters; they are not content.
			if line[i] < 0x20 && line[i] != '\t' {
				i++
				continue
			}
			size := runeSize(line[i:])
			text.WriteString(line[i : i+size])
			i += size
			continue
		}
		end, params, isSGR := readEscape(line[i:])
		if end == 0 {
			i++ // a lone ESC
			continue
		}
		if isSGR {
			flush()
			current = applySGR(current, params)
		}
		i += end
	}
	flush()
	if runs == nil {
		runs = []Run{}
	}
	return runs
}

// runeSize returns the byte length of the UTF-8 sequence at the front of s.
func runeSize(s string) int {
	switch b := s[0]; {
	case b < 0x80:
		return 1
	case b&0xe0 == 0xc0 && len(s) >= 2:
		return 2
	case b&0xf0 == 0xe0 && len(s) >= 3:
		return 3
	case b&0xf8 == 0xf0 && len(s) >= 4:
		return 4
	}
	return 1
}

// readEscape measures an escape sequence starting at s[0] == ESC and reports
// its parameters when it is an SGR.
//
// A CSI is measured by its ECMA-48 shape — parameter bytes 0x30-0x3F, then
// intermediate bytes 0x20-0x2F, then one final byte 0x40-0x7E — so a private
// sequence such as `ESC [ ? 25 h` is consumed whole instead of leaving `25h`
// behind as text. Only a final `m` with plain numeric parameters is an SGR.
// A sequence cut off by the end of the line has length 0: the caller skips the
// ESC alone.
func readEscape(s string) (length int, params []int, isSGR bool) {
	if len(s) < 2 {
		return 0, nil, false
	}
	switch {
	case s[1] == ']':
		return oscLength(s), nil, false
	case s[1] >= 0x20 && s[1] <= 0x2f:
		// nF escape (`ESC ( B` selects a character set): intermediates, then
		// one final byte. Consuming only two bytes would print the final.
		i := 1
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
			i++
		}
		if i >= len(s) || s[i] < 0x30 || s[i] > 0x7e {
			return 0, nil, false
		}
		return i + 1, nil, false
	case s[1] != '[':
		// Any other two-byte escape. Consume it so it does not print.
		return 2, nil, false
	}
	i := 2
	start := i
	for i < len(s) && s[i] >= 0x30 && s[i] <= 0x3f {
		i++
	}
	body := s[start:i]
	for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
		i++
	}
	intermediates := i > start+len(body)
	if i >= len(s) || s[i] < 0x40 || s[i] > 0x7e {
		return 0, nil, false
	}
	final := s[i]
	i++
	if final != 'm' || intermediates || strings.IndexFunc(body, notSGRParam) >= 0 {
		return i, nil, false
	}
	if body == "" {
		return i, []int{0}, true
	}
	for _, field := range strings.FieldsFunc(body, func(r rune) bool { return r == ';' || r == ':' }) {
		params = append(params, atoiOr(field, 0))
	}
	return i, params, true
}

// oscLength measures an operating system command (`ESC ] … BEL` or
// `ESC ] … ESC \`). capture-pane -e writes hyperlinks this way, and their
// URL must not become text. An unterminated one runs to the end of the line:
// what follows the opener is the command's own payload, not content.
func oscLength(s string) int {
	for i := 2; i < len(s); i++ {
		switch {
		case s[i] == 0x07:
			return i + 1
		case s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\':
			return i + 2
		}
	}
	return len(s)
}

// notSGRParam reports a parameter byte an SGR does not use; `ESC [ > 4 m`
// and friends are private modes, not attributes.
func notSGRParam(r rune) bool {
	return r != ';' && r != ':' && (r < '0' || r > '9')
}

func applySGR(run Run, params []int) Run {
	for i := 0; i < len(params); i++ {
		switch code := params[i]; {
		case code == 0:
			run = Run{}
		case code == 1:
			run.Bold = true
		case code == 2:
			run.Dim = true
		case code == 3:
			run.Italic = true
		case code == 4:
			run.Underline = true
		case code == 7:
			run.Reverse = true
		case code == 22:
			run.Bold, run.Dim = false, false
		case code == 23:
			run.Italic = false
		case code == 24:
			run.Underline = false
		case code == 27:
			run.Reverse = false
		case code >= 30 && code <= 37:
			run.FG = ansi16[code-30]
		case code == 39:
			run.FG = ""
		case code >= 40 && code <= 47:
			run.BG = ansi16[code-40]
		case code == 49:
			run.BG = ""
		case code >= 90 && code <= 97:
			run.FG = ansi16[code-90+8]
		case code >= 100 && code <= 107:
			run.BG = ansi16[code-100+8]
		case code == 38 || code == 48:
			color, used := extendedColor(params[i:])
			if used == 0 {
				return run // malformed; stop rather than misread the rest
			}
			if code == 38 {
				run.FG = color
			} else {
				run.BG = color
			}
			i += used - 1
		}
	}
	return run
}

// extendedColor reads a 38/48 sub-sequence and returns the CSS color plus how
// many parameters it consumed.
func extendedColor(params []int) (string, int) {
	if len(params) < 2 {
		return "", 0
	}
	switch params[1] {
	case 5:
		if len(params) < 3 {
			return "", 0
		}
		return xterm256(params[2]), 3
	case 2:
		if len(params) < 5 {
			return "", 0
		}
		return fmt.Sprintf("rgb(%d,%d,%d)", clamp8(params[2]), clamp8(params[3]), clamp8(params[4])), 5
	}
	return "", 0
}

func clamp8(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// ansi16 is the 16-colour base a captured pane is painted with.
//
// The projmux preset defines eleven colours and five of them map straight onto
// ANSI slots: status background, critical, success, warning and text primary.
// It has no token for blue, magenta or cyan, so those three are built on the
// preset's own value ladder — its hexes are made of 0x5f/0x7a/0x87/0xaf/0xc7/
// 0xff components — which keeps them at the same saturation and luma as the
// five that are real. Inventing a hue is unavoidable here; inventing one off
// the ladder is not.
//
// The bright half is each base mixed 30% toward white, except bright black,
// which is the preset's own muted grey.
var ansi16 = [16]string{
	"#182226", // black   — TokenStatusBackground
	"#ff6b6b", // red     — TokenCritical
	"#5faf87", // green   — TokenSuccess
	"#ffcc66", // yellow  — TokenWarning
	"#5f87c7", // blue    — ladder
	"#c787af", // magenta — ladder
	"#5fc7c7", // cyan    — ladder
	"#d8e0e4", // white   — TokenTextPrimary

	"#75848c", // bright black — TokenMuted
	"#ff9797",
	"#8fc7ab",
	"#ffdb94",
	"#8fabd8",
	"#d8abc7",
	"#8fd8d8",
	"#e4e9ec",
}

// xterm256 resolves one 256-colour index to CSS.
func xterm256(index int) string {
	switch {
	case index < 0 || index > 255:
		return ""
	case index < 16:
		return ansi16[index]
	case index < 232:
		n := index - 16
		level := func(v int) int {
			if v == 0 {
				return 0
			}
			return 55 + v*40
		}
		return fmt.Sprintf("rgb(%d,%d,%d)", level(n/36), level((n/6)%6), level(n%6))
	default:
		gray := 8 + (index-232)*10
		return fmt.Sprintf("rgb(%d,%d,%d)", gray, gray, gray)
	}
}
