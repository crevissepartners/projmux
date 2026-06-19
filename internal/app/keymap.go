package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
)

type keymapOverride struct {
	Plain     *string
	Keys      []string
	KeysSet   bool
	Prefix    *string
	lineByKey map[string]int
}

type keymapFile struct {
	Bindings map[string]keymapOverride
}

type keymapLoader struct {
	homeDir   func() (string, error)
	lookupEnv func(string) string
	readFile  func(string) ([]byte, error)
}

type keymapStore struct {
	homeDir   func() (string, error)
	lookupEnv func(string) string
	readFile  func(string) ([]byte, error)
	writeFile func(string, []byte, os.FileMode) error
}

func loadMergedKeyBindingCatalog(loader keymapLoader) ([]keyBindingAction, bool, error) {
	if loader.homeDir == nil {
		return defaultKeyBindingCatalog(), false, nil
	}
	path, err := keymapPath(loader.homeDir, loader.lookupEnv)
	if err != nil {
		return nil, false, err
	}
	readFile := loader.readFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	raw, err := readFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaultKeyBindingCatalog(), false, nil
		}
		return nil, false, fmt.Errorf("read keymap %s: %w", path, err)
	}
	parsed, err := parseKeymapFile(path, string(raw))
	if err != nil {
		return nil, true, err
	}
	merged, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), parsed)
	if err != nil {
		return nil, true, fmt.Errorf("keymap %s: %w", path, err)
	}
	return merged, true, nil
}

func loadKeymapForEdit(store keymapStore) (keymapFile, []keyBindingAction, bool, string, error) {
	if store.homeDir == nil {
		return keymapFile{Bindings: map[string]keymapOverride{}}, defaultKeyBindingCatalog(), false, "", nil
	}
	path, err := keymapPath(store.homeDir, store.lookupEnv)
	if err != nil {
		return keymapFile{}, nil, false, "", err
	}
	readFile := store.readFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	raw, err := readFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return keymapFile{Bindings: map[string]keymapOverride{}}, defaultKeyBindingCatalog(), false, path, nil
		}
		return keymapFile{}, nil, false, path, fmt.Errorf("read keymap %s: %w", path, err)
	}
	parsed, err := parseKeymapFile(path, string(raw))
	if err != nil {
		return keymapFile{}, nil, true, path, err
	}
	merged, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), parsed)
	if err != nil {
		return keymapFile{}, nil, true, path, fmt.Errorf("keymap %s: %w", path, err)
	}
	return parsed, merged, true, path, nil
}

func saveKeymapOverride(store keymapStore, actionID, field string, value *string) (string, error) {
	if field != "plain" && field != "prefix" && field != "keys" {
		return "", fmt.Errorf("unsupported keymap field %q", field)
	}
	current, _, _, path, err := loadKeymapForEdit(store)
	if err != nil {
		return path, err
	}
	defaults := defaultKeyBindingCatalog()
	action, ok := keyBindingActionByID(defaults, actionID)
	if !ok {
		return path, fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	actionID = action.ID

	override := current.Bindings[actionID]
	if current.Bindings == nil {
		current.Bindings = map[string]keymapOverride{}
	}
	defaultValue := action.PlainChord
	if field == "prefix" {
		defaultValue = action.PrefixChord
	}

	if field == "keys" {
		var keys []string
		if value != nil && strings.TrimSpace(*value) != "" {
			keys = strings.Split(*value, ",")
		}
		for i := range keys {
			chord, err := normalizeKeymapTypedChord(keys[i])
			if err != nil {
				return path, err
			}
			keys[i] = chord
		}
		if action.Tier == keyBindingTierTransportDependent {
			keys, err = transportPlainAliasChords(action, keys)
			if err != nil {
				return path, err
			}
		}
		override.Plain = nil
		override.KeysSet = true
		override.Keys = uniqueNonEmptyStrings(keys)
	} else if value == nil || *value == defaultValue {
		if field == "plain" {
			override.Plain = nil
		} else {
			override.Prefix = nil
		}
	} else {
		if err := validateKeymapChord(*value); err != nil {
			return path, err
		}
		copied := *value
		if field == "plain" {
			override.Plain = &copied
		} else {
			override.Prefix = &copied
		}
	}

	if override.Plain == nil && !override.KeysSet && override.Prefix == nil {
		delete(current.Bindings, actionID)
	} else {
		current.Bindings[actionID] = override
	}
	if _, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), current); err != nil {
		return path, err
	}

	if path == "" {
		path, err = keymapPath(store.homeDir, store.lookupEnv)
		if err != nil {
			return "", err
		}
	}
	if err := writeKeymapFile(path, current, store.writeFile); err != nil {
		return path, err
	}
	return path, nil
}

func saveKeymapKeys(store keymapStore, actionID string, keys []string) (string, error) {
	joined := strings.Join(keys, ",")
	return saveKeymapOverride(store, actionID, "keys", &joined)
}

func resetKeymapKeys(store keymapStore, actionID string) (string, error) {
	current, _, _, path, err := loadKeymapForEdit(store)
	if err != nil {
		return path, err
	}
	action, ok := keyBindingActionByID(defaultKeyBindingCatalog(), actionID)
	if !ok {
		return path, fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	actionID = action.ID

	override := current.Bindings[actionID]
	if current.Bindings == nil {
		current.Bindings = map[string]keymapOverride{}
	}
	override.Plain = nil
	override.Keys = nil
	override.KeysSet = false
	if override.Prefix == nil {
		delete(current.Bindings, actionID)
	} else {
		current.Bindings[actionID] = override
	}
	if _, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), current); err != nil {
		return path, err
	}

	if path == "" {
		path, err = keymapPath(store.homeDir, store.lookupEnv)
		if err != nil {
			return "", err
		}
	}
	if err := writeKeymapFile(path, current, store.writeFile); err != nil {
		return path, err
	}
	return path, nil
}

func keyBindingActionByID(actions []keyBindingAction, id string) (keyBindingAction, bool) {
	for _, action := range actions {
		if slices.Contains(keyBindingActionAliases(action), id) {
			return action, true
		}
	}
	return keyBindingAction{}, false
}

func writeKeymapFile(path string, keymap keymapFile, writeFile func(string, []byte, os.FileMode) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create keymap directory: %w", err)
	}
	body := []byte(renderKeymapFile(keymap))
	if writeFile != nil {
		return writeFile(path, body, 0o644)
	}
	return atomicWriteKeymapFile(path, body)
}

func atomicWriteKeymapFile(path string, body []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, config.KeymapFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("create keymap temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("write keymap temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close keymap temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod keymap temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename keymap temp file: %w", err)
	}
	return nil
}

func renderKeymapFile(keymap keymapFile) string {
	var ids []string
	for id, override := range keymap.Bindings {
		if !override.KeysSet && override.Plain == nil && override.Prefix == nil {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var b strings.Builder
	b.WriteString("# Generated by projmux Settings. Edit manually only with supported [bindings.<action-id>] keys arrays.\n")
	for i, id := range ids {
		if i > 0 {
			b.WriteString("\n")
		}
		override := keymap.Bindings[id]
		b.WriteString("[bindings.")
		b.WriteString(formatKeymapActionID(id))
		b.WriteString("]\n")
		if override.KeysSet {
			b.WriteString("keys = ")
			b.WriteString(formatKeymapStringArray(override.Keys))
			b.WriteString("\n")
		} else if override.Plain != nil {
			b.WriteString("plain = ")
			b.WriteString(formatKeymapString(*override.Plain))
			b.WriteString("\n")
		}
		if override.Prefix != nil {
			b.WriteString("prefix = ")
			b.WriteString(formatKeymapString(*override.Prefix))
			b.WriteString("\n")
		}
	}
	return b.String()
}

func formatKeymapString(value string) string {
	return `"` + value + `"`
}

func formatKeymapActionID(id string) string {
	if isTOMLBareKey(id) {
		return id
	}
	escaped := strings.ReplaceAll(id, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

func isTOMLBareKey(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func formatKeymapStringArray(values []string) string {
	var parts []string
	for _, value := range values {
		parts = append(parts, formatKeymapString(value))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func keymapPath(homeDir func() (string, error), lookupEnv func(string) string) (string, error) {
	env := lookupEnv
	if env == nil {
		env = os.Getenv
	}
	home := ""
	if homeDir != nil {
		got, err := homeDir()
		if err != nil {
			return "", fmt.Errorf("resolve keymap home: %w", err)
		}
		home = got
	}
	paths, err := config.Homes{
		HomeDir:    home,
		ConfigHome: env("XDG_CONFIG_HOME"),
		StateHome:  env("XDG_STATE_HOME"),
	}.Paths()
	if err != nil {
		return "", fmt.Errorf("resolve keymap path: %w", err)
	}
	return paths.KeymapFile(), nil
}

func parseKeymapFile(path, raw string) (keymapFile, error) {
	out := keymapFile{Bindings: map[string]keymapOverride{}}
	currentID := ""
	for lineNo, original := range strings.Split(raw, "\n") {
		line := strings.TrimSpace(stripKeymapComment(original))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return out, keymapParseError(path, lineNo+1, "unterminated table header")
			}
			table := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			const prefix = "bindings."
			if !strings.HasPrefix(table, prefix) {
				return out, keymapParseError(path, lineNo+1, "unsupported table %q; use [bindings.<action-id>]", table)
			}
			id := strings.TrimSpace(strings.TrimPrefix(table, prefix))
			id = strings.Trim(id, `"`)
			if !validActionID(id) {
				return out, keymapParseError(path, lineNo+1, "invalid action id %q", id)
			}
			currentID = id
			if _, ok := out.Bindings[id]; !ok {
				out.Bindings[id] = keymapOverride{lineByKey: map[string]int{}}
			}
			continue
		}
		if currentID == "" {
			return out, keymapParseError(path, lineNo+1, "key/value entry must appear under [bindings.<action-id>]")
		}
		key, valueText, ok := strings.Cut(line, "=")
		if !ok {
			return out, keymapParseError(path, lineNo+1, "expected key = \"value\"")
		}
		key = strings.TrimSpace(key)
		override := out.Bindings[currentID]
		if override.lineByKey == nil {
			override.lineByKey = map[string]int{}
		}
		if prev, ok := override.lineByKey[key]; ok {
			return out, keymapParseError(path, lineNo+1, "duplicate %q entry for %s; first set on line %d", key, currentID, prev)
		}
		switch key {
		case "plain":
			value, err := parseKeymapString(strings.TrimSpace(valueText))
			if err != nil {
				return out, keymapParseError(path, lineNo+1, "%v", err)
			}
			if err := validateKeymapChord(value); err != nil {
				return out, keymapParseError(path, lineNo+1, "%s: %v", key, err)
			}
			override.Plain = &value
		case "keys":
			values, err := parseKeymapStringArray(strings.TrimSpace(valueText))
			if err != nil {
				return out, keymapParseError(path, lineNo+1, "%v", err)
			}
			for i, value := range values {
				chord, err := normalizeKeymapAliasChord(value)
				if err != nil {
					return out, keymapParseError(path, lineNo+1, "%s: %v", key, err)
				}
				values[i] = chord
			}
			override.Keys = values
			override.KeysSet = true
		case "prefix":
			value, err := parseKeymapString(strings.TrimSpace(valueText))
			if err != nil {
				return out, keymapParseError(path, lineNo+1, "%v", err)
			}
			if err := validateKeymapChord(value); err != nil {
				return out, keymapParseError(path, lineNo+1, "%s: %v", key, err)
			}
			override.Prefix = &value
		default:
			return out, keymapParseError(path, lineNo+1, "unsupported key %q; supported keys are keys, plain, and prefix", key)
		}
		override.lineByKey[key] = lineNo + 1
		out.Bindings[currentID] = override
	}
	return out, nil
}

func stripKeymapComment(line string) string {
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

func parseKeymapString(text string) (string, error) {
	if len(text) < 2 || text[0] != '"' || text[len(text)-1] != '"' {
		return "", fmt.Errorf("value must be a double-quoted string")
	}
	body := text[1 : len(text)-1]
	if strings.Contains(body, "\\") {
		return "", fmt.Errorf("escape sequences are not supported in keymap strings")
	}
	return body, nil
}

func parseKeymapStringArray(text string) ([]string, error) {
	if len(text) < 2 || text[0] != '[' || text[len(text)-1] != ']' {
		return nil, fmt.Errorf("value must be an array of double-quoted strings")
	}
	body := strings.TrimSpace(text[1 : len(text)-1])
	if body == "" {
		return []string{}, nil
	}
	var out []string
	for part := range strings.SplitSeq(body, ",") {
		value, err := parseKeymapString(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, nil
}

func mergeKeymapOverrides(actions []keyBindingAction, keymap keymapFile) ([]keyBindingAction, error) {
	byID := map[string]int{}
	for i, action := range actions {
		if action.ID == "" {
			continue
		}
		for _, id := range keyBindingActionAliases(action) {
			byID[id] = i
		}
	}
	for id, override := range keymap.Bindings {
		idx, ok := byID[id]
		if !ok {
			// Unknown/dropped action ids are tolerated and ignored
			// (graceful), per the hard-drop policy: an old keymap.toml
			// referencing a since-removed legacy id must not error or
			// crash — we simply skip the binding and keep defaults.
			continue
		}
		if override.KeysSet {
			keys := override.Keys
			if actions[idx].Tier == keyBindingTierTransportDependent && len(keys) != 0 {
				aliases, err := transportPlainAliasChords(actions[idx], keys)
				if err != nil {
					return nil, fmt.Errorf("keymap binding %q: %w", id, err)
				}
				keys = append([]string{actions[idx].PlainChord}, aliases...)
			}
			actions[idx].PlainChords = uniqueNonEmptyStrings(keys)
			if actions[idx].Tier != keyBindingTierTransportDependent {
				actions[idx].PlainChord = firstNonEmptyString(actions[idx].PlainChords)
			}
		} else if override.Plain != nil {
			if actions[idx].Tier == keyBindingTierTransportDependent {
				aliases, err := transportPlainAliasChords(actions[idx], []string{*override.Plain})
				if err != nil {
					return nil, fmt.Errorf("keymap binding %q: %w", id, err)
				}
				actions[idx].PlainChords = uniqueNonEmptyStrings(append([]string{actions[idx].PlainChord}, aliases...))
			} else {
				actions[idx].PlainChord = *override.Plain
				if *override.Plain == "" {
					actions[idx].PlainChords = []string{}
				} else {
					actions[idx].PlainChords = nil
				}
			}
		}
		if override.Prefix != nil {
			actions[idx].PrefixChord = *override.Prefix
		}
	}
	if err := validateKeymapConflicts(actions); err != nil {
		return nil, err
	}
	return actions, nil
}

func transportPlainAliasChords(action keyBindingAction, keys []string) ([]string, error) {
	if action.Tier != keyBindingTierTransportDependent {
		return uniqueNonEmptyStrings(keys), nil
	}
	transportDefault := strings.TrimSpace(action.PlainChord)
	var aliases []string
	for _, key := range uniqueNonEmptyStrings(keys) {
		if key == transportDefault {
			return nil, fmt.Errorf("key %q is the transport-dependent default for %s; omit it from plain aliases", key, action.ID)
		}
		aliases = append(aliases, key)
	}
	return aliases, nil
}

func validateKeymapChord(value string) error {
	if value == "" {
		return nil
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("chord must not have leading or trailing whitespace")
	}
	if strings.ContainsAny(value, " \t\r\n'\"#{},\\") {
		return fmt.Errorf("chord %q contains unsupported tmux config characters", value)
	}
	return nil
}

func normalizeKeymapTypedChord(value string) (string, error) {
	return normalizeKeymapAliasChord(value)
}

func normalizeKeymapAliasChord(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	lower := strings.ToLower(value)
	rejected := []string{"sendinput", "csi:", "\\u001b", "\\x1b", "\x1b", "esc[", "esc ["}
	for _, marker := range rejected {
		if strings.Contains(lower, marker) {
			return "", fmt.Errorf("key alias must be a tmux plain chord, not an escape/sendInput payload")
		}
	}
	if strings.HasPrefix(lower, "[") && strings.HasSuffix(lower, "u") {
		return "", fmt.Errorf("key alias must not be a modified-key escape payload")
	}
	if strings.HasPrefix(lower, "user") {
		return "", fmt.Errorf("key alias must not be a tmux User fallback key")
	}
	if err := validateKeymapChord(value); err != nil {
		return "", err
	}
	return value, nil
}

func validateKeymapConflicts(actions []keyBindingAction) error {
	global := map[string]string{}
	internal := map[string]map[string]string{}
	for _, action := range actions {
		for _, chord := range keyBindingEffectivePlainChords(action) {
			if action.Kind == keyBindingActionPickerInternal {
				surface := strings.TrimSpace(action.Surface)
				if surface == "" {
					surface = action.ID
				}
				if internal[surface] == nil {
					internal[surface] = map[string]string{}
				}
				if prev := internal[surface][chord]; prev != "" && prev != action.ID {
					return fmt.Errorf("key %q is bound to both %s and %s in %s", chord, prev, action.ID, surface)
				}
				internal[surface][chord] = action.ID
				continue
			}
			if prev := global[chord]; prev != "" && prev != action.ID {
				return fmt.Errorf("key %q is bound to both %s and %s", chord, prev, action.ID)
			}
			global[chord] = action.ID
		}
	}
	return nil
}

func validActionID(id string) bool {
	if id == "" || strings.Contains(id, ".") || strings.ContainsAny(id, " \t\r\n\"") {
		return false
	}
	return true
}

func keymapParseError(path string, line int, format string, args ...any) error {
	return fmt.Errorf("%s:%d: %s", filepath.Clean(path), line, fmt.Sprintf(format, args...))
}
