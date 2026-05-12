package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
)

type keymapOverride struct {
	Plain     *string
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
	if field != "plain" && field != "prefix" {
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

	override := current.Bindings[actionID]
	if current.Bindings == nil {
		current.Bindings = map[string]keymapOverride{}
	}
	defaultValue := action.PlainChord
	if field == "prefix" {
		defaultValue = action.PrefixChord
	}

	if value == nil || *value == defaultValue {
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

	if override.Plain == nil && override.Prefix == nil {
		delete(current.Bindings, actionID)
	} else {
		current.Bindings[actionID] = override
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
		if action.ID == id {
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
		if override.Plain == nil {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var b strings.Builder
	b.WriteString("# Generated by projmux Settings. Edit manually only with supported [bindings.<action-id>] plain keys.\n")
	for i, id := range ids {
		if i > 0 {
			b.WriteString("\n")
		}
		override := keymap.Bindings[id]
		b.WriteString("[bindings.")
		b.WriteString(id)
		b.WriteString("]\n")
		if override.Plain != nil {
			b.WriteString("plain = ")
			b.WriteString(formatKeymapString(*override.Plain))
			b.WriteString("\n")
		}
	}
	return b.String()
}

func formatKeymapString(value string) string {
	return `"` + value + `"`
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
		value, err := parseKeymapString(strings.TrimSpace(valueText))
		if err != nil {
			return out, keymapParseError(path, lineNo+1, "%v", err)
		}
		if err := validateKeymapChord(value); err != nil {
			return out, keymapParseError(path, lineNo+1, "%s: %v", key, err)
		}
		override := out.Bindings[currentID]
		if override.lineByKey == nil {
			override.lineByKey = map[string]int{}
		}
		if prev, ok := override.lineByKey[key]; ok {
			return out, keymapParseError(path, lineNo+1, "duplicate %q entry for %s; first set on line %d", key, currentID, prev)
		}
		switch key {
		case "plain":
			override.Plain = &value
		case "prefix":
			override.Prefix = &value
		default:
			return out, keymapParseError(path, lineNo+1, "unsupported key %q; supported keys are plain and prefix", key)
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

func mergeKeymapOverrides(actions []keyBindingAction, keymap keymapFile) ([]keyBindingAction, error) {
	byID := map[string]int{}
	for i, action := range actions {
		if action.ID == "" {
			continue
		}
		byID[action.ID] = i
	}
	for id, override := range keymap.Bindings {
		idx, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("keymap binding %q: unknown action id", id)
		}
		if override.Plain != nil {
			actions[idx].PlainChord = *override.Plain
		}
		if override.Prefix != nil {
			actions[idx].PrefixChord = *override.Prefix
		}
	}
	return actions, nil
}

func validateKeymapChord(value string) error {
	if value == "" {
		return nil
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("chord must not have leading or trailing whitespace")
	}
	if strings.ContainsAny(value, " \t\r\n'\"#{}\\") {
		return fmt.Errorf("chord %q contains unsupported tmux config characters", value)
	}
	return nil
}

func validActionID(id string) bool {
	if id == "" || strings.Contains(id, ".") || strings.ContainsAny(id, " \t\r\n") {
		return false
	}
	return true
}

func keymapParseError(path string, line int, format string, args ...any) error {
	return fmt.Errorf("%s:%d: %s", filepath.Clean(path), line, fmt.Sprintf(format, args...))
}
