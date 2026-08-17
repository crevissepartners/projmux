package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/platformkeys"
)

type keymapOverride struct {
	Plain        *string
	Keys         []string
	KeysSet      bool
	Sequences    []string
	SequencesSet bool
	Prefix       *string
	lineByKey    map[string]int
}

type keymapFile struct {
	// SchemaVersion is the root `schema_version` marker. A file that has no
	// marker is v0 — the only shape projmux wrote before this schema existed —
	// and decodes as 0 rather than as "assume current", so the migrator can
	// tell an unversioned file apart from a migrated one.
	SchemaVersion int
	Bindings      map[string]keymapOverride
}

// keymapSchemaVersion is the keymap TOML schema this binary writes.
//
// This marker is its own version domain. It is deliberately *not* shared with
// the CLI resource registry's `apiVersion: projmux.io/v1alpha1` /
// `schemaVersion: 1` envelope: the two have separate markers, separate backups,
// separate migrators and separate rollback paths, so one can fail and be
// recovered without touching the other.
const (
	keymapSchemaVersionV0 = 0
	keymapSchemaVersionV1 = 1
	keymapSchemaVersionV2 = 2
	keymapSchemaVersion   = keymapSchemaVersionV2
)

const keymapSchemaVersionKey = "schema_version"

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
	actionID = keymapBindingKeyForAction(current, action)

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

	if override.Plain == nil && !override.KeysSet && !override.SequencesSet && override.Prefix == nil {
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

// saveKeymapSequences is the Settings writer seam for schema-v2 sequence
// authoring. It deliberately reuses normalizeKeymapSequence and the merged
// catalog conflict validator rather than defining a second Settings grammar.
func saveKeymapSequences(store keymapStore, actionID string, sequences []string) (string, error) {
	current, _, _, path, err := loadKeymapForEdit(store)
	if err != nil {
		return path, err
	}
	action, ok := keyBindingActionByID(defaultKeyBindingCatalog(), actionID)
	if !ok {
		return path, fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	actionID = keymapBindingKeyForAction(current, action)
	if current.Bindings == nil {
		current.Bindings = map[string]keymapOverride{}
	}
	current.SchemaVersion = keymapSchemaVersion
	override := current.Bindings[actionID]
	normalized := make([]string, 0, len(sequences))
	for _, sequence := range sequences {
		value, normalizeErr := normalizeKeymapSequence(sequence)
		if normalizeErr != nil {
			return path, normalizeErr
		}
		normalized = append(normalized, value)
	}
	override.SequencesSet = true
	override.Sequences = normalized
	current.Bindings[actionID] = override
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

func resetKeymapSequences(store keymapStore, actionID string) (string, error) {
	current, _, _, path, err := loadKeymapForEdit(store)
	if err != nil {
		return path, err
	}
	action, ok := keyBindingActionByID(defaultKeyBindingCatalog(), actionID)
	if !ok {
		return path, fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	actionID = keymapBindingKeyForAction(current, action)
	if current.Bindings == nil {
		current.Bindings = map[string]keymapOverride{}
	}
	override := current.Bindings[actionID]
	override.Sequences = nil
	override.SequencesSet = false
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

func resetKeymapBinding(store keymapStore, actionID string) (string, error) {
	current, _, _, path, err := loadKeymapForEdit(store)
	if err != nil {
		return path, err
	}
	action, ok := keyBindingActionByID(defaultKeyBindingCatalog(), actionID)
	if !ok {
		return path, fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	delete(current.Bindings, keymapBindingKeyForAction(current, action))
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

func resetKeymapKeys(store keymapStore, actionID string) (string, error) {
	current, _, _, path, err := loadKeymapForEdit(store)
	if err != nil {
		return path, err
	}
	action, ok := keyBindingActionByID(defaultKeyBindingCatalog(), actionID)
	if !ok {
		return path, fmt.Errorf("unknown keybinding action: %s", actionID)
	}
	actionID = keymapBindingKeyForAction(current, action)

	override := current.Bindings[actionID]
	if current.Bindings == nil {
		current.Bindings = map[string]keymapOverride{}
	}
	override.Plain = nil
	override.Keys = nil
	override.KeysSet = false
	if override.Prefix == nil && !override.SequencesSet {
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
	return writeKeymapBytes(path, []byte(renderKeymapFile(keymap)), writeFile)
}

// writeKeymapBytes is the one durable keymap write. The Settings writer, the
// schema migrator and the migration rollback all go through it, so they share a
// single directory-creation, injection-seam and atomic-replacement policy.
func writeKeymapBytes(path string, body []byte, writeFile func(string, []byte, os.FileMode) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create keymap directory: %w", err)
	}
	if writeFile != nil {
		return writeFile(path, body, defaultKeymapFileMode)
	}
	return atomicWriteKeymapFile(path, body)
}

func atomicWriteKeymapFile(path string, body []byte) error {
	target, mode, err := resolveKeymapWriteTarget(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(target)
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
	if err := os.Chmod(tmpName, mode); err != nil {
		return fmt.Errorf("chmod keymap temp file: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("rename keymap temp file: %w", err)
	}
	return nil
}

// defaultKeymapFileMode is the mode a keymap projmux creates gets. An existing
// file keeps whatever mode it already had.
const defaultKeymapFileMode os.FileMode = 0o644

// resolveKeymapWriteTarget returns the path an atomic replacement must rename
// onto, plus the mode the replacement must carry.
//
// Renaming onto a symlink replaces the *link* with a regular file, which
// silently detaches a keymap someone deliberately pointed at a dotfiles
// checkout. So a symlinked keymap is resolved first and the replacement lands
// on the real file. Only the supported shape is accepted — a symlink chain that
// resolves to a regular file. Anything else (a directory, a device, a dangling
// link) is refused rather than clobbered.
func resolveKeymapWriteTarget(path string) (string, os.FileMode, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return path, defaultKeymapFileMode, nil
		}
		return "", 0, fmt.Errorf("inspect keymap %s: %w", path, err)
	}
	target := path
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", 0, fmt.Errorf("resolve keymap symlink %s: %w", path, err)
		}
		target = resolved
		info, err = os.Lstat(target)
		if err != nil {
			return "", 0, fmt.Errorf("inspect keymap symlink target %s: %w", target, err)
		}
	}
	if !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("keymap %s is not a regular file", target)
	}
	return target, info.Mode().Perm(), nil
}

func renderKeymapFile(keymap keymapFile) string {
	var ids []string
	for id, override := range keymap.Bindings {
		if !override.KeysSet && !override.SequencesSet && override.Plain == nil && override.Prefix == nil {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var b strings.Builder
	b.WriteString("# Generated by projmux Settings. Edit manually only with supported [bindings.<action-id>] keys arrays.\n")
	if keymap.SchemaVersion >= keymapSchemaVersionV1 {
		b.WriteString(keymapSchemaVersionKey)
		b.WriteString(" = ")
		b.WriteString(strconv.Itoa(keymap.SchemaVersion))
		b.WriteString("\n")
	}
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
		if override.SequencesSet {
			b.WriteString("sequences = ")
			b.WriteString(formatKeymapStringArray(override.Sequences))
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
	schemaVersionLine := 0
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
			quoted := len(id) >= 2 && strings.HasPrefix(id, `"`) && strings.HasSuffix(id, `"`)
			id = strings.Trim(id, `"`)
			if !validActionID(id, quoted) {
				return out, keymapParseError(path, lineNo+1, "invalid action id %q", id)
			}
			if id == retiredPaneRenameActionID {
				return out, keymapParseError(
					path,
					lineNo+1,
					"keybinding action %q was removed; replace [bindings.%s] with [bindings.%s]",
					retiredPaneRenameActionID,
					retiredPaneRenameActionID,
					paneRenameActionID,
				)
			}
			currentID = id
			if _, ok := out.Bindings[id]; !ok {
				out.Bindings[id] = keymapOverride{lineByKey: map[string]int{}}
			}
			continue
		}
		key, valueText, ok := strings.Cut(line, "=")
		if !ok {
			return out, keymapParseError(path, lineNo+1, "expected key = \"value\"")
		}
		key = strings.TrimSpace(key)
		if currentID == "" {
			if key != keymapSchemaVersionKey {
				return out, keymapParseError(path, lineNo+1, "key/value entry must appear under [bindings.<action-id>]")
			}
			if schemaVersionLine > 0 {
				return out, keymapParseError(path, lineNo+1,
					"duplicate %q entry; first set on line %d", keymapSchemaVersionKey, schemaVersionLine)
			}
			version, err := parseKeymapSchemaVersion(strings.TrimSpace(valueText))
			if err != nil {
				return out, keymapParseError(path, lineNo+1, "%s: %v", keymapSchemaVersionKey, err)
			}
			out.SchemaVersion = version
			schemaVersionLine = lineNo + 1
			continue
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
		case "sequences":
			if out.SchemaVersion < keymapSchemaVersionV2 {
				return out, keymapParseError(path, lineNo+1, "%s requires schema_version = %d", key, keymapSchemaVersionV2)
			}
			values, err := parseKeymapStringArray(strings.TrimSpace(valueText))
			if err != nil {
				return out, keymapParseError(path, lineNo+1, "%v", err)
			}
			for i, value := range values {
				sequence, err := normalizeKeymapSequence(value)
				if err != nil {
					return out, keymapParseError(path, lineNo+1, "%s: %v", key, err)
				}
				values[i] = sequence
			}
			override.Sequences = values
			override.SequencesSet = true
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
			return out, keymapParseError(path, lineNo+1, "unsupported key %q; supported keys are keys, sequences, plain, and prefix", key)
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

// parseKeymapSchemaVersion reads the root `schema_version` marker.
//
// A version newer than this binary writes fails closed rather than being read
// as best-effort: a forward file may name actions or fields this binary has no
// disposition for, and merging it would silently drop them on the next write.
func parseKeymapSchemaVersion(text string) (int, error) {
	version, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		return 0, fmt.Errorf("value must be an integer")
	}
	if version < keymapSchemaVersionV1 {
		return 0, fmt.Errorf("value must be at least %d; omit the marker for an unversioned file", keymapSchemaVersionV1)
	}
	if version > keymapSchemaVersion {
		return 0, fmt.Errorf("schema version %d is newer than the supported version %d; upgrade projmux to read this keymap",
			version, keymapSchemaVersion)
	}
	return version, nil
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
		if override.SequencesSet {
			if actions[idx].Kind == keyBindingActionPickerInternal {
				return nil, fmt.Errorf("keymap binding %q: sequences are not supported for picker-local actions", id)
			}
			actions[idx].Sequences = append([]string(nil), override.Sequences...)
		}
	}
	migrateLegacyAIPickerDefaultOverrides(actions, keymap)
	if err := validateKeymapConflicts(actions); err != nil {
		return nil, err
	}
	return actions, nil
}

func migrateLegacyAIPickerDefaultOverrides(actions []keyBindingAction, keymap keymapFile) {
	type migration struct {
		actionID       string
		defaultOwnerID string
		oldChord       string
		newChord       string
	}
	for _, migration := range []migration{
		{actionID: "AISplitPickerToggle", defaultOwnerID: "AIResumePickerToggle", oldChord: "M-4", newChord: "M-7"},
		{actionID: "AIResumePickerToggle", defaultOwnerID: "AISplitPickerToggle", oldChord: "M-7", newChord: "M-4"},
	} {
		override, ok := keymapOverrideForActionID(actions, keymap, migration.actionID)
		if !ok || !keymapOverrideBindsChord(override, migration.oldChord) {
			continue
		}

		idx := keyBindingActionIndex(actions, migration.actionID)
		defaultOwnerIdx := keyBindingActionIndex(actions, migration.defaultOwnerID)
		if idx < 0 || defaultOwnerIdx < 0 || keymapActionHasPlainOverride(actions, keymap, migration.defaultOwnerID) ||
			!slices.Contains(keyBindingEffectivePlainChords(actions[defaultOwnerIdx]), migration.oldChord) {
			continue
		}
		actions[idx].PlainChords = replaceKeymapChord(
			keyBindingEffectivePlainChords(actions[idx]),
			migration.oldChord,
			migration.newChord,
		)
		actions[idx].PlainChord = firstNonEmptyString(actions[idx].PlainChords)
	}
}

func keymapOverrideBindsChord(override keymapOverride, chord string) bool {
	if override.KeysSet {
		return slices.Contains(override.Keys, chord)
	}
	return override.Plain != nil && *override.Plain == chord
}

func keymapActionHasPlainOverride(actions []keyBindingAction, keymap keymapFile, actionID string) bool {
	override, ok := keymapOverrideForActionID(actions, keymap, actionID)
	return ok && (override.KeysSet || override.Plain != nil)
}

// keymapOverrideForActionID finds an action's override whatever spelling the
// file used for its table.
//
// Looking the id up directly in keymap.Bindings only works while there is one
// spelling per action. A v1 file names the same action by its canonical id, so a
// direct lookup silently misses and the caller concludes the user set nothing —
// which, for the AI picker default swap below, would resurrect a chord conflict
// the swap exists to resolve.
func keymapOverrideForActionID(actions []keyBindingAction, keymap keymapFile, actionID string) (keymapOverride, bool) {
	action, ok := keyBindingActionByID(actions, actionID)
	if !ok {
		override, present := keymap.Bindings[actionID]
		return override, present
	}
	for _, id := range keyBindingActionAliases(action) {
		if override, present := keymap.Bindings[id]; present {
			return override, true
		}
	}
	return keymapOverride{}, false
}

func keyBindingActionIndex(actions []keyBindingAction, actionID string) int {
	for i, action := range actions {
		if action.ID == actionID {
			return i
		}
	}
	return -1
}

func replaceKeymapChord(chords []string, oldChord, newChord string) []string {
	replaced := make([]string, len(chords))
	for i, chord := range chords {
		if chord == oldChord {
			chord = newChord
		}
		replaced[i] = chord
	}
	return uniqueNonEmptyStrings(replaced)
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

const keymapReservedAuthoringReason = "reserved control/navigation keys cannot be authored in Settings; choose a printable, function, or non-reserved modified key"

// reservedKeymapAuthoringBase classifies the logical base key used by the
// Settings authoring policy. It intentionally does not participate in parsing
// or runtime compilation: existing keymap files and shipped defaults may keep
// using these keys, while every new Settings write is rejected separately.
//
// tmux and the picker expose several names for the same logical key (for
// example Return/Enter, BSpace/Backspace, DC/Delete and PPage/PageUp). Strip
// portable modifier prefixes first so a modified spelling cannot bypass the
// same base-key policy. C-m/C-i/C-[ are terminal aliases for
// Enter/Tab/Escape, including when another modifier wraps them.
func reservedKeymapAuthoringBase(chord string) (string, bool) {
	parts := strings.Split(strings.TrimSpace(chord), "-")
	if len(parts) == 0 {
		return "", false
	}
	control := false
	for len(parts) > 1 {
		switch strings.ToLower(parts[0]) {
		case "c", "ctrl", "control":
			control = true
		case "m", "alt", "meta", "option", "s", "shift":
		default:
			goto base
		}
		parts = parts[1:]
	}

base:
	base := strings.ToLower(strings.Join(parts, "-"))
	if control {
		switch base {
		case "m":
			return "Enter", true
		case "i":
			return "Tab", true
		case "[":
			return "Escape", true
		}
	}
	switch base {
	case "enter", "return", "cr":
		return "Enter", true
	case "escape", "esc":
		return "Escape", true
	case "tab", "btab":
		return "Tab", true
	case "backspace", "bspace", "bs":
		return "Backspace", true
	case "delete", "del", "dc":
		return "Delete", true
	case "up":
		return "Up", true
	case "down":
		return "Down", true
	case "left":
		return "Left", true
	case "right":
		return "Right", true
	case "home":
		return "Home", true
	case "end":
		return "End", true
	case "pageup", "page-up", "pgup", "ppage":
		return "PageUp", true
	case "pagedown", "page-down", "pgdn", "npage":
		return "PageDown", true
	default:
		return "", false
	}
}

func validateKeymapAuthoringChord(chord string) error {
	if _, reserved := reservedKeymapAuthoringBase(chord); reserved {
		return fmt.Errorf("%s", keymapReservedAuthoringReason)
	}
	return nil
}

func normalizeKeymapAuthoringChord(chord string) (string, error) {
	normalized, err := normalizeKeymapTypedChord(chord)
	if err != nil {
		return "", err
	}
	if err := validateKeymapAuthoringChord(normalized); err != nil {
		return "", err
	}
	return normalized, nil
}

func validateKeymapAuthoringSequence(sequence string) error {
	for stroke := range strings.FieldsSeq(sequence) {
		if err := validateKeymapAuthoringChord(stroke); err != nil {
			return err
		}
	}
	return nil
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
	if strings.HasPrefix(lower, "[") && strings.Contains(lower, ";") {
		return "", fmt.Errorf("key alias must not be an xterm modified-key escape payload")
	}
	if strings.HasPrefix(lower, "user") {
		return "", fmt.Errorf("key alias must not be a tmux User fallback key")
	}
	if err := validateKeymapChord(value); err != nil {
		return "", err
	}
	return value, nil
}

// normalizeKeymapSequence owns the only whitespace grammar in keymap chords.
// Each stroke is otherwise parsed by the existing safe chord normalizer. Only
// the first stroke must be modified/navigation/function; later plain printable
// strokes remain terminal-delivered. The native broker separately allowlists
// the subset it can represent as safe physical modified chords.
func normalizeKeymapSequence(value string) (string, error) {
	if strings.TrimSpace(value) != value {
		return "", fmt.Errorf("sequence must not have leading or trailing whitespace")
	}
	strokes := strings.Fields(value)
	if len(strokes) < 2 || len(strokes) > 4 {
		return "", fmt.Errorf("sequence must contain 2 to 4 strokes")
	}
	if strings.Join(strokes, " ") != value {
		return "", fmt.Errorf("sequence strokes must be separated by one space")
	}
	for i, stroke := range strokes {
		normalized, err := normalizeKeymapAliasChord(stroke)
		if err != nil {
			return "", fmt.Errorf("stroke %d: %w", i+1, err)
		}
		if named, ok := keymapUndeliverableStrokeAliases[strings.ToLower(normalized)]; ok {
			if named == "Escape" {
				return "", fmt.Errorf("stroke %d %q is Escape, which is reserved for cancelling a sequence", i+1, stroke)
			}
			return "", fmt.Errorf("stroke %d %q is not delivered under that name; write %q instead", i+1, stroke, named)
		}
		if !safeKeymapSequenceStroke(normalized) {
			return "", fmt.Errorf("stroke %d %q is not a safe logical tmux chord", i+1, stroke)
		}
		strokes[i] = normalized
	}
	if _, modified := platformkeys.ParseBinding(strokes[0]); !modified && !keymapNavigationOrFunctionStroke(strokes[0]) {
		return "", fmt.Errorf("first stroke %q must be modified, navigation, or a function key", strokes[0])
	}
	return strings.Join(strokes, " "), nil
}

// keymapUndeliverableStrokeAliases are control chords a terminal never reports
// under that spelling. tmux resolves the incoming byte to the named key
// instead, so a sequence written with the control form binds a key the user
// cannot press: the real keystroke misses the leaf and hits the table's cancel
// fallback, silently doing nothing. Rejecting at parse time is what keeps
// "a configured sequence runs its action" true. Measured against tmux 3.5a
// driven by a real client: CR -> Enter, TAB -> Tab, ESC -> Escape.
var keymapUndeliverableStrokeAliases = map[string]string{
	"c-m": "Enter",
	"c-i": "Tab",
	"c-[": "Escape",
}

func safeKeymapSequenceStroke(stroke string) bool {
	lower := strings.ToLower(stroke)
	if lower == "escape" || lower == "esc" {
		return false
	}
	if _, ok := platformkeys.ParseBinding(stroke); ok {
		return true
	}
	if keymapNavigationOrFunctionStroke(stroke) {
		return true
	}
	switch lower {
	case "enter", "return", "tab", "space", "backspace", "bs", "delete":
		return true
	}
	return len(stroke) == 1 && stroke[0] >= 0x21 && stroke[0] <= 0x7e
}

func keymapNavigationOrFunctionStroke(stroke string) bool {
	switch strings.ToLower(stroke) {
	case "up", "down", "left", "right", "home", "end", "pageup", "pgup", "pagedown", "pgdn":
		return true
	}
	lower := strings.ToLower(stroke)
	if !strings.HasPrefix(lower, "f") {
		return false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(lower, "f"))
	return err == nil && n >= 1 && n <= 20
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
	type sequenceOwner struct {
		action  string
		value   string
		strokes []string
	}
	var sequences []sequenceOwner
	for _, action := range actions {
		for _, sequence := range keyBindingEffectiveSequences(action) {
			strokes := strings.Split(sequence, " ")
			if owner := global[strokes[0]]; owner != "" {
				return fmt.Errorf("sequence %q for %s starts with key %q already bound to %s", sequence, action.ID, strokes[0], owner)
			}
			for _, existing := range sequences {
				common := 0
				for common < len(strokes) && common < len(existing.strokes) && strokes[common] == existing.strokes[common] {
					common++
				}
				if common == len(strokes) && common == len(existing.strokes) {
					return fmt.Errorf("sequence %q is bound to both %s and %s", sequence, existing.action, action.ID)
				}
				if common == len(strokes) || common == len(existing.strokes) {
					return fmt.Errorf("sequence %q for %s has a strict-prefix conflict with %q for %s", sequence, action.ID, existing.value, existing.action)
				}
			}
			sequences = append(sequences, sequenceOwner{action: action.ID, value: sequence, strokes: strokes})
		}
	}
	return nil
}

// validActionID reports whether a `[bindings.<id>]` table names a usable action.
//
// A dot is only legal in a quoted id. Unquoted, `[bindings.window.create]` is a
// nested TOML table rather than a key named `window.create`, and silently
// accepting it would make the v1 canonical ids look writable in a spelling this
// parser and a real TOML parser would disagree about. The v1 writer always
// quotes a dotted id, so the strict reading costs nothing.
func validActionID(id string, quoted bool) bool {
	if id == "" || strings.ContainsAny(id, " \t\r\n\"") {
		return false
	}
	if strings.Contains(id, ".") && !quoted {
		return false
	}
	return true
}

func keymapParseError(path string, line int, format string, args ...any) error {
	return fmt.Errorf("%s:%d: %s", filepath.Clean(path), line, fmt.Sprintf(format, args...))
}
