package platformkeys

import (
	"fmt"
	"strconv"
	"strings"
)

// Modifiers is the platform-neutral modifier mask used by the native adapter.
// Alt intentionally corresponds to tmux's M- modifier.
type Modifiers uint16

const (
	ModifierAlt Modifiers = 1 << iota
	ModifierControl
	ModifierShift
	// ModifierCommand is reserved so a physical Command+Option chord cannot
	// accidentally match a portable Option-only binding.
	ModifierCommand
)

// Binding maps one physical key chord to the canonical tmux chord already
// present in projmux's generated configuration.
type Binding struct {
	Chord     string
	KeyCode   uint16
	Modifiers Modifiers
}

// ParseBinding converts the portable tmux chord vocabulary used by keymap.toml
// into a physical-key binding. It returns false for chords that should continue
// through the terminal's ordinary input path.
func ParseBinding(chord string) (Binding, bool) {
	chord = strings.TrimSpace(chord)
	if chord == "" {
		return Binding{}, false
	}

	parts := strings.Split(chord, "-")
	if len(parts) < 2 {
		return Binding{}, false
	}

	var modifiers Modifiers
	keyIndex := 0
	for keyIndex < len(parts)-1 {
		switch strings.ToUpper(parts[keyIndex]) {
		case "M":
			modifiers |= ModifierAlt
		case "C":
			modifiers |= ModifierControl
		case "S":
			modifiers |= ModifierShift
		default:
			return Binding{}, false
		}
		keyIndex++
	}
	if modifiers&(ModifierAlt|ModifierControl) == 0 {
		return Binding{}, false
	}

	key := strings.Join(parts[keyIndex:], "-")
	if len(key) == 1 && key[0] >= 'A' && key[0] <= 'Z' {
		modifiers |= ModifierShift
		key = strings.ToLower(key)
	}
	keyCode, ok := darwinVirtualKeyCode(key)
	if !ok {
		return Binding{}, false
	}
	return Binding{Chord: chord, KeyCode: keyCode, Modifiers: modifiers}, true
}

// ParseBindings filters unsupported chords while preserving catalog order.
func ParseBindings(chords []string) []Binding {
	seen := map[string]bool{}
	out := make([]Binding, 0, len(chords))
	for _, chord := range chords {
		binding, ok := ParseBinding(chord)
		if !ok {
			continue
		}
		signature := fmt.Sprintf("%d:%d", binding.KeyCode, binding.Modifiers)
		if seen[signature] {
			continue
		}
		seen[signature] = true
		out = append(out, binding)
	}
	return out
}

// darwinVirtualKeyCode uses the layout-independent hardware positions exposed
// by CGEvent. Letters and top-row digits therefore remain physical keys even
// while a non-Latin input source is active.
func darwinVirtualKeyCode(key string) (uint16, bool) {
	if len(key) == 1 {
		if code, ok := darwinANSIKeyCodes[strings.ToLower(key)]; ok {
			return code, true
		}
	}
	lower := strings.ToLower(key)
	if after, ok := strings.CutPrefix(lower, "f"); ok {
		if number, err := strconv.Atoi(after); err == nil {
			if code, ok := darwinFunctionKeyCodes[number]; ok {
				return code, true
			}
		}
	}
	code, ok := darwinNamedKeyCodes[lower]
	return code, ok
}

var darwinANSIKeyCodes = map[string]uint16{
	"a": 0, "s": 1, "d": 2, "f": 3, "h": 4, "g": 5,
	"z": 6, "x": 7, "c": 8, "v": 9, "b": 11, "q": 12,
	"w": 13, "e": 14, "r": 15, "y": 16, "t": 17,
	"1": 18, "2": 19, "3": 20, "4": 21, "6": 22, "5": 23,
	"=": 24, "9": 25, "7": 26, "-": 27, "8": 28, "0": 29,
	"]": 30, "o": 31, "u": 32, "[": 33, "i": 34, "p": 35,
	"l": 37, "j": 38, "'": 39, "k": 40, ";": 41, "\\": 42,
	",": 43, "/": 44, "n": 45, "m": 46, ".": 47, "`": 50,
}

var darwinNamedKeyCodes = map[string]uint16{
	"return":    36,
	"enter":     36,
	"tab":       48,
	"space":     49,
	"backspace": 51,
	"bs":        51,
	"escape":    53,
	"esc":       53,
	"home":      115,
	"pageup":    116,
	"pgup":      116,
	"delete":    117,
	"end":       119,
	"pagedown":  121,
	"pgdn":      121,
	"left":      123,
	"right":     124,
	"down":      125,
	"up":        126,
}

var darwinFunctionKeyCodes = map[int]uint16{
	1: 122, 2: 120, 3: 99, 4: 118, 5: 96, 6: 97,
	7: 98, 8: 100, 9: 101, 10: 109, 11: 103, 12: 111,
	13: 105, 14: 107, 15: 113, 16: 106, 17: 64, 18: 79,
	19: 80, 20: 90,
}
