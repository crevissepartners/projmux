package app

import (
	"strings"
)

// Codex records hook trust in the same `~/.codex/config.toml` that projmux
// converges: a `[hooks.state]` subtable per reviewed hook holding the
// `trusted_hash` the user approved. Trust is the user's decision and Codex's
// hash, so projmux owns only the hook definitions it declares and must hand
// every other table back byte-for-byte — including state Codex re-serialized
// into the middle of the projmux-managed marker block.
//
// This file holds the ownership split: a minimal table-header scanner plus the
// exact shape of a projmux-authored Codex hook pair. Nothing here computes,
// copies, or removes a `trusted_hash`.

// codexTomlSection is one top-level TOML table and its verbatim body. The first
// section of a document carries an empty key and holds the root prologue.
type codexTomlSection struct {
	header string
	key    string
	array  bool
	body   string
}

func (s codexTomlSection) raw() string { return s.header + s.body }

// splitCodexTomlSections slices content on table headers while preserving every
// original byte, so unrelated tables can be reassembled exactly as written.
func splitCodexTomlSections(content string) []codexTomlSection {
	sections := []codexTomlSection{{}}
	for _, line := range strings.SplitAfter(content, "\n") {
		if line == "" {
			continue
		}
		if key, array, ok := codexTomlTableHeader(line); ok {
			sections = append(sections, codexTomlSection{header: line, key: key, array: array})
			continue
		}
		last := &sections[len(sections)-1]
		last.body += line
	}
	return sections
}

func joinCodexTomlSections(sections []codexTomlSection, keep func(int) bool) string {
	var out strings.Builder
	for i, section := range sections {
		if keep != nil && !keep(i) {
			continue
		}
		out.WriteString(section.raw())
	}
	return out.String()
}

func codexTomlTableHeader(line string) (string, bool, bool) {
	trimmed := strings.TrimSpace(stripCodexTomlComment(line))
	if !strings.HasPrefix(trimmed, "[") || !strings.HasSuffix(trimmed, "]") || len(trimmed) < 3 {
		return "", false, false
	}
	array := strings.HasPrefix(trimmed, "[[") && strings.HasSuffix(trimmed, "]]")
	key := trimmed[1 : len(trimmed)-1]
	if array {
		if len(trimmed) < 5 {
			return "", false, false
		}
		key = trimmed[2 : len(trimmed)-2]
	}
	key = strings.TrimSpace(key)
	if !codexTomlKeyIsWellFormed(key) {
		return "", false, false
	}
	return key, array, true
}

// codexTomlKeyIsWellFormed keeps a multi-line array element such as `[3, 4]`
// from being mistaken for a table header.
func codexTomlKeyIsWellFormed(key string) bool {
	if key == "" {
		return false
	}
	for _, part := range splitCodexTomlDottedKey(key) {
		if part == "" {
			return false
		}
		if strings.HasPrefix(part, `"`) || strings.HasPrefix(part, "'") {
			if _, ok := codexTomlUnquote(part); !ok {
				return false
			}
			continue
		}
		for _, r := range part {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			default:
				return false
			}
		}
	}
	return true
}

func splitCodexTomlDottedKey(key string) []string {
	parts := []string{}
	quote := rune(0)
	current := strings.Builder{}
	for _, r := range key {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
			current.WriteRune(r)
		case r == '"' || r == '\'':
			quote = r
			current.WriteRune(r)
		case r == '.':
			parts = append(parts, strings.TrimSpace(current.String()))
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	parts = append(parts, strings.TrimSpace(current.String()))
	return parts
}

func codexTomlUnquote(raw string) (string, bool) {
	if len(raw) < 2 {
		return "", false
	}
	switch raw[0] {
	case '\'':
		if raw[len(raw)-1] != '\'' {
			return "", false
		}
		return raw[1 : len(raw)-1], true
	case '"':
		if raw[len(raw)-1] != '"' {
			return "", false
		}
		inner := raw[1 : len(raw)-1]
		if strings.Contains(inner, `"`) || strings.Contains(inner, `\`) {
			return "", false
		}
		return inner, true
	}
	return "", false
}

// codexSectionEntries returns the raw `key = value` pairs of a table body. It
// reports false for any body holding a comment, a nested structure, or a line
// it does not fully understand, so an unrecognized body is never treated as
// projmux-owned.
func codexSectionEntries(body string) (map[string]string, bool) {
	entries := map[string]string{}
	for line := range strings.SplitSeq(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			return nil, false
		}
		key, value, ok := strings.Cut(strings.TrimSpace(stripCodexTomlComment(line)), "=")
		if !ok {
			return nil, false
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, false
		}
		if _, exists := entries[key]; exists {
			return nil, false
		}
		entries[key] = strings.TrimSpace(value)
	}
	return entries, true
}

func codexSectionHasExactEntries(body string, want map[string]string) bool {
	entries, ok := codexSectionEntries(body)
	if !ok || len(entries) != len(want) {
		return false
	}
	for key, value := range want {
		if entries[key] != value {
			return false
		}
	}
	return true
}

// codexManagedHookPairEvent reports the event name when sections[i] and the
// section that follows it are exactly the `[[hooks.<Event>]]` +
// `[[hooks.<Event>.hooks]]` pair projmux writes. Any extra key, a different
// matcher, or a command projmux did not author fails the match, which is what
// keeps a hand-edited hook out of projmux ownership.
func codexManagedHookPairEvent(sections []codexTomlSection, i int) (string, bool) {
	if i < 0 || i+1 >= len(sections) {
		return "", false
	}
	head, nested := sections[i], sections[i+1]
	event, ok := codexHookEventSection(head)
	if !ok || !nested.array || nested.key != "hooks."+event+".hooks" {
		return "", false
	}
	if !codexSectionHasExactEntries(head.body, map[string]string{"matcher": `"*"`}) {
		return "", false
	}
	entries, ok := codexSectionEntries(nested.body)
	if !ok || len(entries) != 2 || entries["type"] != `"command"` {
		return "", false
	}
	command, ok := codexTomlUnquote(entries["command"])
	if !ok {
		return "", false
	}
	if !codexProjmuxHookCommand(command) {
		return "", false
	}
	return event, true
}

// codexProjmuxHookCommand reports whether an installed hook command is one
// projmux authored. Every spelling projmux has ever written stays recognized so
// that an older config converges forward instead of being read as hand-edited
// wiring that the integration must refuse to touch.
func codexProjmuxHookCommand(command string) bool {
	return command == codexHookCommand || command == priorCodexHookCommand || command == legacyCodexHookCommand
}

// codexManagedFlatHookSection reports the event of the pre-0.11 single-table
// hook projmux used to write, so an old managed block still converges.
func codexManagedFlatHookSection(section codexTomlSection) (string, bool) {
	event, ok := codexHookEventSection(section)
	if !ok {
		return "", false
	}
	entries, ok := codexSectionEntries(section.body)
	if !ok || len(entries) != 1 {
		return "", false
	}
	command, ok := codexTomlUnquote(entries["command"])
	if !ok || !codexProjmuxHookCommand(command) {
		return "", false
	}
	return event, true
}

// codexHookEventSection reports the event of a `[[hooks.<Event>]]` header.
func codexHookEventSection(section codexTomlSection) (string, bool) {
	if !section.array {
		return "", false
	}
	event, ok := strings.CutPrefix(section.key, "hooks.")
	if !ok || event == "" || strings.Contains(event, ".") || event == "state" {
		return "", false
	}
	return event, true
}

func codexIsProjmuxFeatureSection(section codexTomlSection) bool {
	return !section.array && section.key == "features" &&
		codexSectionHasExactEntries(section.body, map[string]string{"hooks": "true"})
}

// stripProjmuxManagedCodexHookSections drops the hook definitions projmux
// authored and returns every other table verbatim, together with how many pairs
// were dropped. Passing insideBlock also drops the `[features] hooks = true`
// table projmux emits inside its marker block.
func stripProjmuxManagedCodexHookSections(content string, insideBlock bool) (string, int) {
	sections := splitCodexTomlSections(content)
	drop := make(map[int]bool, len(sections))
	removed := 0
	for i := 0; i < len(sections); i++ {
		if _, ok := codexManagedHookPairEvent(sections, i); ok {
			drop[i] = true
			drop[i+1] = true
			removed++
			i++
			continue
		}
		if _, ok := codexManagedFlatHookSection(sections[i]); ok {
			drop[i] = true
			removed++
			continue
		}
		if insideBlock && codexIsProjmuxFeatureSection(sections[i]) {
			drop[i] = true
		}
	}
	return joinCodexTomlSections(sections, func(i int) bool { return !drop[i] }), removed
}

// stripUnmarkedProjmuxCodexHooks recovers hook wiring projmux authored that no
// longer sits inside its marker block, which is what a Codex TOML
// re-serialization leaves behind once it drops the marker comments.
//
// It returns the input unchanged when removing the projmux pairs would renumber
// a surviving hook entry of the same event, because Codex keys trust state by
// array position and a renumber would silently expire a hook projmux does not
// own. That case keeps the pre-existing unmanaged-hooks refusal.
func stripUnmarkedProjmuxCodexHooks(content string) (string, int) {
	sections := splitCodexTomlSections(content)
	drop := make(map[int]bool, len(sections))
	removed := 0
	for i := 0; i < len(sections); i++ {
		if _, ok := codexManagedHookPairEvent(sections, i); ok {
			drop[i] = true
			drop[i+1] = true
			removed++
			i++
			continue
		}
		if _, ok := codexManagedFlatHookSection(sections[i]); ok {
			drop[i] = true
			removed++
		}
	}
	if removed == 0 {
		return content, 0
	}
	dropped := map[string]bool{}
	for i, section := range sections {
		event, ok := codexHookEventSection(section)
		if !ok {
			continue
		}
		if drop[i] {
			dropped[event] = true
			continue
		}
		if dropped[event] {
			return content, 0
		}
	}
	return joinCodexTomlSections(sections, func(i int) bool { return !drop[i] }), removed
}
