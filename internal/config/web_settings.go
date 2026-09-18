package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// web.toml is the web layer of projmux settings: values an operator changed in
// the web UI and that only the web reads. The TUI never opens it, and a web
// change never writes a TUI or central file. An absent file, or an absent key,
// means the web shows the value of the layer the key belongs to.

// WebSettingsFileName is the web settings layer file inside ConfigDir.
const WebSettingsFileName = "web.toml"

// WebSettingsFile returns the web settings layer file.
func (p Paths) WebSettingsFile() string {
	return filepath.Join(p.ConfigDir, WebSettingsFileName)
}

// WebSettingKind is the value shape of one web setting.
type WebSettingKind uint8

const (
	// WebSettingSwitch is TOML true/false, spelled "on"/"off" on the API.
	WebSettingSwitch WebSettingKind = iota + 1
	// WebSettingChoice is a TOML string, one of the spec's Choices when it
	// lists any, spelled the same on the API.
	WebSettingChoice
)

// WebSettingSpec is one allowed web.toml key.
//
// Key is the API key (the `key` of PATCH /api/v1/web/settings). Table and Name
// are where the value lives in web.toml: `[Table]` then `Name = value`. A
// segment spelled `<...>` is a placeholder that matches one bare-key segment;
// a placeholder with the same spelling binds the same text in Key, Table and
// Name. Keys with placeholders are dynamic: the loader asks the caller whether
// the bound key exists (the usage providers and windows are owned by the app).
type WebSettingSpec struct {
	Key     string
	Table   string
	Name    string
	Kind    WebSettingKind
	Choices []string
}

// webSettingSpecs is the one registry of web.toml keys. Adding a web setting
// is adding one entry here; the parser, the writer and the ordering of the
// written file all follow this table. Entries are matched in order, so a
// literal entry must come before a placeholder entry that would also match it.
var webSettingSpecs = []WebSettingSpec{
	{Key: "ai.splitCwdFrom", Table: "ai", Name: "split_cwd_from", Kind: WebSettingChoice, Choices: []string{"project", "pane"}},
	{Key: "statusbar.notifications", Table: "statusbar", Name: "notifications", Kind: WebSettingSwitch},
	{Key: "statusbar.project", Table: "statusbar", Name: "project", Kind: WebSettingSwitch},
	{Key: "statusbar.working-directory", Table: "statusbar", Name: "working_directory", Kind: WebSettingSwitch},
	{Key: "statusbar.git", Table: "statusbar", Name: "git", Kind: WebSettingSwitch},
	{Key: "statusbar.clock", Table: "statusbar", Name: "clock", Kind: WebSettingSwitch},
	// The usage HUD is a tree: the HUD, its providers, their windows. Every
	// node that has children is a table and names itself `visible`, so the
	// file stays valid TOML (a key cannot be both a value and a table).
	{Key: "statusbar.usage", Table: "statusbar.usage", Name: "visible", Kind: WebSettingSwitch},
	{Key: "statusbar.usage.<provider>", Table: "statusbar.usage.<provider>", Name: "visible", Kind: WebSettingSwitch},
	{Key: "statusbar.usage.<provider>.<window>", Table: "statusbar.usage.<provider>", Name: "<window>", Kind: WebSettingSwitch},
}

func (spec WebSettingSpec) dynamic() bool { return strings.Contains(spec.Key, "<") }

// WebSettingKnown answers whether a dynamic key (one matched through a
// placeholder) names something that exists. nil accepts every structurally
// valid key.
type WebSettingKnown func(key string) bool

// WebSettingsError is a web.toml that cannot be read as the registry
// describes. Path and Line name the offending line; Line is 0 when the file
// as a whole is at fault.
type WebSettingsError struct {
	Path    string
	Line    int
	Message string
}

func (e *WebSettingsError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", e.Path, e.Line, e.Message)
	}
	return e.Path + ": " + e.Message
}

// WebSettings is the content of web.toml, keyed by API key with API values
// ("on"/"off" for a switch). Entries this build does not know are kept
// aside, unapplied, so a write can put them back (see Skipped).
type WebSettings struct {
	values  map[string]string
	skipped []webSettingSkipped
}

// SkippedWebSetting is a web.toml entry this build does not know: a key not
// in the registry, or a dynamic key the caller did not know. Its value is
// neither checked nor applied, and a web write keeps it. Key is the dotted
// TOML path, not an API key.
type SkippedWebSetting struct {
	Line int
	Key  string
}

// webSettingSkipped is a skipped entry as a write puts it back: raw is the
// value text after `=`, trimmed.
type webSettingSkipped struct {
	line             int
	table, name, raw string
}

// Skipped returns the entries this build did not know, in file order.
func (s WebSettings) Skipped() []SkippedWebSetting {
	out := make([]SkippedWebSetting, len(s.skipped))
	for i, e := range s.skipped {
		key := e.name
		if e.table != "" {
			key = e.table + "." + e.name
		}
		out[i] = SkippedWebSetting{Line: e.line, Key: key}
	}
	return out
}

// Value returns the web value of key, and whether the web set one.
func (s WebSettings) Value(key string) (string, bool) {
	value, ok := s.values[key]
	return value, ok
}

// Keys returns the set keys in the order they are written.
func (s WebSettings) Keys() []string {
	type entry struct {
		key, table, name string
		spec             int
	}
	entries := make([]entry, 0, len(s.values))
	tableRank := map[string]int{}
	for key := range s.values {
		index, table, name, ok := webSettingLocation(key)
		if !ok {
			continue
		}
		entries = append(entries, entry{key: key, table: table, name: name, spec: index})
		if rank, seen := tableRank[table]; !seen || index < rank {
			tableRank[table] = index
		}
	}
	slices.SortFunc(entries, func(a, b entry) int {
		if a.table != b.table {
			if ra, rb := tableRank[a.table], tableRank[b.table]; ra != rb {
				return ra - rb
			}
			return strings.Compare(a.table, b.table)
		}
		if a.spec != b.spec {
			return a.spec - b.spec
		}
		return strings.Compare(a.name, b.name)
	})
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.key
	}
	return out
}

// Set records a web value for key. It checks the key's shape against the
// registry and the value against its kind; whether a dynamic key names an
// existing provider or window is the caller's check. A skipped entry at the
// same location is dropped, so the written file holds only the new value.
func (s *WebSettings) Set(key, value string) error {
	index, table, name, ok := webSettingLocation(key)
	if !ok {
		return fmt.Errorf("unknown web setting %q", key)
	}
	spec := webSettingSpecs[index]
	switch spec.Kind {
	case WebSettingSwitch:
		if value != "on" && value != "off" {
			return fmt.Errorf("web setting %q must be on or off", key)
		}
	case WebSettingChoice:
		if len(spec.Choices) > 0 && !slices.Contains(spec.Choices, value) {
			return fmt.Errorf("web setting %q must be one of %s", key, quotedList(spec.Choices))
		}
		if strings.ContainsAny(value, "\n\r") {
			return fmt.Errorf("web setting %q must be one line", key)
		}
	}
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[key] = value
	kept := make([]webSettingSkipped, 0, len(s.skipped))
	for _, e := range s.skipped {
		if e.table != table || e.name != name {
			kept = append(kept, e)
		}
	}
	s.skipped = kept
	return nil
}

// webSettingLocation maps an API key to its registry entry and TOML location.
// It succeeds only when the location maps back to the same key, so a key a
// literal entry shadows is never written where it would read back as another.
func webSettingLocation(key string) (int, string, string, bool) {
	segments := strings.Split(key, ".")
	for index, spec := range webSettingSpecs {
		bind := map[string]string{}
		if !matchWebSettingSegments(strings.Split(spec.Key, "."), segments, bind) {
			continue
		}
		table := substituteWebSettingPlaceholders(spec.Table, bind)
		name := substituteWebSettingPlaceholders(spec.Name, bind)
		back, again, ok := webSettingForTOML(table, name)
		if !ok || back != index || again != key {
			return 0, "", "", false
		}
		return index, table, name, true
	}
	return 0, "", "", false
}

// webSettingForTOML maps a TOML location to its registry entry and API key.
func webSettingForTOML(table, name string) (int, string, bool) {
	actual := append(strings.Split(table, "."), name)
	for index, spec := range webSettingSpecs {
		bind := map[string]string{}
		pattern := append(strings.Split(spec.Table, "."), spec.Name)
		if matchWebSettingSegments(pattern, actual, bind) {
			return index, substituteWebSettingPlaceholders(spec.Key, bind), true
		}
	}
	return 0, "", false
}

func isWebSettingPlaceholder(segment string) bool {
	return len(segment) > 2 && segment[0] == '<' && segment[len(segment)-1] == '>'
}

func matchWebSettingSegments(pattern, actual []string, bind map[string]string) bool {
	if len(pattern) != len(actual) {
		return false
	}
	for i, want := range pattern {
		got := actual[i]
		if !isWebSettingPlaceholder(want) {
			if want != got {
				return false
			}
			continue
		}
		if !isTOMLBareKey(got) {
			return false
		}
		if bound, ok := bind[want]; ok && bound != got {
			return false
		}
		bind[want] = got
	}
	return true
}

func substituteWebSettingPlaceholders(pattern string, bind map[string]string) string {
	segments := strings.Split(pattern, ".")
	for i, segment := range segments {
		if value, ok := bind[segment]; ok {
			segments[i] = value
		}
	}
	return strings.Join(segments, ".")
}

// LoadWebSettingsFile reads web.toml. A missing file is an empty layer. Keys
// this build does not know are skipped (see ParseWebSettings). A file that
// cannot be read is a *WebSettingsError naming the line; nothing in it is
// used.
func LoadWebSettingsFile(path string, known WebSettingKnown) (WebSettings, error) {
	if strings.TrimSpace(path) == "" {
		return WebSettings{}, ErrHomeDirRequired
	}
	noteFrontFile(path)
	// #nosec G304 -- path is the resolved projmux configuration file supplied by the caller.
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return WebSettings{}, nil
		}
		return WebSettings{}, fmt.Errorf("read web settings: %w", err)
	}
	return ParseWebSettings(path, content, known)
}

// ParseWebSettings parses web.toml content. path is only used in errors.
//
// The accepted syntax is a strict TOML subset: blank lines, `#` comments,
// `[table]` / `[a.b]` headers of bare keys, and `key = "string"`,
// `key = 'string'` or `key = true|false` with a bare key. A key the registry
// does not have, or a dynamic key known rejects, is skipped: its value is not
// checked or applied, and Skipped lists it. Anything else -- a known key's
// value of the wrong kind, a repeated key or table, an array of tables, a
// dotted or quoted key, a missing value or a multi-line string, a skipped key
// whose path collides with a table or key the registry or the file uses --
// is an error naming the line.
func ParseWebSettings(path string, content []byte, known WebSettingKnown) (WebSettings, error) {
	settings := WebSettings{values: map[string]string{}}
	fail := func(line int, format string, args ...any) (WebSettings, error) {
		return WebSettings{}, &WebSettingsError{Path: path, Line: line, Message: fmt.Sprintf(format, args...)}
	}
	tables := map[string]bool{}
	keys := map[string]int{}
	// The paths skipped entries set as values and use as tables, by line.
	skippedValues := map[string]int{}
	skippedTables := map[string]int{}
	table := ""
	for i, raw := range strings.Split(string(content), "\n") {
		n := i + 1
		if !utf8.ValidString(raw) {
			return fail(n, "line is not valid UTF-8")
		}
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || line[0] == '#' {
			continue
		}
		if line[0] == '[' {
			if strings.HasPrefix(line, "[[") {
				return fail(n, "arrays of tables are not supported")
			}
			end := strings.IndexByte(line, ']')
			if end < 0 {
				return fail(n, "unterminated table header")
			}
			if rest := strings.TrimSpace(line[end+1:]); rest != "" && rest[0] != '#' {
				return fail(n, "unexpected text after table header: %q", rest)
			}
			name, ok := parseTOMLTableName(line[1:end])
			if !ok {
				return fail(n, "table name %q must be bare keys joined by dots", strings.TrimSpace(line[1:end]))
			}
			if tables[name] {
				return fail(n, "table [%s] is defined twice", name)
			}
			tables[name], table = true, name
			continue
		}
		before, after, ok := strings.Cut(line, "=")
		if !ok {
			return fail(n, "expected key = value")
		}
		name := strings.TrimSpace(before)
		if !isTOMLBareKey(name) {
			return fail(n, "key %q must be a bare key under a [table] header", name)
		}
		dotted := name
		if table != "" {
			dotted = table + "." + name
		}
		if first, dup := keys[dotted]; dup {
			return fail(n, "key %q is already set on line %d", dotted, first)
		}
		keys[dotted] = n
		index, key, ok := webSettingForTOML(table, name)
		if !ok || (webSettingSpecs[index].dynamic() && known != nil && !known(key)) {
			raw := strings.TrimSpace(after)
			if err := checkSkippedTOMLValue(raw); err != nil {
				return fail(n, "key %q: %v", dotted, err)
			}
			if message := webSettingOverlap(table, name); message != "" {
				return fail(n, "key %q overlaps %s this build uses", dotted, message)
			}
			if first, clash := skippedTables[dotted]; clash {
				return fail(n, "key %q overlaps the table [%s] used on line %d", dotted, dotted, first)
			}
			prefixes := webSettingTablePrefixes(table)
			for _, prefix := range prefixes {
				if first, clash := skippedValues[prefix]; clash {
					return fail(n, "key %q overlaps the key %q set on line %d", dotted, prefix, first)
				}
			}
			for _, prefix := range prefixes {
				if _, seen := skippedTables[prefix]; !seen {
					skippedTables[prefix] = n
				}
			}
			skippedValues[dotted] = n
			settings.skipped = append(settings.skipped, webSettingSkipped{line: n, table: table, name: name, raw: raw})
			continue
		}
		value, err := parseTOMLScalar(after)
		if err != nil {
			return fail(n, "key %q: %v", dotted, err)
		}
		spec := webSettingSpecs[index]
		switch spec.Kind {
		case WebSettingSwitch:
			if !value.isBool {
				return fail(n, "key %q must be true or false", dotted)
			}
			settings.values[key] = "off"
			if value.boolean {
				settings.values[key] = "on"
			}
		case WebSettingChoice:
			if value.isBool {
				return fail(n, "key %q must be a string", dotted)
			}
			if len(spec.Choices) > 0 && !slices.Contains(spec.Choices, value.text) {
				return fail(n, "key %q must be one of %s", dotted, quotedList(spec.Choices))
			}
			settings.values[key] = value.text
		}
	}
	return settings, nil
}

// webSettingTablePrefixes lists table and each table above it, outermost
// first: "a.b" is "a", "a.b". The top level ("") has none.
func webSettingTablePrefixes(table string) []string {
	if table == "" {
		return nil
	}
	segments := strings.Split(table, ".")
	out := make([]string, len(segments))
	for i := range segments {
		out[i] = strings.Join(segments[:i+1], ".")
	}
	return out
}

// webSettingOverlap describes how a skipped entry at [table] name collides
// with the registry, or returns "": its path would be a table the registry
// uses, or a table it sits in would be a key the registry uses. Writing it
// back would give a file that is not valid TOML once both are set.
func webSettingOverlap(table, name string) string {
	path := []string{name}
	if table != "" {
		path = append(strings.Split(table, "."), name)
	}
	for _, spec := range webSettingSpecs {
		specTable := strings.Split(spec.Table, ".")
		if len(path) <= len(specTable) && matchWebSettingSegments(specTable[:len(path)], path, map[string]string{}) {
			return "the table [" + strings.Join(path, ".") + "]"
		}
	}
	for _, prefix := range webSettingTablePrefixes(table) {
		segments := strings.Split(prefix, ".")
		for _, spec := range webSettingSpecs {
			pattern := append(strings.Split(spec.Table, "."), spec.Name)
			if matchWebSettingSegments(pattern, segments, map[string]string{}) {
				return "the key " + strconv.Quote(prefix)
			}
		}
	}
	return ""
}

// checkSkippedTOMLValue checks the raw value of a skipped entry for what a
// verbatim rewrite would break: a missing value (or only a comment) and a
// multi-line string. The value itself is not checked.
func checkSkippedTOMLValue(raw string) error {
	switch {
	case raw == "", raw[0] == '#':
		return errors.New("missing value")
	case strings.HasPrefix(raw, `"""`), strings.HasPrefix(raw, "'''"):
		return errors.New("multi-line strings are not supported")
	}
	return nil
}

type tomlScalar struct {
	text    string
	boolean bool
	isBool  bool
}

// parseTOMLScalar reads one value and the optional trailing comment.
func parseTOMLScalar(raw string) (tomlScalar, error) {
	raw = strings.TrimSpace(raw)
	var value tomlScalar
	var rest string
	switch {
	case raw == "":
		return value, errors.New("missing value")
	case strings.HasPrefix(raw, `"""`), strings.HasPrefix(raw, "'''"):
		return value, errors.New("multi-line strings are not supported")
	case raw[0] == '"':
		text, n, err := decodeTOMLBasicString(raw)
		if err != nil {
			return value, err
		}
		value.text, rest = text, raw[n:]
	case raw[0] == '\'':
		end := strings.IndexByte(raw[1:], '\'')
		if end < 0 {
			return value, errors.New("unterminated string")
		}
		value.text, rest = raw[1:end+1], raw[end+2:]
	default:
		token := raw
		if cut := strings.IndexAny(raw, " \t#"); cut >= 0 {
			token, rest = raw[:cut], raw[cut:]
		}
		switch token {
		case "true", "false":
			value.isBool, value.boolean = true, token == "true"
		default:
			return value, fmt.Errorf("unsupported value %q: web.toml takes a quoted string or true/false", token)
		}
	}
	if rest = strings.TrimSpace(rest); rest != "" && rest[0] != '#' {
		return value, fmt.Errorf("unexpected text after value: %q", rest)
	}
	return value, nil
}

// decodeTOMLBasicString decodes a "..." string at the start of raw and returns
// it with the number of bytes consumed.
func decodeTOMLBasicString(raw string) (string, int, error) {
	var b strings.Builder
	for i := 1; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '"':
			return b.String(), i + 1, nil
		case c == '\\':
			if i+1 >= len(raw) {
				return "", 0, errors.New("unterminated string")
			}
			i++
			switch raw[i] {
			case '"', '\\':
				b.WriteByte(raw[i])
			case 'b':
				b.WriteByte('\b')
			case 't':
				b.WriteByte('\t')
			case 'n':
				b.WriteByte('\n')
			case 'f':
				b.WriteByte('\f')
			case 'r':
				b.WriteByte('\r')
			case 'u', 'U':
				width := 4
				if raw[i] == 'U' {
					width = 8
				}
				if i+width >= len(raw) {
					return "", 0, errors.New("short unicode escape")
				}
				code, ok := decodeTOMLUnicodeEscape(raw[i+1 : i+1+width])
				if !ok {
					return "", 0, fmt.Errorf("invalid unicode escape %q", raw[i-1:i+1+width])
				}
				b.WriteRune(code)
				i += width
			default:
				return "", 0, fmt.Errorf("invalid escape \\%c", raw[i])
			}
		case c < 0x20 && c != '\t', c == 0x7f:
			return "", 0, errors.New("control character in string")
		default:
			b.WriteByte(c)
		}
	}
	return "", 0, errors.New("unterminated string")
}

// decodeTOMLUnicodeEscape reads the hex digits of a \uXXXX or \UXXXXXXXX
// escape into a rune. It accepts only hex digits and a valid Unicode scalar
// value: a value past utf8.MaxRune or a surrogate is refused.
func decodeTOMLUnicodeEscape(digits string) (rune, bool) {
	var code rune
	for i := 0; i < len(digits); i++ {
		var digit rune
		switch c := rune(digits[i]); {
		case c >= '0' && c <= '9':
			digit = c - '0'
		case c >= 'a' && c <= 'f':
			digit = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			digit = c - 'A' + 10
		default:
			return 0, false
		}
		code = code<<4 | digit
		if code > utf8.MaxRune {
			return 0, false
		}
	}
	return code, digits != "" && utf8.ValidRune(code)
}

func parseTOMLTableName(raw string) (string, bool) {
	segments := strings.Split(raw, ".")
	for i, segment := range segments {
		segment = strings.TrimSpace(segment)
		if !isTOMLBareKey(segment) {
			return "", false
		}
		segments[i] = segment
	}
	return strings.Join(segments, "."), true
}

func isTOMLBareKey(key string) bool {
	if key == "" {
		return false
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

func quotedList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = strconv.Quote(value)
	}
	return strings.Join(quoted, ", ")
}

const webSettingsFileHeader = `# projmux web settings. The web UI writes this file; a key here changes the
# web only and overrides the TUI or central value it names. Remove a key to
# follow that value again. See docs/configuration.md#web-settings-webtoml.
`

// RenderWebSettings is the deterministic web.toml text for s: tables and keys
// in registry order, dynamic tables and keys by name. Skipped entries are
// written back verbatim: top-level ones before the first table, the rest
// after the known keys of their table, and tables holding only skipped
// entries last, in the order the file had them.
func RenderWebSettings(s WebSettings) []byte {
	var b strings.Builder
	b.WriteString(webSettingsFileHeader)
	writeSkipped := func(table string) {
		for _, e := range s.skipped {
			if e.table == table {
				b.WriteString(e.name + " = " + e.raw + "\n")
			}
		}
	}
	// A top-level key after a header would belong to that table.
	if slices.ContainsFunc(s.skipped, func(e webSettingSkipped) bool { return e.table == "" }) {
		b.WriteString("\n")
		writeSkipped("")
	}
	written := map[string]bool{"": true}
	table := ""
	for _, key := range s.Keys() {
		index, keyTable, name, _ := webSettingLocation(key)
		if keyTable != table {
			if table != "" {
				writeSkipped(table)
			}
			table = keyTable
			written[table] = true
			b.WriteString("\n[" + table + "]\n")
		}
		value := s.values[key]
		switch webSettingSpecs[index].Kind {
		case WebSettingSwitch:
			if value == "on" {
				value = "true"
			} else {
				value = "false"
			}
		default:
			value = encodeTOMLBasicString(value)
		}
		b.WriteString(name + " = " + value + "\n")
	}
	if table != "" {
		writeSkipped(table)
	}
	for _, e := range s.skipped {
		if !written[e.table] {
			written[e.table] = true
			b.WriteString("\n[" + e.table + "]\n")
			writeSkipped(e.table)
		}
	}
	return []byte(b.String())
}

func encodeTOMLBasicString(value string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range value {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "\\u%04X", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// webSettingsCommit moves the fully written temp file over web.toml. A
// variable so a test can interrupt a save between the write and the rename.
var webSettingsCommit = os.Rename

// SaveWebSettingsFile writes s to path atomically: a 0600 temp file in the
// same directory, fsynced, then renamed over path. A reader sees the old file
// or the new one, never a partial write.
func SaveWebSettingsFile(path string, s WebSettings) error {
	if strings.TrimSpace(path) == "" {
		return ErrHomeDirRequired
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create web settings directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create web settings temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod web settings temp file: %w", err)
	}
	if _, err := tmp.Write(RenderWebSettings(s)); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write web settings temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync web settings temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close web settings temp file: %w", err)
	}
	if err := webSettingsCommit(tmpName, path); err != nil {
		return fmt.Errorf("replace web settings: %w", err)
	}
	// The rename is durable once the directory entry is; a failed directory
	// sync leaves a complete file either way.
	if d, err := os.Open(dir); err == nil { // #nosec G304 -- dir is the directory of the caller's config path.
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// UpdateWebSettingsFile loads web.toml, applies update, and saves the result.
// Keys this build does not know are written back as they were. A file that
// fails to load is returned as the error and left untouched, so a web change
// never overwrites a hand edit it could not read.
func UpdateWebSettingsFile(path string, known WebSettingKnown, update func(*WebSettings) error) error {
	settings, err := LoadWebSettingsFile(path, known)
	if err != nil {
		return err
	}
	if err := update(&settings); err != nil {
		return err
	}
	return SaveWebSettingsFile(path, settings)
}
