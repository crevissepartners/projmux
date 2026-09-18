package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
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
		{"usage master is a table", "[statusbar]\nusage = false\n", `/x/web.toml:2: key "statusbar.usage" overlaps the table [statusbar.usage] this build uses`},
		{"unknown key in a provider's place", "[statusbar.usage]\ncodex = false\n", `/x/web.toml:2: key "statusbar.usage.codex" overlaps the table [statusbar.usage.codex] this build uses`},
		{"top-level key in a table's place", "statusbar = false\n", `/x/web.toml:1: key "statusbar" overlaps the table [statusbar] this build uses`},
		{"unknown table under a known key", "[statusbar.git]\nx = 1\n", `/x/web.toml:2: key "statusbar.git.x" overlaps the key "statusbar.git" this build uses`},
		{"unknown key then a table under it", "[confirm]\nstop = true\n[confirm.stop]\nx = 1\n", `/x/web.toml:4: key "confirm.stop.x" overlaps the key "confirm.stop" set on line 2`},
		{"unknown table then a key in its place", "[confirm.stop]\nx = 1\n[confirm]\nstop = true\n", `/x/web.toml:4: key "confirm.stop" overlaps the table [confirm.stop] used on line 2`},
		{"unknown key with a multi-line string", "[theme]\npreset = \"\"\"forest\"\"\"\n", `/x/web.toml:2: key "theme.preset": multi-line strings are not supported`},
		{"unknown key with a multi-line literal string", "[theme]\npreset = '''forest\n", `/x/web.toml:2: key "theme.preset": multi-line strings are not supported`},
		{"unknown key with a missing value", "[theme]\npreset =\n", `/x/web.toml:2: key "theme.preset": missing value`},
		{"unknown key with only a comment", "[theme]\npreset = # later\n", `/x/web.toml:2: key "theme.preset": missing value`},
		{"repeated unknown key", "[theme]\npreset = 1\npreset = 2\n", `/x/web.toml:3: key "theme.preset" is already set on line 2`},
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
	for _, tc := range []struct {
		content string
		want    SkippedWebSetting
	}{
		{"[statusbar.usage.claude]\nmonthly = false\n", SkippedWebSetting{Line: 2, Key: "statusbar.usage.claude.monthly"}},
		{"\n[statusbar.usage.gemini]\nvisible = true\n", SkippedWebSetting{Line: 3, Key: "statusbar.usage.gemini.visible"}},
	} {
		settings, err := ParseWebSettings("/x/web.toml", []byte(tc.content), known)
		if err != nil {
			t.Fatalf("%q: %v", tc.content, err)
		}
		if got := settings.Skipped(); !slices.Equal(got, []SkippedWebSetting{tc.want}) {
			t.Fatalf("%q: skipped = %v", tc.content, got)
		}
		if keys := settings.Keys(); len(keys) != 0 {
			t.Fatalf("%q: applied %v", tc.content, keys)
		}
	}
}

func TestWebSettingsParseSkipsKeysThisBuildDoesNotKnow(t *testing.T) {
	content := "[statusbar]\ngit = false\n[ai]\nnew_window_mode = \"resume\"\n[confirm]\nstop_agent = true\n"
	settings, err := ParseWebSettings("/x/web.toml", []byte(content), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := settings.Value("statusbar.git"); !ok || got != "off" {
		t.Fatalf("statusbar.git = %q %v", got, ok)
	}
	want := []SkippedWebSetting{{Line: 4, Key: "ai.new_window_mode"}, {Line: 6, Key: "confirm.stop_agent"}}
	if got := settings.Skipped(); !slices.Equal(got, want) {
		t.Fatalf("skipped = %v, want %v", got, want)
	}
	if keys := settings.Keys(); !slices.Equal(keys, []string{"statusbar.git"}) {
		t.Fatalf("keys = %v", keys)
	}
	for _, key := range []string{"ai.new_window_mode", "ai.newWindowMode", "confirm.stop_agent", "confirm.stopAgent"} {
		if _, ok := settings.Value(key); ok {
			t.Errorf("a skipped entry is a value: %s", key)
		}
	}

	// An unknown table and a top-level key are skipped too, and a value
	// that no web.toml key takes is not checked.
	content = "git = false\n[theme]\npreset = \"forest\"\nsize = 12 # px\n[statusbar]\ngti = [1, 2]\n"
	settings, err = ParseWebSettings("/x/web.toml", []byte(content), nil)
	if err != nil {
		t.Fatal(err)
	}
	want = []SkippedWebSetting{{Line: 1, Key: "git"}, {Line: 3, Key: "theme.preset"}, {Line: 4, Key: "theme.size"}, {Line: 6, Key: "statusbar.gti"}}
	if got := settings.Skipped(); !slices.Equal(got, want) {
		t.Fatalf("skipped = %v, want %v", got, want)
	}
	if keys := settings.Keys(); len(keys) != 0 {
		t.Fatalf("keys = %v", keys)
	}

	// The returned slice is a copy.
	got := settings.Skipped()
	got[0] = SkippedWebSetting{Line: 99, Key: "changed"}
	if again := settings.Skipped(); !slices.Equal(again, want) {
		t.Fatalf("mutating Skipped() changed the settings: %v", again)
	}
}

func TestWebSettingsParseSkipsDynamicKeysTheCallerDoesNotKnow(t *testing.T) {
	known := func(key string) bool {
		return key == "statusbar.usage.codex" || key == "statusbar.usage.codex.5h"
	}
	content := "[statusbar.usage.nosuchprovider]\nvisible = false\n[statusbar.usage.codex]\nvisible = true\nmonthly = false\n5h = false\n"
	settings, err := ParseWebSettings("/x/web.toml", []byte(content), known)
	if err != nil {
		t.Fatal(err)
	}
	want := []SkippedWebSetting{{Line: 2, Key: "statusbar.usage.nosuchprovider.visible"}, {Line: 5, Key: "statusbar.usage.codex.monthly"}}
	if got := settings.Skipped(); !slices.Equal(got, want) {
		t.Fatalf("skipped = %v, want %v", got, want)
	}
	if keys := settings.Keys(); !slices.Equal(keys, []string{"statusbar.usage.codex", "statusbar.usage.codex.5h"}) {
		t.Fatalf("keys = %v", keys)
	}
	// The provider table holds a known and a skipped key: one header, the
	// skipped key after the known ones.
	rendered := string(RenderWebSettings(settings))
	wantText := webSettingsFileHeader + `
[statusbar.usage.codex]
visible = true
5h = false
monthly = false

[statusbar.usage.nosuchprovider]
visible = false
`
	if rendered != wantText {
		t.Fatalf("render =\n%s\nwant\n%s", rendered, wantText)
	}
	for _, again := range []WebSettingKnown{nil, known} {
		parsed, err := ParseWebSettings("/x/web.toml", []byte(rendered), again)
		if err != nil {
			t.Fatal(err)
		}
		if twice := string(RenderWebSettings(parsed)); twice != rendered {
			t.Fatalf("round trip changed the file:\n%s", twice)
		}
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
	broken := []byte("[statusbar]\ngit = \"off\"\n")
	if err := os.WriteFile(path, broken, 0o600); err != nil {
		t.Fatal(err)
	}
	err := UpdateWebSettingsFile(path, nil, func(s *WebSettings) error { return s.Set("statusbar.clock", "off") })
	if err == nil || err.Error() != path+`:2: key "statusbar.git" must be true or false` {
		t.Fatalf("err = %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, broken) {
		t.Fatalf("a broken web.toml was rewritten:\n%s", after)
	}
}

func TestWebSettingsUpdateKeepsKeysThisBuildDoesNotKnow(t *testing.T) {
	path := filepath.Join(t.TempDir(), WebSettingsFileName)
	content := "[statusbar]\ngit = false\n[ai]\nnew_window_mode = \"resume\"\n[confirm]\nstop_agent = true\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateWebSettingsFile(path, nil, func(s *WebSettings) error { return s.Set("statusbar.clock", "off") }); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(written)
	want := webSettingsFileHeader + `
[statusbar]
git = false
clock = false

[ai]
new_window_mode = "resume"

[confirm]
stop_agent = true
`
	if text != want {
		t.Fatalf("web.toml =\n%s\nwant\n%s", text, want)
	}
	for _, header := range []string{"\n[ai]\n", "\n[confirm]\n", "\n[statusbar]\n"} {
		if n := strings.Count(text, header); n != 1 {
			t.Errorf("%q appears %d times", header, n)
		}
	}
	for _, line := range []string{"\nnew_window_mode = \"resume\"\n", "\nstop_agent = true\n"} {
		if !strings.Contains(text, line) {
			t.Errorf("web.toml lost %q", line)
		}
	}
	settings, err := LoadWebSettingsFile(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := settings.Skipped(); !slices.Equal(got, []SkippedWebSetting{{Line: 10, Key: "ai.new_window_mode"}, {Line: 13, Key: "confirm.stop_agent"}}) {
		t.Fatalf("skipped = %v", got)
	}
	for _, key := range []string{"statusbar.git", "statusbar.clock"} {
		if got, ok := settings.Value(key); !ok || got != "off" {
			t.Errorf("%s = %q %v", key, got, ok)
		}
	}
	if again := string(RenderWebSettings(settings)); again != text {
		t.Fatalf("round trip changed the file:\n%s", again)
	}

	// A known key set later in a table that held only skipped entries
	// joins that table's one header.
	if err := UpdateWebSettingsFile(path, nil, func(s *WebSettings) error { return s.Set("ai.splitCwdFrom", "pane") }); err != nil {
		t.Fatal(err)
	}
	written, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want = webSettingsFileHeader + `
[ai]
split_cwd_from = "pane"
new_window_mode = "resume"

[statusbar]
git = false
clock = false

[confirm]
stop_agent = true
`
	if string(written) != want {
		t.Fatalf("web.toml =\n%s\nwant\n%s", written, want)
	}
}

func TestWebSettingsUnknownKeyInAKnownTableStaysUnderItsHeader(t *testing.T) {
	content := "[statusbar]\ngti = false\nclock = true # hand edit\nresources_extra = \"cpu\" # kept\ngit = false\n"
	settings, err := ParseWebSettings("/x/web.toml", []byte(content), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := string(RenderWebSettings(settings))
	want := webSettingsFileHeader + `
[statusbar]
git = false
clock = true
gti = false
resources_extra = "cpu" # kept
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
}

func TestWebSettingsSetReplacesASkippedEntryAtTheSameLocation(t *testing.T) {
	known := func(key string) bool { return key == "statusbar.usage.codex" }
	content := "[statusbar.usage.codex]\nmonthly = false\n"
	settings, err := ParseWebSettings("/x/web.toml", []byte(content), known)
	if err != nil {
		t.Fatal(err)
	}
	if got := settings.Skipped(); !slices.Equal(got, []SkippedWebSetting{{Line: 2, Key: "statusbar.usage.codex.monthly"}}) {
		t.Fatalf("skipped = %v", got)
	}
	if err := settings.Set("statusbar.usage.codex.monthly", "on"); err != nil {
		t.Fatal(err)
	}
	if got := settings.Skipped(); len(got) != 0 {
		t.Fatalf("skipped after Set = %v", got)
	}
	rendered := string(RenderWebSettings(settings))
	if n := strings.Count(rendered, "monthly = "); n != 1 || !strings.Contains(rendered, "\nmonthly = true\n") {
		t.Fatalf("render =\n%s", rendered)
	}
	parsed, err := ParseWebSettings("/x/web.toml", []byte(rendered), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Skipped(); len(got) != 0 {
		t.Fatalf("skipped after re-parse = %v", got)
	}
	if got, ok := parsed.Value("statusbar.usage.codex.monthly"); !ok || got != "on" {
		t.Fatalf("monthly = %q %v", got, ok)
	}
}

func TestWebSettingsSkippedTopLevelKeysStayBeforeTheFirstTable(t *testing.T) {
	// After a header, `git = false` would belong to that table, so a
	// top-level key is written right after the file header.
	content := "git = false\nlocale = \"ko\"\n[statusbar]\ngit = false\n[theme]\npreset = \"forest\"\n"
	settings, err := ParseWebSettings("/x/web.toml", []byte(content), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.Set("statusbar.clock", "off"); err != nil {
		t.Fatal(err)
	}
	got := string(RenderWebSettings(settings))
	want := webSettingsFileHeader + `
git = false
locale = "ko"

[statusbar]
git = false
clock = false

[theme]
preset = "forest"
`
	if got != want {
		t.Fatalf("render =\n%s\nwant\n%s", got, want)
	}
	parsed, err := ParseWebSettings("/x/web.toml", []byte(got), nil)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, e := range parsed.Skipped() {
		keys = append(keys, e.Key)
	}
	if !slices.Equal(keys, []string{"git", "locale", "theme.preset"}) {
		t.Fatalf("skipped = %v", parsed.Skipped())
	}
	if got := parsed.Keys(); !slices.Equal(got, []string{"statusbar.git", "statusbar.clock"}) {
		t.Fatalf("keys = %v", got)
	}
	if again := string(RenderWebSettings(parsed)); again != got {
		t.Fatalf("round trip changed the file:\n%s", again)
	}

	// A file of only top-level keys keeps them after the file header.
	only, err := ParseWebSettings("/x/web.toml", []byte("git = false\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(RenderWebSettings(only)); got != webSettingsFileHeader+"\ngit = false\n" {
		t.Fatalf("render =\n%s", got)
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
