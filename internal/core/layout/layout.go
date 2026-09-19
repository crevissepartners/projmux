// Package layout holds the line-level comment stripping shared by projmux's
// hand-parsed TOML-style config readers (keymap and project config).
package layout

// StripComment drops a trailing `#` comment from one config line, ignoring
// `#` inside a double-quoted string.
func StripComment(line string) string {
	inString := false
	escaped := false
	for i, r := range line {
		if escaped {
			escaped = false
			continue
		}
		if inString && r == '\\' {
			escaped = true
			continue
		}
		if r == '"' {
			inString = !inString
			continue
		}
		if !inString && r == '#' {
			return line[:i]
		}
	}
	return line
}
