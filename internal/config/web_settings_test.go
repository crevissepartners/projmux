package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebSettingsFileLivesInConfigDir(t *testing.T) {
	paths := Paths{ConfigDir: "/cfg/projmux", StateDir: "/state/projmux"}
	if got := paths.WebSettingsFile(); got != "/cfg/projmux/web.toml" {
		t.Fatalf("WebSettingsFile() = %q", got)
	}
}

func TestWebSettingsMissingFileIsAnEmptyLayer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.toml")
	settings, err := LoadWebSettingsFile(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if keys := settings.Keys(); len(keys) != 0 {
		t.Fatalf("keys = %v", keys)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("loading created the file: %v", err)
	}
}

func TestWebSettingsParseReadsEveryRegistryShape(t *testing.T) {
	content := `# a comment
[ai]
split_cwd_from = "pane" # trailing comment

[statusbar]
notifications = false
project = true
working_directory = false
git = false
clock = true

[statusbar.usage]
visible = false

[ statusbar.usage.claude ]
visible = false
5h = true
weekly = false
`
	settings, err := ParseWebSettings("/x/web.toml", []byte(content), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"ai.splitCwdFrom":               "pane",
		"statusbar.notifications":       "off",
		"statusbar.project":             "on",
		"statusbar.working-directory":   "off",
		"statusbar.git":                 "off",
		"statusbar.clock":               "on",
		"statusbar.usage":               "off",
		"statusbar.usage.claude":        "off",
		"statusbar.usage.claude.5h":     "on",
		"statusbar.usage.claude.weekly": "off",
	}
	if got := settings.Keys(); len(got) != len(want) {
		t.Fatalf("keys = %v", got)
	}
	for key, value := range want {
		if got, ok := settings.Value(key); !ok || got != value {
			t.Errorf("%s = %q %v, want %q", key, got, ok, value)
		}
	}
}

func TestWebSettingsParseRejectsWithFileAndLine(t *testing.T) {
	for _, tc := range []struct {
		name, content, want string
	}{
		{"unknown key", "[statusbar]\ngit = true\ngti = false\n", `/x/web.toml:3: unknown key "statusbar.gti"`},
		{"unknown table", "[theme]\npreset = \"forest\"\n", `/x/web.toml:2: unknown key "theme.preset"`},
		{"top-level key", "git = false\n", `/x/web.toml:1: unknown key "git"`},
		{"usage master is a table", "[statusbar]\nusage = false\n", `/x/web.toml:2: unknown key "statusbar.usage"`},
		{"switch given a string", "[statusbar]\ngit = \"off\"\n", `/x/web.toml:2: key "statusbar.git" must be true or false`},
		{"choice given a bool", "[ai]\nsplit_cwd_from = true\n", `/x/web.toml:2: key "ai.split_cwd_from" must be a string`},
		{"choice outside its list", "[ai]\nsplit_cwd_from = \"home\"\n", `/x/web.toml:2: key "ai.split_cwd_from" must be one of "project", "pane"`},
		{"bare word", "[statusbar]\ngit = off\n", `/x/web.toml:2: key "statusbar.git": unsupported value "off"`},
		{"integer", "[statusbar]\ngit = 0\n", `/x/web.toml:2: key "statusbar.git": unsupported value "0"`},
		{"no equals", "[statusbar]\ngit\n", `/x/web.toml:2: expected key = value`},
		{"dotted key", "[statusbar]\nusage.visible = false\n", `/x/web.toml:2: key "usage.visible" must be a bare key`},
		{"quoted key", "[statusbar]\n\"git\" = false\n", `/x/web.toml:2: key "\"git\"" must be a bare key`},
		{"repeated key", "[statusbar]\ngit = true\ngit = false\n", `/x/web.toml:3: key "statusbar.git" is already set on line 2`},
		{"repeated table", "[statusbar]\n[ai]\n[statusbar]\n", `/x/web.toml:3: table [statusbar] is defined twice`},
		{"array of tables", "[[statusbar]]\n", `/x/web.toml:1: arrays of tables are not supported`},
		{"unterminated header", "[statusbar\n", `/x/web.toml:1: unterminated table header`},
		{"text after value", "[statusbar]\ngit = false true\n", `/x/web.toml:2: key "statusbar.git": unexpected text after value`},
		{"unterminated string", "[ai]\nsplit_cwd_from = \"pane\n", `/x/web.toml:2: key "ai.split_cwd_from": unterminated string`},
		{"multi-line string", "[ai]\nsplit_cwd_from = \"\"\"pane\"\"\"\n", `/x/web.toml:2: key "ai.split_cwd_from": multi-line strings are not supported`},
		{"missing value", "[statusbar]\ngit =\n", `/x/web.toml:2: key "statusbar.git": missing value`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseWebSettings("/x/web.toml", []byte(tc.content), nil)
			var typed *WebSettingsError
			if !errors.As(err, &typed) {
				t.Fatalf("err = %v, want a *WebSettingsError", err)
			}
			if !strings.HasPrefix(err.Error(), tc.want) {
				t.Fatalf("err = %q, want prefix %q", err, tc.want)
			}
			if typed.Path != "/x/web.toml" || typed.Line == 0 {
				t.Fatalf("err names %q line %d", typed.Path, typed.Line)
			}
		})
	}
}

func TestWebSettingsParseAsksTheCallerAboutDynamicKeysOnly(t *testing.T) {
	var asked []string
	known := func(key string) bool {
		asked = append(asked, key)
		return key == "statusbar.usage.claude" || key == "statusbar.usage.claude.5h"
	}
	content := "[statusbar]\ngit = false\n[statusbar.usage.claude]\nvisible = false\n5h = false\n"
	if _, err := ParseWebSettings("/x/web.toml", []byte(content), known); err != nil {
		t.Fatal(err)
	}
	if strings.Join(asked, ",") != "statusbar.usage.claude,statusbar.usage.claude.5h" {
		t.Fatalf("asked = %v", asked)
	}
	_, err := ParseWebSettings("/x/web.toml", []byte("[statusbar.usage.claude]\nmonthly = false\n"), known)
	if err == nil || err.Error() != `/x/web.toml:2: unknown key "statusbar.usage.claude.monthly"` {
		t.Fatalf("err = %v", err)
	}
	_, err = ParseWebSettings("/x/web.toml", []byte("\n[statusbar.usage.gemini]\nvisible = true\n"), known)
	if err == nil || err.Error() != `/x/web.toml:3: unknown key "statusbar.usage.gemini.visible"` {
		t.Fatalf("err = %v", err)
	}
}

func TestWebSettingsSetValidatesKeyShapeAndValue(t *testing.T) {
	var s WebSettings
	for _, bad := range [][2]string{
		{"statusbar.gti", "on"},
		{"statusbar.resources", "on"},
		{"locale", "ko-KR"},
		{"ai.provider.codex", "off"},
		{"statusbar.git", "false"},
		{"ai.splitCwdFrom", "home"},
		// A provider named like the node's own key would read back as the
		// provider's visibility, so it is refused rather than misfiled.
		{"statusbar.usage.claude.visible", "on"},
		{"statusbar.usage.claude.5h.extra", "on"},
		{"statusbar.usage.cla ude", "on"},
	} {
		if err := s.Set(bad[0], bad[1]); err == nil {
			t.Errorf("Set(%q, %q) succeeded", bad[0], bad[1])
		}
	}
	if keys := s.Keys(); len(keys) != 0 {
		t.Fatalf("a refused Set recorded %v", keys)
	}
}

func TestWebSettingsRenderIsDeterministicAndRoundTrips(t *testing.T) {
	var s WebSettings
	// Set in an order unlike the written one.
	for _, kv := range [][2]string{
		{"statusbar.usage.codex.weekly", "off"},
		{"statusbar.clock", "off"},
		{"statusbar.usage.claude.5h", "off"},
		{"statusbar.usage", "on"},
		{"ai.splitCwdFrom", "pane"},
		{"statusbar.usage.claude", "off"},
		{"statusbar.working-directory", "on"},
		{"statusbar.git", "off"},
	} {
		if err := s.Set(kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	got := string(RenderWebSettings(s))
	want := webSettingsFileHeader + `
[ai]
split_cwd_from = "pane"

[statusbar]
working_directory = true
git = false
clock = false

[statusbar.usage]
visible = true

[statusbar.usage.claude]
visible = false
5h = false

[statusbar.usage.codex]
weekly = false
`
	if got != want {
		t.Fatalf("render =\n%s\nwant\n%s", got, want)
	}
	parsed, err := ParseWebSettings("/x/web.toml", []byte(got), nil)
	if err != nil {
		t.Fatal(err)
	}
	if again := string(RenderWebSettings(parsed)); again != got {
		t.Fatalf("round trip changed the file:\n%s", again)
	}
	for _, key := range s.Keys() {
		a, _ := s.Value(key)
		b, _ := parsed.Value(key)
		if a != b {
			t.Errorf("%s: %q -> %q", key, a, b)
		}
	}
}

func TestWebSettingsStringEscapesRoundTrip(t *testing.T) {
	value := "a\"b\\c\td é"
	encoded := encodeTOMLBasicString(value)
	decoded, n, err := decodeTOMLBasicString(encoded)
	if err != nil || n != len(encoded) || decoded != value {
		t.Fatalf("decode(%s) = %q %d %v", encoded, decoded, n, err)
	}
	if got, _, err := decodeTOMLBasicString(`"é\U0001F600"`); err != nil || got != "é😀" {
		t.Fatalf("unicode escapes = %q %v", got, err)
	}
	if _, _, err := decodeTOMLBasicString(`"\x41"`); err == nil {
		t.Fatal("a Go-only escape was accepted")
	}
	value2, err := parseTOMLScalar(`'pa#ne' # c`)
	if err != nil || value2.text != "pa#ne" {
		t.Fatalf("literal string = %+v %v", value2, err)
	}
}

func TestWebSettingsUnicodeEscapesDecodeOnlyScalarValues(t *testing.T) {
	// The inputs spell the backslash as "\\" so the escapes reach the
	// decoder as text rather than being decoded by Go.
	for _, tc := range []struct {
		raw, want string
	}{
		{"\"\\u00e9\"", "\u00e9"},
		{"\"\\u00E9\"", "\u00e9"},
		{"\"\\U0001F600\"", "\U0001F600"},
		{"\"\\U0010FFFF\"", "\U0010FFFF"},
		{"\"\\u0041\\u0062\"", "Ab"},
	} {
		got, n, err := decodeTOMLBasicString(tc.raw)
		if err != nil || got != tc.want || n != len(tc.raw) {
			t.Errorf("decode(%s) = %q %d %v, want %q", tc.raw, got, n, err, tc.want)
		}
	}
	for _, tc := range []struct {
		raw, want string
	}{
		{"\"\\U00110000\"", `invalid unicode escape "\\U00110000"`},
		{"\"\\UFFFFFFFF\"", `invalid unicode escape "\\UFFFFFFFF"`},
		{"\"\\uD800\"", `invalid unicode escape "\\uD800"`},
		{"\"\\uDFFF\"", `invalid unicode escape "\\uDFFF"`},
		{"\"\\u00g9\"", `invalid unicode escape "\\u00g9"`},
		{"\"\\u+0e9\"", `invalid unicode escape "\\u+0e9"`},
		// Too few digits before the closing quote: the quote is read as a
		// digit and refused.
		{"\"\\u00e\"", `invalid unicode escape "\\u00e\""`},
		{"\"\\u00e", "short unicode escape"},
	} {
		if got, _, err := decodeTOMLBasicString(tc.raw); err == nil || err.Error() != tc.want {
			t.Errorf("decode(%s) = %q %v, want error %q", tc.raw, got, err, tc.want)
		}
	}
}

func TestWebSettingsSaveIsPrivateAndReadable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "projmux")
	path := filepath.Join(dir, WebSettingsFileName)
	err := UpdateWebSettingsFile(path, nil, func(s *WebSettings) error { return s.Set("statusbar.git", "off") })
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("web.toml mode = %o, want 600", mode)
	}
	settings, err := LoadWebSettingsFile(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := settings.Value("statusbar.git"); !ok || got != "off" {
		t.Fatalf("statusbar.git = %q %v", got, ok)
	}
	assertOnlyWebToml(t, dir)
}

func TestWebSettingsInterruptedSaveLeavesThePreviousFileWhole(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, WebSettingsFileName)
	if err := UpdateWebSettingsFile(path, nil, func(s *WebSettings) error { return s.Set("statusbar.git", "off") }); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	interrupted := errors.New("power cut")
	prev := webSettingsCommit
	t.Cleanup(func() { webSettingsCommit = prev })
	webSettingsCommit = func(tmp, target string) error {
		// The new content is complete in the temp file, and a reader of
		// web.toml at this instant still gets the previous file.
		staged, err := os.ReadFile(tmp)
		if err != nil || !strings.Contains(string(staged), "clock = false") {
			t.Errorf("staged temp file = %q %v", staged, err)
		}
		seen, err := LoadWebSettingsFile(target, nil)
		if err != nil {
			t.Errorf("reader during the save: %v", err)
		}
		if _, ok := seen.Value("statusbar.clock"); ok {
			t.Error("a reader saw the unfinished save")
		}
		return interrupted
	}
	err = UpdateWebSettingsFile(path, nil, func(s *WebSettings) error { return s.Set("statusbar.clock", "off") })
	if !errors.Is(err, interrupted) {
		t.Fatalf("save err = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("web.toml changed:\n%s\nwant\n%s", after, before)
	}
	assertOnlyWebToml(t, dir)
}

func TestWebSettingsUpdateNeverOverwritesAFileItCannotParse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, WebSettingsFileName)
	broken := []byte("[statusbar]\ngti = false\n")
	if err := os.WriteFile(path, broken, 0o600); err != nil {
		t.Fatal(err)
	}
	err := UpdateWebSettingsFile(path, nil, func(s *WebSettings) error { return s.Set("statusbar.git", "off") })
	if err == nil || err.Error() != path+`:2: unknown key "statusbar.gti"` {
		t.Fatalf("err = %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, broken) {
		t.Fatalf("a broken web.toml was rewritten:\n%s", after)
	}
}

func TestWebSettingsRegistryEntriesAreReachable(t *testing.T) {
	for index, spec := range webSettingSpecs {
		key := strings.NewReplacer("<provider>", "p1", "<window>", "w1").Replace(spec.Key)
		got, table, name, ok := webSettingLocation(key)
		if !ok || got != index {
			t.Errorf("%s resolves to entry %d %v, want %d", key, got, ok, index)
			continue
		}
		if back, again, ok := webSettingForTOML(table, name); !ok || back != index || again != key {
			t.Errorf("%s -> [%s] %s -> %s", key, table, name, again)
		}
		if spec.Kind != WebSettingSwitch && spec.Kind != WebSettingChoice {
			t.Errorf("%s has no kind", spec.Key)
		}
	}
}

func assertOnlyWebToml(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != WebSettingsFileName {
			t.Errorf("left behind %s", entry.Name())
		}
	}
}
